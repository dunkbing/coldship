package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/offsite"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

// Off-site backups: every snapshot is copied to the org's S3-compatible
// bucket, with a manifest next to it saying what it is. A bucket alone is
// enough to restore, on this server or a new one.
//
// Layout under the prefix:
//   data/<service>/<time>-<snapshot><ext>        a data service's dump
//   apps/<app>/<database>/<time>-<snapshot><ext> an app database's dump
//   …<ext>.json                                   its manifest
// A backup exists once its manifest does.

// Manifest describes one off-site backup.
type Manifest struct {
	Format     int       `json:"format"`     // 1
	Encryption string    `json:"encryption"` // "none": use the bucket's encryption
	Snapshot   string    `json:"snapshot"`
	App        string    `json:"app,omitempty"` // empty for data services
	Database   string    `json:"database"`
	Engine     string    `json:"engine"`
	Version    string    `json:"version"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Backup is an off-site backup: its dump's key and its manifest.
type Backup struct {
	Key string `json:"key"`
	Manifest
}

// BackupView is the org's destination, without its secret key.
type BackupView struct {
	Configured bool   `json:"configured"`
	Endpoint   string `json:"endpoint,omitempty"`
	Region     string `json:"region,omitempty"`
	Bucket     string `json:"bucket,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	AccessKey  string `json:"access_key,omitempty"`
	SecretSet  bool   `json:"secret_key_set,omitempty"`
	PathStyle  bool   `json:"path_style,omitempty"`
	Keep       int    `json:"keep,omitempty"`
	// PublicOnly: this org's destination must be a public HTTPS endpoint.
	PublicOnly bool       `json:"public_only"`
	Since      *time.Time `json:"since,omitempty"`
	LastSync   *time.Time `json:"last_sync,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	Pending    int        `json:"pending"`  // snapshots not copied yet
	Uploaded   int        `json:"uploaded"` // local snapshots with an off-site copy
	// Interval is how often every database is snapshotted (0: never).
	Interval string `json:"interval,omitempty"`
}

type backupState struct {
	mu       sync.Mutex // one sync at a time
	statusMu sync.Mutex
	status   map[string]syncStatus // by org
	kick     chan struct{}
	once     sync.Once
}

type syncStatus struct {
	at  time.Time
	err string
}

func (e *Engine) backups() *backupState {
	e.bk.once.Do(func() {
		e.bk.status = map[string]syncStatus{}
		e.bk.kick = make(chan struct{}, 1)
	})
	return &e.bk
}

func backupKey(org string) string { return "backup:" + org }

// restricted orgs may only reach public HTTPS endpoints: their admins don't
// run this server, and must not use it to reach its network.
func restricted(org string) bool { return store.OrgOr(org) != store.DefaultOrg }

func (e *Engine) remote(cfg offsite.Config, org string) (offsite.Remote, error) {
	if e.NewRemote != nil {
		return e.NewRemote(cfg, restricted(org))
	}
	return offsite.New(cfg, restricted(org))
}

func (e *Engine) backupConfig(ctx context.Context, org string) (*offsite.Config, *time.Time, error) {
	in, err := e.st.GetIntegration(ctx, backupKey(org))
	if err != nil || in == nil {
		return nil, nil, err
	}
	var cfg offsite.Config
	if err := json.Unmarshal([]byte(in.Secret), &cfg); err != nil {
		return nil, nil, err
	}
	return &cfg, &in.CreatedAt, nil
}

func (e *Engine) BackupStatus(ctx context.Context) (*BackupView, error) {
	org := store.OrgOr(store.OrgFrom(ctx))
	v := &BackupView{PublicOnly: restricted(org)}
	if e.SnapshotInterval > 0 {
		v.Interval = e.SnapshotInterval.String()
	}
	cfg, since, err := e.backupConfig(ctx, org)
	if err != nil || cfg == nil {
		return v, err
	}
	v.Configured, v.Endpoint, v.Region, v.Bucket, v.Prefix = true, cfg.Endpoint, cfg.Region, cfg.Bucket, cfg.Prefix
	v.AccessKey, v.SecretSet, v.PathStyle, v.Keep, v.Since = cfg.AccessKey, cfg.SecretKey != "", cfg.PathStyle, cfg.Keep, since
	b := e.backups()
	b.statusMu.Lock()
	if st, ok := b.status[org]; ok {
		at := st.at
		v.LastSync, v.LastError = &at, st.err
	}
	b.statusMu.Unlock()
	snaps, err := e.st.AllSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	orgs := e.ownerOrgs()
	for _, sn := range snaps {
		if orgs.of(ctx, sn) != org {
			continue
		}
		if sn.RemoteKey != "" {
			v.Uploaded++
		} else {
			v.Pending++
		}
	}
	return v, nil
}

// prepare fills a blank secret key from the saved config and checks cfg.
func (e *Engine) prepareBackup(ctx context.Context, org string, cfg *offsite.Config) error {
	if strings.TrimSpace(cfg.SecretKey) == "" {
		if old, _, err := e.backupConfig(ctx, org); err != nil {
			return err
		} else if old != nil {
			cfg.SecretKey = old.SecretKey
		}
	}
	if err := cfg.Normalize(); err != nil {
		return apierr.New(400, "invalid_request", "%v", err)
	}
	return nil
}

func (e *Engine) checkBackup(ctx context.Context, org string, cfg offsite.Config) error {
	r, err := e.remote(cfg, org)
	if err == nil {
		err = r.Check(ctx)
	}
	if err != nil {
		return apierr.New(400, "backup_unreachable", "can't write to the bucket: %v", err).
			WithHint("check the endpoint, bucket, keys and region; the keys need to list, read, write and delete objects")
	}
	return nil
}

// TestBackup checks a destination without saving it.
func (e *Engine) TestBackup(ctx context.Context, cfg offsite.Config) error {
	org := store.OrgOr(store.OrgFrom(ctx))
	if err := e.prepareBackup(ctx, org, &cfg); err != nil {
		return err
	}
	return e.checkBackup(ctx, org, cfg)
}

// SaveBackup checks the destination, saves it, and starts copying.
func (e *Engine) SaveBackup(ctx context.Context, actor string, cfg offsite.Config) (*BackupView, error) {
	org := store.OrgOr(store.OrgFrom(ctx))
	if err := e.prepareBackup(ctx, org, &cfg); err != nil {
		return nil, err
	}
	if err := e.checkBackup(ctx, org, cfg); err != nil {
		return nil, err
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	bk := e.backups()
	bk.mu.Lock() // no sync in between: it would copy to the old bucket
	old, _, err := e.backupConfig(ctx, org)
	if err == nil {
		err = e.st.SaveIntegration(ctx, &store.Integration{Name: backupKey(org), Secret: string(b),
			Account: cfg.Bucket, CreatedAt: e.Now()})
	}
	if err == nil && (old == nil || old.Endpoint != cfg.Endpoint || old.Bucket != cfg.Bucket || old.Prefix != cfg.Prefix) {
		err = e.forgetCopies(ctx, org) // a new place: copy everything there
	}
	bk.mu.Unlock()
	if err != nil {
		return nil, err
	}
	bk.statusMu.Lock()
	delete(bk.status, org)
	bk.statusMu.Unlock()
	e.audit(ctx, actor, "backup.configure", cfg.Bucket, "", map[string]any{"endpoint": cfg.Endpoint, "prefix": cfg.Prefix, "keep": cfg.Keep})
	e.KickBackups()
	return e.BackupStatus(ctx)
}

// DeleteBackup stops off-site copies. What's in the bucket stays.
func (e *Engine) DeleteBackup(ctx context.Context, actor string) error {
	org := store.OrgOr(store.OrgFrom(ctx))
	bk := e.backups()
	bk.mu.Lock()
	defer bk.mu.Unlock()
	if err := e.st.DeleteIntegration(ctx, backupKey(org)); err != nil {
		return err
	}
	if err := e.forgetCopies(ctx, org); err != nil {
		return err
	}
	bk.statusMu.Lock()
	delete(bk.status, org)
	bk.statusMu.Unlock()
	e.audit(ctx, actor, "backup.disconnect", "backup", "", nil)
	return nil
}

// forgetCopies marks org's snapshots as not copied, for a new bucket.
func (e *Engine) forgetCopies(ctx context.Context, org string) error {
	snaps, err := e.st.AllSnapshots(ctx)
	if err != nil {
		return err
	}
	orgs := e.ownerOrgs()
	for i := range snaps {
		sn := &snaps[i]
		if sn.RemoteKey == "" && sn.RemoteError == "" || orgs.of(ctx, *sn) != org {
			continue
		}
		sn.RemoteKey, sn.RemoteAt, sn.RemoteError = "", nil, ""
		if err := e.st.UpdateSnapshot(ctx, sn); err != nil {
			return err
		}
	}
	return nil
}

// KickBackups starts a sync soon.
func (e *Engine) KickBackups() {
	select {
	case e.backups().kick <- struct{}{}:
	default:
	}
}

// RunBackups copies new snapshots off-site when kicked, and every few
// minutes to retry failures.
func (e *Engine) RunBackups(ctx context.Context, log *slog.Logger) {
	b := e.backups()
	e.KickBackups() // catch up after a restart
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.kick:
			// Let a burst of snapshots (a scheduled run) finish first.
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		case <-t.C:
		}
		if n, err := e.SyncBackups(ctx, ""); err != nil {
			log.Error("off-site backups", "err", err, "copied", n)
		} else if n > 0 {
			log.Info("off-site backups", "copied", n)
		}
	}
}

// orgCache finds which org a snapshot's database belongs to now: its app's
// or data service's, or, once that's gone, the one it had when taken.
type orgCache struct {
	e *Engine
	m map[string]string
}

func (e *Engine) ownerOrgs() *orgCache { return &orgCache{e: e, m: map[string]string{}} }

func (c *orgCache) of(ctx context.Context, sn store.Snapshot) string {
	k := sn.App + "\x00" + sn.Database
	if org, ok := c.m[k]; ok {
		return org
	}
	org := ""
	if sn.App != "" {
		if a, err := c.e.st.GetApp(ctx, sn.App); err == nil && a != nil {
			org = a.Org
		}
	} else if d, err := c.e.st.GetDatabase(ctx, "", sn.Database); err == nil && d != nil {
		org = d.Org
	}
	if org == "" {
		org = sn.Org
	}
	org = store.OrgOr(org)
	c.m[k] = org
	return org
}

// remoteDir is where a database's backups go, under the prefix.
func remoteDir(app, database string) string {
	if app == "" {
		return path.Join("data", database)
	}
	return path.Join("apps", app, database)
}

// SyncBackups copies every snapshot without an off-site copy to its org's
// bucket (only org's, when given), then prunes old copies of the databases
// it copied. It returns how many it copied.
func (e *Engine) SyncBackups(ctx context.Context, only string) (int, error) {
	b := e.backups()
	b.mu.Lock()
	defer b.mu.Unlock()
	snaps, err := e.st.AllSnapshots(ctx)
	if err != nil {
		return 0, err
	}
	orgs := e.ownerOrgs()
	byOrg := map[string][]store.Snapshot{}
	for _, sn := range snaps {
		if sn.RemoteKey == "" {
			org := orgs.of(ctx, sn)
			byOrg[org] = append(byOrg[org], sn)
		}
	}
	names := make([]string, 0, len(byOrg))
	for org := range byOrg {
		names = append(names, org)
	}
	sort.Strings(names)
	copied := 0
	var errs []error
	for _, org := range names {
		if only != "" && org != only {
			continue
		}
		cfg, _, err := e.backupConfig(ctx, org)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if cfg == nil {
			continue
		}
		n, err := e.syncOrg(ctx, org, *cfg, byOrg[org])
		copied += n
		st := syncStatus{at: e.Now()}
		if err != nil {
			st.err = err.Error()
			errs = append(errs, fmt.Errorf("org %s: %w", org, err))
		}
		b.statusMu.Lock()
		prev, had := b.status[org]
		b.status[org] = st
		b.statusMu.Unlock()
		switch {
		case st.err != "" && (!had || prev.err == ""):
			e.emit(ctx, Event{Kind: EvBackupFailing, Org: org, Severity: "error", Title: "Off-site backups are failing",
				Detail: clipText(st.err, 1000) + "\nSnapshots stay on the server and are retried every 10 minutes."})
		case st.err == "" && had && prev.err != "":
			e.emit(ctx, Event{Kind: EvBackupRecovered, Org: org, Severity: "ok", Title: "Off-site backups work again"})
		}
	}
	return copied, errors.Join(errs...)
}

func (e *Engine) syncOrg(ctx context.Context, org string, cfg offsite.Config, snaps []store.Snapshot) (int, error) {
	r, err := e.remote(cfg, org)
	if err != nil {
		return 0, err
	}
	copied := 0
	touched := map[string]bool{}
	for i := range snaps {
		sn := &snaps[i]
		key, err := e.upload(ctx, r, cfg, sn)
		if errors.Is(err, os.ErrNotExist) {
			continue // pruned meanwhile
		}
		if err != nil {
			// The bucket is likely down; try again next time.
			sn.RemoteError = err.Error()
			e.st.UpdateSnapshot(ctx, sn)
			return copied, err
		}
		now := e.Now()
		sn.RemoteKey, sn.RemoteAt, sn.RemoteError = key, &now, ""
		if err := e.st.UpdateSnapshot(ctx, sn); err != nil {
			return copied, err
		}
		copied++
		touched[remoteDir(sn.App, sn.Database)] = true
	}
	var errs []error
	for dir := range touched {
		if err := e.pruneRemote(ctx, r, cfg, dir); err != nil {
			errs = append(errs, err)
		}
	}
	return copied, errors.Join(errs...)
}

func (e *Engine) upload(ctx context.Context, r offsite.Remote, cfg offsite.Config, sn *store.Snapshot) (string, error) {
	f, err := os.Open(sn.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	key := cfg.Key(remoteDir(sn.App, sn.Database), sn.CreatedAt.UTC().Format(remoteTime)+"-"+sn.ID+runtime.SnapshotExt(sn.Engine))
	if err := r.Put(ctx, key, f, fi.Size()); err != nil {
		return "", err
	}
	m, err := json.MarshalIndent(Manifest{Format: 1, Encryption: "none", Snapshot: sn.ID, App: sn.App, Database: sn.Database,
		Engine: sn.Engine, Version: sn.Version, Size: sn.Size, SHA256: sn.SHA256, Reason: sn.Reason, CreatedAt: sn.CreatedAt}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := r.Put(ctx, key+".json", strings.NewReader(string(m)), int64(len(m))); err != nil {
		return "", err
	}
	return key, nil
}

// remoteTime names backups so they sort by time, to the nanosecond.
const remoteTime = "20060102T150405.000000000Z"

// backupFile matches the dumps Coldship uploads: <time>-<snapshot id><ext>.
var backupFile = regexp.MustCompile(`^\d{8}T\d{6}\.\d{9}Z-snap_[0-9a-f]+\.[a-z0-9.]+$`)

// complete lists a directory's finished backups (dump keys), oldest first.
func complete(keys []string) []string {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	var out []string
	for _, k := range keys {
		dump, ok := strings.CutSuffix(k, ".json")
		if ok && set[dump] && backupFile.MatchString(path.Base(dump)) {
			out = append(out, dump)
		}
	}
	sort.Slice(out, func(i, j int) bool { return path.Base(out[i]) < path.Base(out[j]) })
	return out
}

// pruneRemote keeps the newest cfg.Keep backups in dir.
func (e *Engine) pruneRemote(ctx context.Context, r offsite.Remote, cfg offsite.Config, dir string) error {
	prefix := cfg.Key(dir) + "/"
	keys, err := r.List(ctx, prefix)
	if err != nil {
		return err // never prune on a failed listing
	}
	var direct []string
	for _, k := range keys {
		if !strings.Contains(strings.TrimPrefix(k, prefix), "/") {
			direct = append(direct, k)
		}
	}
	done := complete(direct)
	for i := 0; i < len(done)-cfg.Keep; i++ {
		// Manifest first: without it the dump is no longer a backup.
		if err := r.Delete(ctx, done[i]+".json"); err != nil {
			return err
		}
		if err := r.Delete(ctx, done[i]); err != nil {
			return err
		}
	}
	return nil
}

// Backups lists the off-site backups of one database (app "" for a data
// service), or of every database when database is "", newest first.
func (e *Engine) Backups(ctx context.Context, app, database string) ([]Backup, error) {
	org := store.OrgOr(store.OrgFrom(ctx))
	cfg, _, err := e.backupConfig(ctx, org)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, notConfigured()
	}
	r, err := e.remote(*cfg, org)
	if err != nil {
		return nil, err
	}
	prefix := cfg.Key() + "/"
	if cfg.Prefix == "" {
		prefix = ""
	}
	if database != "" {
		prefix = cfg.Key(remoteDir(app, database)) + "/"
	}
	keys, err := r.List(ctx, prefix)
	if err != nil {
		return nil, apierr.New(502, "backup_unreachable", "can't list the bucket: %v", err)
	}
	done := complete(keys)
	sort.SliceStable(done, func(i, j int) bool { return path.Base(done[i]) > path.Base(done[j]) })
	const max = 200
	if len(done) > max {
		done = done[:max]
	}
	out := []Backup{}
	for _, k := range done {
		m, err := readManifest(ctx, r, *cfg, k)
		if err != nil {
			continue // unreadable: not offered
		}
		if database != "" && (m.App != app || m.Database != database) {
			continue
		}
		out = append(out, Backup{Key: k, Manifest: *m})
	}
	return out, nil
}

func notConfigured() error {
	return apierr.New(409, "backup_not_configured", "off-site backups aren't set up").
		WithHint("an admin sets a bucket in the dashboard (Data & Storage → Backups)")
}

// manifests caches manifests, which never change once written, by
// destination and key.
var manifests sync.Map

func readManifest(ctx context.Context, r offsite.Remote, cfg offsite.Config, key string) (*Manifest, error) {
	ck := cfg.Endpoint + "\x00" + cfg.Bucket + "\x00" + cfg.AccessKey + "\x00" + key
	if m, ok := manifests.Load(ck); ok {
		return m.(*Manifest), nil
	}
	m, err := fetchManifest(ctx, r, key)
	if err == nil {
		manifests.Store(ck, m)
	}
	return m, err
}

func fetchManifest(ctx context.Context, r offsite.Remote, key string) (*Manifest, error) {
	rc, err := r.Get(ctx, key+".json")
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(rc, 64<<10)).Decode(&m); err != nil {
		return nil, err
	}
	if m.Format != 1 || m.Encryption != "none" {
		return nil, fmt.Errorf("backup %s has format %d, encryption %q; this version can't read it", key, m.Format, m.Encryption)
	}
	return &m, nil
}

// major is a version's leading number ("16" in "16-alpine"), or -1.
func major(v string) int {
	end := 0
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(v[:end])
	if err != nil {
		return -1
	}
	return n
}

// importBackup downloads an off-site backup into a local snapshot of the
// target database (app "" for a data service), verified against its
// manifest. It refuses another engine, or a newer major version.
func (e *Engine) importBackup(ctx context.Context, actor, key, app string, target spec.Database) (*store.Snapshot, *Manifest, error) {
	org := store.OrgOr(store.OrgFrom(ctx))
	cfg, _, err := e.backupConfig(ctx, org)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, notConfigured()
	}
	key = strings.TrimSpace(key)
	if cfg.Prefix != "" && !strings.HasPrefix(key, cfg.Prefix+"/") || strings.Contains(key, "..") || !backupFile.MatchString(path.Base(key)) {
		return nil, nil, apierr.New(400, "invalid_request", "%q is not a backup in this org's bucket", key).WithField("backup")
	}
	r, err := e.remote(*cfg, org)
	if err != nil {
		return nil, nil, err
	}
	m, err := readManifest(ctx, r, *cfg, key)
	if errors.Is(err, offsite.ErrNotFound) {
		return nil, nil, apierr.New(404, "backup_not_found", "no backup %s in the bucket", key)
	}
	if err != nil {
		return nil, nil, apierr.New(502, "backup_unreachable", "can't read the backup: %v", err)
	}
	if m.Engine != target.Engine {
		return nil, nil, apierr.New(409, "engine_mismatch", "%s is a %s backup; %s is %s", key, m.Engine, target.Name, target.Engine)
	}
	if bm, tm := major(m.Version), major(target.Version); bm > 0 && tm > 0 && bm > tm {
		return nil, nil, apierr.New(409, "version_too_old", "the backup is from %s %s; %s runs %s, which can't load it",
			m.Engine, m.Version, target.Name, target.Version).
			WithHint("create a data service with version " + m.Version + " or newer and restore into that")
	}
	rc, err := r.Get(ctx, key)
	if err != nil {
		return nil, nil, apierr.New(502, "backup_unreachable", "can't download the backup: %v", err)
	}
	defer rc.Close()
	owner := app
	if owner == "" {
		owner = "_data"
	}
	dir := filepath.Join(e.SnapshotDir, owner, target.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	tmp, err := os.CreateTemp(dir, ".import-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), rc)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, nil, apierr.New(502, "backup_unreachable", "downloading the backup failed: %v", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != m.SHA256 || n != m.Size {
		return nil, nil, apierr.New(502, "backup_corrupt", "%s does not match its manifest's checksum; refusing it", key)
	}
	sn := &store.Snapshot{ID: store.NewID("snap"), App: app, Database: target.Name, Engine: m.Engine, Version: m.Version,
		Size: n, SHA256: m.SHA256, Reason: "copied from off-site backup " + m.CreatedAt.UTC().Format(time.RFC3339),
		Actor: actor, CreatedAt: e.Now(), Manual: true, Org: org, RemoteKey: key}
	now := e.Now()
	sn.RemoteAt = &now
	sn.Path = filepath.Join(dir, sn.CreatedAt.UTC().Format("20060102T150405Z")+"-"+sn.ID+runtime.SnapshotExt(m.Engine))
	if err := os.Rename(tmp.Name(), sn.Path); err != nil {
		return nil, nil, err
	}
	if err := e.st.SaveSnapshot(ctx, sn); err != nil {
		os.Remove(sn.Path)
		return nil, nil, err
	}
	e.audit(ctx, actor, "backup.import", owner, "", map[string]any{"database": target.Name, "backup": key, "snapshot": sn.ID})
	e.pruneSnapshots(ctx, app, target.Name, true)
	return sn, m, nil
}

// RestoreDataBackup replaces a data service's contents with an off-site
// backup, saving the current data as a snapshot first.
func (e *Engine) RestoreDataBackup(ctx context.Context, actor, name, key, reason string) (string, error) {
	defer e.lockApp("data:" + name)()
	db, err := e.serviceDB(ctx, name)
	if err != nil {
		return "", err
	}
	target := spec.Database{Name: name, Engine: db.Engine, Version: db.Version}
	ctx = context.WithoutCancel(ctx)
	sn, m, err := e.importBackup(ctx, actor, key, "", target)
	if err != nil {
		return "", err
	}
	return e.restoreFrom(ctx, actor, "", target, sn, reason, "the off-site backup of "+m.CreatedAt.UTC().Format(time.RFC3339)+" (now snapshot "+sn.ID+")")
}

// ImportAppBackup copies an off-site backup into a local snapshot of an
// app's database, to restore with a plan.
func (e *Engine) ImportAppBackup(ctx context.Context, actor, app, database, key string) (*store.Snapshot, error) {
	a, err := e.st.GetApp(ctx, app)
	if err != nil {
		return nil, err
	}
	if a == nil || a.Spec == nil || store.OrgOr(a.Org) != store.OrgOr(store.OrgFrom(ctx)) {
		return nil, apierr.NotFound("app", app)
	}
	for _, d := range a.Spec.Databases {
		if d.Name == database {
			sn, _, err := e.importBackup(context.WithoutCancel(ctx), actor, key, app, d)
			return sn, err
		}
	}
	return nil, apierr.NotFound("database", database)
}
