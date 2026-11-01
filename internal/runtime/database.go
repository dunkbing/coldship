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

// DB identifies a managed database and carries its generated password.
type DB struct {
	App      string
	Name     string
	Engine   string
	Version  string
	Password string
}

type DBStatus struct {
	Exists  bool   `json:"exists"`
	Running bool   `json:"running"`
	State   string `json:"state,omitempty"`
	Image   string `json:"image,omitempty"`
}

// DBHost is the database's hostname on the Coldship network.
func DBHost(app, name string) string { return app + "-db-" + name }

func dbContainer(app, name string) string { return "coldship-" + DBHost(app, name) }
func dbVolume(app, name string) string    { return "coldship-" + DBHost(app, name) + "-data" }

func dbImage(engine, version string) string { return engine + ":" + version + "-alpine" }

// URL is the connection URL injected into the app's env.
func (d DB) URL() string {
	host := DBHost(d.App, d.Name)
	switch d.Engine {
	case spec.EngineRedis:
		// Name the "default" user: with an empty username some clients
		// (redis-cli 7 among them) authenticate as user "" and fail.
		return fmt.Sprintf("redis://default:%s@%s:6379/0", url.QueryEscape(d.Password), host)
	default:
		return fmt.Sprintf("postgres://app:%s@%s:5432/app?sslmode=disable", url.QueryEscape(d.Password), host)
	}
}

// SnapshotExt is the file extension for the engine's snapshot format.
func SnapshotExt(engine string) string {
	if engine == spec.EngineRedis {
		return ".rdb"
	}
	return ".dump"
}

func (d *Docker) EnsureDatabase(ctx context.Context, db DB, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	if err := d.EnsureNetwork(ctx); err != nil {
		return err
	}
	name, image := dbContainer(db.App, db.Name), dbImage(db.Engine, db.Version)
	st, err := d.DatabaseStatus(ctx, db.App, db.Name)
	if err != nil {
		return err
	}
	if st.Exists {
		if err := d.checkOwner(ctx, name, db.App, db.Name); err != nil {
			return err
		}
	}
	switch {
	case st.Exists && st.Image != image:
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
		cs := ContainerSpec{Name: name, Image: image,
			Labels:  map[string]string{"coldship.app": db.App, "coldship.db": db.Name},
			Aliases: []string{DBHost(db.App, db.Name)}}
		switch db.Engine {
		case spec.EngineRedis:
			cs.Env = []string{"REDIS_PASSWORD=" + db.Password}
			cs.Binds = []string{dbVolume(db.App, db.Name) + ":/data"}
			cs.Cmd = []string{"sh", "-c", `exec redis-server --requirepass "$REDIS_PASSWORD" --save 60 1 --appendonly no`}
		default:
			cs.Env = []string{"POSTGRES_USER=app", "POSTGRES_DB=app", "POSTGRES_PASSWORD=" + db.Password}
			cs.Binds = []string{dbVolume(db.App, db.Name) + ":/var/lib/postgresql/data"}
		}
		if se, err := d.RunContainer(ctx, cs); err != nil {
			return apierr.New(502, "database_start_failed", "could not create database %s: %s", db.Name, se)
		}
	}
	return d.waitDBReady(ctx, db, log)
}

func (d *Docker) waitDBReady(ctx context.Context, db DB, log io.Writer) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		if d.dbReady(ctx, db) {
			return nil
		}
		if time.Now().After(deadline) {
			out, _ := d.tailLogs(context.WithoutCancel(ctx), dbContainer(db.App, db.Name), 30, "")
			fmt.Fprintf(log, "coldship: database %s did not become ready; its last output:\n%s", db.Name, out)
			return apierr.New(502, "database_not_ready", "database %s did not accept connections within 90s", db.Name).
				WithHint("read the deploy log for the database's output")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// redisAuth passes the password in the exec request, not on a command line.
func redisAuth(db DB) []string { return []string{"REDISCLI_AUTH=" + db.Password} }

func (d *Docker) dbReady(ctx context.Context, db DB) bool {
	c := dbContainer(db.App, db.Name)
	switch db.Engine {
	case spec.EngineRedis:
		var out bytes.Buffer
		code, _, err := d.Exec(ctx, c, []string{"redis-cli", "ping"}, ExecIO{Env: redisAuth(db), Stdout: &out})
		return err == nil && code == 0 && strings.Contains(out.String(), "PONG")
	default:
		// Over TCP: during first-time setup postgres only listens on its
		// socket, and restarts before it is really ready.
		code, _, err := d.Exec(ctx, c, []string{"pg_isready", "-h", "127.0.0.1", "-U", "app", "-d", "app"}, ExecIO{})
		return err == nil && code == 0
	}
}

func (d *Docker) DatabaseStatus(ctx context.Context, app, name string) (DBStatus, error) {
	info, err := d.InspectContainer(ctx, dbContainer(app, name))
	if err != nil || !info.Exists {
		return DBStatus{}, err
	}
	return DBStatus{Exists: true, Running: info.State == "running", State: info.State, Image: info.Image}, nil
}

func (d *Docker) RemoveDatabase(ctx context.Context, app, name string) error {
	if err := d.checkOwner(ctx, dbContainer(app, name), app, name); err != nil {
		return err
	}
	if err := d.ForceRemove(ctx, dbContainer(app, name)); err != nil {
		return apierr.New(502, "database_remove_failed", "could not remove database %s: %v", name, err)
	}
	if err := d.removeVolume(ctx, dbVolume(app, name)); err != nil {
		return apierr.New(502, "database_remove_failed", "could not remove database volume %s: %v", name, err)
	}
	return nil
}

// SnapshotDatabase writes a consistent snapshot to w: a pg_dump custom
// archive for postgres, an RDB file for redis.
func (d *Docker) SnapshotDatabase(ctx context.Context, db DB, w io.Writer) error {
	c := dbContainer(db.App, db.Name)
	var cmd []string
	switch db.Engine {
	case spec.EngineRedis:
		code, se, err := d.Exec(ctx, c, []string{"redis-cli", "SAVE"}, ExecIO{Env: redisAuth(db)})
		if err != nil || code != 0 {
			return apierr.New(502, "snapshot_failed", "redis SAVE failed for %s: %s", db.Name, errText(se, err))
		}
		cmd = []string{"cat", "/data/dump.rdb"}
	default:
		cmd = []string{"pg_dump", "-U", "app", "-d", "app", "-Fc"}
	}
	code, se, err := d.Exec(ctx, c, cmd, ExecIO{Stdout: w})
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
	c := dbContainer(db.App, db.Name)
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
	default:
		code, se, err := d.Exec(ctx, c, []string{"pg_restore", "-U", "app", "-d", "app",
			"--clean", "--if-exists", "--no-owner", "--single-transaction"}, ExecIO{Stdin: r, Stdout: log})
		if err != nil || code != 0 {
			fmt.Fprintln(log, se)
			return apierr.New(502, "restore_failed", "pg_restore failed for %s: %s", db.Name, lastLine(errText(se, err)))
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
