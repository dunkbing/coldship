package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/spec"
)

// DB identifies a managed database or data service and carries its
// generated password.
type DB struct {
	App      string // owning app; "" for a standalone data service
	Name     string
	Engine   string // postgres | mysql | redis | s3
	Version  string
	Password string
}

type DBStatus struct {
	Exists  bool   `json:"exists"`
	Running bool   `json:"running"`
	State   string `json:"state,omitempty"`
	Image   string `json:"image,omitempty"`
}

// DBHost is an app database's hostname on the Coldship network.
func DBHost(app, name string) string { return app + "-db-" + name }

// ServiceHost is a standalone data service's hostname (and container name).
func ServiceHost(name string) string { return "coldship-data-" + name }

func dbContainer(app, name string) string { return "coldship-" + DBHost(app, name) }
func dbVolume(app, name string) string    { return "coldship-" + DBHost(app, name) + "-data" }

// Host is where apps reach the database.
func (d DB) Host() string {
	if d.App == "" {
		return ServiceHost(d.Name)
	}
	return DBHost(d.App, d.Name)
}

func (d DB) container() string {
	if d.App == "" {
		return ServiceHost(d.Name)
	}
	return dbContainer(d.App, d.Name)
}

func (d DB) volume() string {
	if d.App == "" {
		return ServiceHost(d.Name)
	}
	return dbVolume(d.App, d.Name)
}

func (d DB) labels() map[string]string {
	if d.App == "" {
		return map[string]string{"coldship.db": d.Name, "coldship.data": d.Name}
	}
	return map[string]string{"coldship.app": d.App, "coldship.db": d.Name}
}

// engineDef is everything engine-specific about running a database.
type engineDef struct {
	image   func(version string) string
	dataDir string
	port    int
	// env and cmd configure the container; secrets go in env, which is sent
	// in the API request, never on a command line.
	env        func(d DB) []string
	entrypoint []string
	cmd        func(d DB) []string
	// ready and snapshot are commands run inside the container, with execEnv.
	ready    []string
	execEnv  func(d DB) []string
	snapshot []string
	restore  []string
	ext      string
}

// S3AccessKey is the access key id of an S3 data service; the password is
// its secret key.
func S3AccessKey(name string) string { return "coldship-" + name }

var engines = map[string]engineDef{
	spec.EnginePostgres: {
		image:   func(v string) string { return "postgres:" + v + "-alpine" },
		dataDir: "/var/lib/postgresql/data", port: 5432,
		env: func(d DB) []string {
			return []string{"POSTGRES_USER=app", "POSTGRES_DB=app", "POSTGRES_PASSWORD=" + d.Password}
		},
		// Over TCP: during first-time setup postgres only listens on its
		// socket, and restarts before it is really ready.
		ready:    []string{"pg_isready", "-h", "127.0.0.1", "-U", "app", "-d", "app"},
		snapshot: []string{"pg_dump", "-U", "app", "-d", "app", "-Fc"},
		restore:  []string{"pg_restore", "-U", "app", "-d", "app", "--clean", "--if-exists", "--no-owner", "--single-transaction"},
		ext:      ".dump",
	},
	spec.EngineMySQL: {
		image:   func(v string) string { return "mysql:" + v },
		dataDir: "/var/lib/mysql", port: 3306,
		env: func(d DB) []string {
			return []string{"MYSQL_DATABASE=app", "MYSQL_USER=app", "MYSQL_PASSWORD=" + d.Password, "MYSQL_ROOT_PASSWORD=" + d.Password}
		},
		// Over TCP, for the same reason as postgres: the setup server
		// doesn't listen on the network.
		ready:    []string{"mysqladmin", "ping", "-h", "127.0.0.1", "-u", "app", "--silent"},
		execEnv:  func(d DB) []string { return []string{"MYSQL_PWD=" + d.Password} },
		snapshot: []string{"mysqldump", "-h", "127.0.0.1", "-u", "app", "--single-transaction", "--routines", "--triggers", "--no-tablespaces", "app"},
		restore:  []string{"mysql", "-h", "127.0.0.1", "-u", "app", "app"},
		ext:      ".sql",
	},
	spec.EngineRedis: {
		image:   func(v string) string { return "redis:" + v + "-alpine" },
		dataDir: "/data", port: 6379,
		env: func(d DB) []string { return []string{"REDIS_PASSWORD=" + d.Password} },
		cmd: func(DB) []string {
			return []string{"sh", "-c", `exec redis-server --requirepass "$REDIS_PASSWORD" --save 60 1 --appendonly no`}
		},
		ready:   []string{"redis-cli", "ping"},
		execEnv: func(d DB) []string { return []string{"REDISCLI_AUTH=" + d.Password} },
		ext:     ".rdb",
	},
	spec.EngineS3: {
		// SeaweedFS: one container serving the S3 API on port 8333.
		image:   func(v string) string { return "chrislusf/seaweedfs:" + v },
		dataDir: "/data", port: 8333,
		env: func(d DB) []string {
			return []string{fmt.Sprintf(`S3_CONFIG={"identities":[{"name":"coldship","credentials":[{"accessKey":%q,"secretKey":%q}],"actions":["Admin","Read","Write","List","Tagging"]}]}`,
				S3AccessKey(d.Name), d.Password)}
		},
		entrypoint: []string{"sh", "-c"},
		cmd: func(DB) []string {
			return []string{`printf '%s' "$S3_CONFIG" > /tmp/s3.json && exec weed server -dir=/data -s3 -s3.port=8333 -s3.config=/tmp/s3.json -master.volumeSizeLimitMB=1024`}
		},
		// Any HTTP answer (403 without credentials) means the S3 API is up.
		ready: []string{"sh", "-c", "wget -q -S -O /dev/null http://127.0.0.1:8333/ 2>&1 | grep -q HTTP/"},
	},
}

