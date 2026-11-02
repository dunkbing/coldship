// Package offsite stores backups in an S3-compatible bucket (AWS S3,
// Cloudflare R2, Backblaze B2, Wasabi, MinIO, SeaweedFS…).
package offsite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/dunkbing/coldship/internal/netguard"
)

// Config is where an org's backups go.
type Config struct {
	Endpoint  string `json:"endpoint"` // https://s3.us-east-1.amazonaws.com, or host[:port]
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key,omitempty"`
	PathStyle bool   `json:"path_style,omitempty"`
	Keep      int    `json:"keep,omitempty"` // backups kept per database
}

const DefaultKeep = 30

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Normalize trims and checks the config, filling defaults.
func (c *Config) Normalize() error {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.Region = strings.TrimSpace(c.Region)
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.Prefix = strings.Trim(strings.TrimSpace(c.Prefix), "/")
	c.AccessKey = strings.TrimSpace(c.AccessKey)
	c.SecretKey = strings.TrimSpace(c.SecretKey)
	if c.Keep <= 0 {
		c.Keep = DefaultKeep
	}
	if c.Keep > 1000 {
		return errors.New("keep at most 1000 backups per database")
	}
	if _, _, err := c.endpoint(); err != nil {
		return err
	}
	if !bucketName.MatchString(c.Bucket) {
		return errors.New("bucket must be a valid S3 bucket name (3-63 lowercase letters, digits, dots and dashes)")
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return errors.New("give an access key and a secret key")
	}
	if c.Prefix != "" && (strings.Contains(c.Prefix, "..") || strings.ContainsAny(c.Prefix, "\\\x00")) {
		return errors.New("prefix may not contain .. or backslashes")
	}
	return nil
}

// endpoint returns host[:port] and whether to use TLS.
func (c Config) endpoint() (string, bool, error) {
	raw := c.Endpoint
	if raw == "" {
		return "", false, errors.New("give the S3 endpoint, such as https://s3.us-east-1.amazonaws.com")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return "", false, errors.New("endpoint must be a URL like https://s3.example.com, without a path")
	}
	switch u.Scheme {
	case "https":
		return u.Host, true, nil
	case "http":
		return u.Host, false, nil
	}
	return "", false, fmt.Errorf("endpoint scheme must be https or http, not %s", u.Scheme)
}

// Key joins the prefix and parts into an object key.
func (c Config) Key(parts ...string) string {
	return path.Join(append([]string{c.Prefix}, parts...)...)
}

// Remote is a bucket.
type Remote interface {
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// List returns every key under prefix.
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string) error
	// Check proves the bucket can be written to.
	Check(ctx context.Context) error
}

type s3 struct {
	c          *minio.Client
	bucket     string
	prefix     string
	restricted bool
}

// New connects to cfg's bucket. A restricted remote only talks to public
// addresses over HTTPS, and keeps the server's error messages out of its
// errors: it's set up by people who don't run this server.
func New(cfg Config, restricted bool) (Remote, error) {
	host, secure, err := cfg.endpoint()
	if err != nil {
		return nil, err
	}
	if restricted && !secure {
		return nil, errors.New("endpoint must use https")
	}
	tr, err := minio.DefaultTransport(secure)
	if err != nil {
		return nil, err
	}
	if restricted {
		tr.Proxy = nil
		tr.DialContext = netguard.Dialer().DialContext
	}
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	c, err := minio.New(host, &minio.Options{
		Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""), Secure: secure,
		Region: cfg.Region, BucketLookup: lookup, Transport: tr,
	})
	if err != nil {
		return nil, err
	}
	return &s3{c: c, bucket: cfg.Bucket, prefix: cfg.Prefix, restricted: restricted}, nil
}

// Public reports whether ip is a public unicast address.
func Public(ip net.IP) bool { return netguard.Public(ip) }

// explain turns a client error into a short message.
func (s *s3) explain(op string, err error) error {
	if err == nil {
		return nil
	}
	er := minio.ToErrorResponse(err)
	switch {
	case er.Code == "NoSuchBucket":
		return fmt.Errorf("%s: bucket %s does not exist", op, s.bucket)
	case er.Code == "AccessDenied" || er.Code == "InvalidAccessKeyId" || er.Code == "SignatureDoesNotMatch":
		return fmt.Errorf("%s: the bucket refused the keys (%s)", op, er.Code)
	case er.Code != "":
		if s.restricted {
			return fmt.Errorf("%s: the bucket answered %s", op, clip(er.Code, 60))
		}
		return fmt.Errorf("%s: %s: %s", op, clip(er.Code, 60), clip(er.Message, 200))
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("%s: %s", op, clip(err.Error(), 200))
}

func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (s *s3) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	_, err := s.c.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return s.explain("upload "+key, err)
}

func (s *s3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	o, err := s.c.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, s.explain("download "+key, err)
	}
	if _, err := o.Stat(); err != nil {
		o.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, s.explain("download "+key, err)
	}
	return o, nil
}

// ErrNotFound is a missing object.
var ErrNotFound = errors.New("not found")

func (s *s3) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	for o := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return nil, s.explain("list "+prefix, o.Err)
		}
		keys = append(keys, o.Key)
	}
	return keys, nil
}

func (s *s3) Delete(ctx context.Context, key string) error {
	return s.explain("delete "+key, s.c.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}))
}

func (s *s3) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	key := path.Join(s.prefix, ".coldship-check")
	body := []byte("coldship can write here\n")
	if err := s.Put(ctx, key, bytes.NewReader(body), int64(len(body))); err != nil {
		return err
	}
	if _, err := s.List(ctx, key); err != nil {
		return err
	}
	return s.Delete(ctx, key)
}

// Memory is an in-memory Remote, for tests.
type Memory struct {
	mu      sync.Mutex
	Objects map[string][]byte
	Fail    error // returned by every call when set
}

func NewMemory() *Memory { return &Memory{Objects: map[string][]byte{}} }

func (m *Memory) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.Objects[key] = b
	return nil
}

func (m *Memory) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return nil, m.Fail
	}
	b, ok := m.Objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *Memory) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return nil, m.Fail
	}
	var out []string
	for k := range m.Objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	delete(m.Objects, key)
	return nil
}

func (m *Memory) Check(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Fail
}

// SetFail makes every call fail with err (nil to recover).
func (m *Memory) SetFail(err error) {
	m.mu.Lock()
	m.Fail = err
	m.mu.Unlock()
}

// Keys lists every object, for tests.
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.Objects))
	for k := range m.Objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
