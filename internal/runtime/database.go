package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

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
		if _, se, err := d.run(ctx, "start", name); err != nil {
			return apierr.New(502, "database_start_failed", "could not start database %s: %s", db.Name, se)
		}
	case !st.Exists:
		if _, _, err := d.run(ctx, "image", "inspect", image); err != nil {
			fmt.Fprintf(log, "coldship: pulling %s\n", image)
			if _, se, err := d.run(ctx, "pull", image); err != nil {
				return apierr.New(502, "database_start_failed", "could not pull %s: %s", image, se)
			}
		}
		fmt.Fprintf(log, "coldship: creating database %s (%s)\n", db.Name, image)
		envFile, err := os.CreateTemp("", "coldship-db-env-*")
		if err != nil {
			return err
		}
		defer os.Remove(envFile.Name())
		args := []string{"run", "-d", "--name", name, "--restart", "unless-stopped",
			"--label", "coldship.app=" + db.App, "--label", "coldship.db=" + db.Name,
			"--network", Network, "--network-alias", DBHost(db.App, db.Name), "--env-file", envFile.Name()}
		switch db.Engine {
		case spec.EngineRedis:
			fmt.Fprintf(envFile, "REDIS_PASSWORD=%s\n", db.Password)
			args = append(args, "-v", dbVolume(db.App, db.Name)+":/data", image,
				"sh", "-c", `exec redis-server --requirepass "$REDIS_PASSWORD" --save 60 1 --appendonly no`)
		default:
			fmt.Fprintf(envFile, "POSTGRES_USER=app\nPOSTGRES_DB=app\nPOSTGRES_PASSWORD=%s\n", db.Password)
			args = append(args, "-v", dbVolume(db.App, db.Name)+":/var/lib/postgresql/data", image)
		}
		envFile.Close()
		if _, se, err := d.run(ctx, args...); err != nil {
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
			out, _ := exec.CommandContext(context.WithoutCancel(ctx), d.Bin, "logs", "--tail", "30", dbContainer(db.App, db.Name)).CombinedOutput()
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

func (d *Docker) dbReady(ctx context.Context, db DB) bool {
	c := dbContainer(db.App, db.Name)
	switch db.Engine {
	case spec.EngineRedis:
		out, _, err := d.runEnv(ctx, []string{"REDISCLI_AUTH=" + db.Password}, "exec", "-e", "REDISCLI_AUTH", c, "redis-cli", "ping")
		return err == nil && strings.Contains(out, "PONG")
	default:
		// Over TCP: during first-time setup postgres only listens on its
		// socket, and restarts before it is really ready.
		_, _, err := d.run(ctx, "exec", c, "pg_isready", "-h", "127.0.0.1", "-U", "app", "-d", "app")
		return err == nil
	}
}

func (d *Docker) DatabaseStatus(ctx context.Context, app, name string) (DBStatus, error) {
	out, se, err := d.run(ctx, "inspect", "-f", "{{.State.Status}} {{.Config.Image}}", dbContainer(app, name))
	if err != nil {
		if strings.Contains(se, "No such") {
			return DBStatus{}, nil
		}
		return DBStatus{}, apierr.New(502, "runtime_unavailable", "docker inspect failed: %s", se)
	}
	state, image, _ := strings.Cut(strings.TrimSpace(out), " ")
	return DBStatus{Exists: true, Running: state == "running", State: state, Image: image}, nil
}

func (d *Docker) RemoveDatabase(ctx context.Context, app, name string) error {
	if err := d.checkOwner(ctx, dbContainer(app, name), app, name); err != nil {
		return err
	}
	if _, se, err := d.run(ctx, "rm", "-f", dbContainer(app, name)); err != nil && !strings.Contains(se, "No such container") {
		return apierr.New(502, "database_remove_failed", "could not remove database %s: %s", name, se)
	}
	if _, se, err := d.run(ctx, "volume", "rm", dbVolume(app, name)); err != nil && !strings.Contains(se, "no such volume") {
		return apierr.New(502, "database_remove_failed", "could not remove database volume %s: %s", name, se)
	}
	return nil
}

// SnapshotDatabase writes a consistent snapshot to w: a pg_dump custom
// archive for postgres, an RDB file for redis.
func (d *Docker) SnapshotDatabase(ctx context.Context, db DB, w io.Writer) error {
	c := dbContainer(db.App, db.Name)
	var args []string
	switch db.Engine {
	case spec.EngineRedis:
		if _, se, err := d.runEnv(ctx, []string{"REDISCLI_AUTH=" + db.Password}, "exec", "-e", "REDISCLI_AUTH", c, "redis-cli", "SAVE"); err != nil {
			return apierr.New(502, "snapshot_failed", "redis SAVE failed for %s: %s", db.Name, se)
		}
		args = []string{"exec", c, "cat", "/data/dump.rdb"}
	default:
		args = []string{"exec", c, "pg_dump", "-U", "app", "-d", "app", "-Fc"}
	}
	cmd := exec.CommandContext(ctx, d.Bin, args...)
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &errb
	if err := cmd.Run(); err != nil {
		return apierr.New(502, "snapshot_failed", "snapshot of database %s failed: %s", db.Name, strings.TrimSpace(errb.String()))
	}
	return nil
}

// RestoreDatabase replaces the database's contents with a snapshot.
func (d *Docker) RestoreDatabase(ctx context.Context, db DB, r io.Reader, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	c := dbContainer(db.App, db.Name)
	var errb bytes.Buffer
	switch db.Engine {
	case spec.EngineRedis:
		// Redis loads dump.rdb only at startup: stop it, replace the file
		// in the volume, start it again.
		fmt.Fprintf(log, "coldship: stopping redis %s to restore\n", db.Name)
		if _, se, err := d.run(ctx, "stop", c); err != nil {
			return apierr.New(502, "restore_failed", "could not stop %s: %s", db.Name, se)
		}
		cmd := exec.CommandContext(ctx, d.Bin, "run", "--rm", "-i", "-v", dbVolume(db.App, db.Name)+":/data",
			"--entrypoint", "sh", dbImage(db.Engine, db.Version), "-c", "cat > /data/dump.rdb")
		cmd.Stdin, cmd.Stderr = r, &errb
		runErr := cmd.Run()
		if _, se, err := d.run(ctx, "start", c); err != nil {
			return apierr.New(502, "restore_failed", "could not start %s after restoring: %s", db.Name, se)
		}
		if runErr != nil {
			return apierr.New(502, "restore_failed", "writing the snapshot failed: %s", strings.TrimSpace(errb.String()))
		}
	default:
		cmd := exec.CommandContext(ctx, d.Bin, "exec", "-i", c, "pg_restore", "-U", "app", "-d", "app",
			"--clean", "--if-exists", "--no-owner", "--single-transaction")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = r, log, &errb
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(log, errb.String())
			return apierr.New(502, "restore_failed", "pg_restore failed for %s: %s", db.Name, lastLine(errb.String()))
		}
	}
	return d.waitDBReady(ctx, db, log)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