func engineOf(d DB) (engineDef, error) {
	e, ok := engines[d.Engine]
	if !ok {
		return engineDef{}, apierr.New(400, "invalid_request", "unknown database engine %q", d.Engine)
	}
	return e, nil
}

// Image is the container image for an engine and version.
func Image(engine, version string) string {
	if e, ok := engines[engine]; ok {
		return e.image(version)
	}
	return engine + ":" + version
}

// URL is the connection URL for engines that have one (not s3).
func (d DB) URL() string {
	host, pw := d.Host(), url.QueryEscape(d.Password)
	switch d.Engine {
	case spec.EngineRedis:
		// Name the "default" user: with an empty username some clients
		// (redis-cli 7 among them) authenticate as user "" and fail.
		return fmt.Sprintf("redis://default:%s@%s:6379/0", pw, host)
	case spec.EngineMySQL:
		return fmt.Sprintf("mysql://app:%s@%s:3306/app", pw, host)
	case spec.EngineS3:
		return fmt.Sprintf("http://%s:8333", host)
	default:
		return fmt.Sprintf("postgres://app:%s@%s:5432/app?sslmode=disable", pw, host)
	}
}

// S3Env is what an app gets from an S3 data service: the AWS SDKs' standard
// variables (which they read on their own) and plain S3_* ones. The bucket
// has the service's name.
func (d DB) S3Env() map[string]string {
	endpoint := d.URL()
	return map[string]string{
		"AWS_ACCESS_KEY_ID": S3AccessKey(d.Name), "AWS_SECRET_ACCESS_KEY": d.Password,
		"AWS_ENDPOINT_URL": endpoint, "AWS_REGION": "us-east-1",
		"S3_ENDPOINT": endpoint, "S3_BUCKET": d.Name, "S3_REGION": "us-east-1",
		"S3_ACCESS_KEY_ID": S3AccessKey(d.Name), "S3_SECRET_ACCESS_KEY": d.Password,
		"S3_FORCE_PATH_STYLE": "true",
	}
}

// SnapshotExt is the file extension for the engine's snapshot format.
func SnapshotExt(engine string) string {
	if e, ok := engines[engine]; ok && e.ext != "" {
		return e.ext
	}
	return ".dump"
}

func (d *Docker) EnsureDatabase(ctx context.Context, db DB, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	eng, err := engineOf(db)
	if err != nil {
		return err
	}
	if err := d.EnsureNetwork(ctx); err != nil {
		return err
	}
	name, image := db.container(), eng.image(db.Version)
	st, err := d.dbStatus(ctx, db)
	if err != nil {
		return err
	}
	if st.Exists {
		if err := d.checkOwner(ctx, name, db.App, db.Name); err != nil {
			return err
		}
	}
	switch {
	case st.Exists && normalizeImage(st.Image) != normalizeImage(image):
		return apierr.New(409, "database_version_change", "database %s runs %s, not %s", db.Name, st.Image, image).
			WithField("databases").WithHint("changing a database's engine or major version needs a migration, which Coldship doesn't do yet; keep the current version")
	case st.Exists && !st.Running:
		fmt.Fprintf(log, "coldship: starting database %s\n", db.Name)
		if err := d.startContainer(ctx, name); err != nil {
			return apierr.New(502, "database_start_failed", "could not start database %s: %v", db.Name, err)
		}
	case !st.Exists:
		if err := d.EnsureImage(ctx, image, log); err != nil {
			return apierr.New(502, "database_start_failed", "could not pull %s: %v", image, err)
		}
		fmt.Fprintf(log, "coldship: creating database %s (%s)\n", db.Name, image)
		cs := ContainerSpec{Name: name, Image: image, Labels: db.labels(), Env: eng.env(db), Entrypoint: eng.entrypoint,
			Binds: []string{db.volume() + ":" + eng.dataDir}}
		if db.App != "" {
			cs.Aliases = []string{db.Host()}
		}
		if eng.cmd != nil {
			cs.Cmd = eng.cmd(db)
		}
		if se, err := d.RunContainer(ctx, cs); err != nil {
			return apierr.New(502, "database_start_failed", "could not create database %s: %s", db.Name, se)
		}
	}
	if err := d.waitDBReady(ctx, db, log); err != nil {
		return err
	}
	if db.Engine == spec.EngineS3 {
		return d.ensureBucket(ctx, db)
	}
	return nil
}

// ensureBucket creates the S3 service's bucket, named after it. It exists
// already on every start but the first, which is fine.
func (d *Docker) ensureBucket(ctx context.Context, db DB) error {
	cmd := fmt.Sprintf("echo 's3.bucket.create -name %s' | weed shell -master=127.0.0.1:9333", db.Name)
	var lastErr string
	for i := 0; i < 30; i++ {
		var out bytes.Buffer
		code, se, err := d.Exec(ctx, db.container(), []string{"sh", "-c", cmd}, ExecIO{Stdout: &out})
		text := out.String() + se
		if err == nil && code == 0 && !strings.Contains(strings.ToLower(text), "error") || strings.Contains(text, "already exist") {
			return nil
		}
		lastErr = errText(text, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return apierr.New(502, "database_not_ready", "could not create bucket %s: %s", db.Name, lastLine(lastErr))
}

func (d *Docker) waitDBReady(ctx context.Context, db DB, log io.Writer) error {
	deadline := time.Now().Add(120 * time.Second)
	for {
		if d.dbReady(ctx, db) {
			return nil
		}
		if time.Now().After(deadline) {
			out, _ := d.tailLogs(context.WithoutCancel(ctx), db.container(), 30, "")
			fmt.Fprintf(log, "coldship: database %s did not become ready; its last output:\n%s", db.Name, out)
			return apierr.New(502, "database_not_ready", "database %s did not accept connections within 120s", db.Name).
				WithHint("read the deploy log for the database's output")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func execEnv(eng engineDef, db DB) []string {
	if eng.execEnv == nil {
		return nil
	}
	return eng.execEnv(db)
}

func (d *Docker) dbReady(ctx context.Context, db DB) bool {
	eng, err := engineOf(db)
	if err != nil {
		return false
	}
	var out bytes.Buffer
	code, _, err := d.Exec(ctx, db.container(), eng.ready, ExecIO{Env: execEnv(eng, db), Stdout: &out})
	if err != nil || code != 0 {
		return false
	}
	return db.Engine != spec.EngineRedis || strings.Contains(out.String(), "PONG")
}

func (d *Docker) DatabaseStatus(ctx context.Context, app, name string) (DBStatus, error) {
	return d.dbStatus(ctx, DB{App: app, Name: name})
}

func (d *Docker) dbStatus(ctx context.Context, db DB) (DBStatus, error) {
	info, err := d.InspectContainer(ctx, db.container())
	if err != nil || !info.Exists {
		return DBStatus{}, err
	}
	return DBStatus{Exists: true, Running: info.State == "running", State: info.State, Image: info.Image}, nil
}

func (d *Docker) RemoveDatabase(ctx context.Context, app, name string) error {
	db := DB{App: app, Name: name}
	if err := d.checkOwner(ctx, db.container(), app, name); err != nil {
		return err
	}
	if err := d.ForceRemove(ctx, db.container()); err != nil {
		return apierr.New(502, "database_remove_failed", "could not remove database %s: %v", name, err)
	}
	if err := d.removeVolume(ctx, db.volume()); err != nil {
		return apierr.New(502, "database_remove_failed", "could not remove database volume %s: %v", name, err)
	}
	return nil
}

// SnapshotDatabase writes a consistent snapshot to w: a pg_dump custom
// archive for postgres, a SQL dump for mysql, an RDB file for redis.
func (d *Docker) SnapshotDatabase(ctx context.Context, db DB, w io.Writer) error {
	eng, err := engineOf(db)
	if err != nil {
		return err
	}
	c := db.container()
	cmd := eng.snapshot
	switch db.Engine {
	case spec.EngineRedis:
		code, se, err := d.Exec(ctx, c, []string{"redis-cli", "SAVE"}, ExecIO{Env: execEnv(eng, db)})
		if err != nil || code != 0 {
			return apierr.New(502, "snapshot_failed", "redis SAVE failed for %s: %s", db.Name, errText(se, err))
		}
		cmd = []string{"cat", "/data/dump.rdb"}
	case spec.EngineS3:
		return apierr.New(400, "snapshot_unsupported", "snapshots of S3 storage aren't supported yet").
			WithHint("back up the bucket with an S3 tool, such as rclone or aws s3 sync")
	}
	code, se, err := d.Exec(ctx, c, cmd, ExecIO{Env: execEnv(eng, db), Stdout: w})
	if err != nil || code != 0 {
		return apierr.New(502, "snapshot_failed", "snapshot of database %s failed: %s", db.Name, errText(se, err))
	}
	return nil
}

// RestoreDatabase replaces the database's contents with a snapshot.
func (d *Docker) RestoreDatabase(ctx context.Context, db DB, r io.Reader, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	eng, err := engineOf(db)
	if err != nil {
		return err
	}
	c := db.container()
	switch db.Engine {
	case spec.EngineRedis:
		// Redis loads dump.rdb only at startup: stop it, copy the file into
		// its volume, start it again.
		fmt.Fprintf(log, "coldship: stopping redis %s to restore\n", db.Name)
		if err := d.stopContainer(ctx, c, 10*time.Second); err != nil {
			return apierr.New(502, "restore_failed", "could not stop %s: %v", db.Name, err)
		}
		copyErr := d.copyFile(ctx, c, "/data", "dump.rdb", r)
		if err := d.startContainer(ctx, c); err != nil {
			return apierr.New(502, "restore_failed", "could not start %s after restoring: %v", db.Name, err)
		}
		if copyErr != nil {
			return apierr.New(502, "restore_failed", "writing the snapshot failed: %v", copyErr)
		}
	case spec.EngineS3:
		return apierr.New(400, "snapshot_unsupported", "snapshots of S3 storage aren't supported yet")
	default:
		code, se, err := d.Exec(ctx, c, eng.restore, ExecIO{Env: execEnv(eng, db), Stdin: r, Stdout: log})
		if err != nil || code != 0 {
			fmt.Fprintln(log, se)
			return apierr.New(502, "restore_failed", "restoring %s failed: %s", db.Name, lastLine(errText(se, err)))
		}
	}
	return d.waitDBReady(ctx, db, log)
}

// copyFile writes r as dir/name inside a container (running or stopped,
// including into its volumes). It spools r to a temp file first, because a
// tar header needs the size.
func (d *Docker) copyFile(ctx context.Context, ctr, dir, name string, r io.Reader) error {
	cli, err := d.api()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "coldship-copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	size, err := io.Copy(tmp, r)
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: size, ModTime: time.Now()})
		if err == nil {
			_, err = io.Copy(tw, tmp)
		}
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()
	_, err = cli.CopyToContainer(ctx, ctr, client.CopyToContainerOptions{DestinationPath: dir, Content: pr})
	pr.Close()
	return err
}

func errText(stderr string, err error) string {
	if s := strings.TrimSpace(stderr); s != "" {
		return s
	}
	if err != nil {
		return err.Error()
	}
	return "exited with an error"
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// normalizeImage makes Docker Hub references comparable: Podman reports
// docker.io/library/postgres:17-alpine where Docker says postgres:17-alpine.
func normalizeImage(ref string) string {
	for _, p := range []string{"docker.io/library/", "index.docker.io/library/", "docker.io/", "index.docker.io/"} {
		if strings.HasPrefix(ref, p) {
			return strings.TrimPrefix(ref, p)
		}
	}
	return ref
}
