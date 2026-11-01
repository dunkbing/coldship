package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

// RestoreRequest asks for a plan that restores a database from a snapshot.
type RestoreRequest struct {
	App      string `json:"app"`
	Database string `json:"database"`
	Snapshot string `json:"snapshot"`
}

type DatabaseView struct {
	Name         string           `json:"name"`
	Engine       string           `json:"engine"`
	Version      string           `json:"version"`
	Env          string           `json:"env"` // env var the URL is injected as
	Host         string           `json:"host"`
	Status       runtime.DBStatus `json:"status"`
	Snapshots    int              `json:"snapshots"`
	LastSnapshot *store.Snapshot  `json:"last_snapshot,omitempty"`
}

// dbCreds returns a database's runtime identity, generating and storing
// its password the first time.
func (e *Engine) dbCreds(ctx context.Context, app string, d spec.Database) (runtime.DB, error) {
	rec, err := e.st.GetDatabase(ctx, app, d.Name)
	if err != nil {
		return runtime.DB{}, err
	}
	if rec == nil {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return runtime.DB{}, err
		}
		rec = &store.Database{App: app, Name: d.Name, Engine: d.Engine, Version: d.Version,
			Password: hex.EncodeToString(b), CreatedAt: e.Now()}
		if err := e.st.SaveDatabase(ctx, rec); err != nil {
			return runtime.DB{}, err
		}
	}
	return runtime.DB{App: app, Name: d.Name, Engine: d.Engine, Version: d.Version, Password: rec.Password}, nil
}

// ensureDatabases starts the app's databases and injects their connection
// URLs into run's env (run is the throwaway copy that gets deployed; the
// stored spec never contains the URLs).
func (e *Engine) ensureDatabases(ctx context.Context, run *spec.App, log io.Writer) error {
	for _, d := range run.Databases {
		db, err := e.dbCreds(ctx, run.Name, d)
		if err != nil {
			return err
		}
		if err := e.rt.EnsureDatabase(ctx, db, log); err != nil {
			return err
		}
		if run.Env == nil {
			run.Env = map[string]string{}
		}
		run.Env[d.Env] = db.URL()
		fmt.Fprintf(log, "coldship: database %s (%s %s) ready; connection URL injected as %s\n", d.Name, d.Engine, d.Version, d.Env)
	}
	return nil
}

// Snapshot takes a snapshot of one database now.
func (e *Engine) Snapshot(ctx context.Context, actor, app, database, reason string) (*store.Snapshot, error) {
	defer e.lockApp(app)()
	a, err := e.st.GetApp(ctx, app)
	if err != nil {
		return nil, err
	}
	if a == nil || a.Spec == nil {
		return nil, apierr.NotFound("app", app)
	}
	for _, d := range a.Spec.Databases {
		if d.Name == database {
			if reason == "" {
				reason = "manual"
			}
			return e.snapshot(ctx, actor, app, d, reason)
		}
	}
	return nil, apierr.NotFound("database", database)
}

func (e *Engine) snapshot(ctx context.Context, actor, app string, d spec.Database, reason string) (*store.Snapshot, error) {
	db, err := e.dbCreds(ctx, app, d)
	if err != nil {
		return nil, err
	}
	sn := &store.Snapshot{ID: store.NewID("snap"), App: app, Database: d.Name, Engine: d.Engine, Version: d.Version,
		Reason: reason, Actor: actor, CreatedAt: e.Now()}
	dir := filepath.Join(e.SnapshotDir, app, d.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sn.Path = filepath.Join(dir, sn.CreatedAt.UTC().Format("20060102T150405Z")+"-"+sn.ID+runtime.SnapshotExt(d.Engine))
	tmp, err := os.CreateTemp(dir, ".snap-*")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(tmp, h)}
	snapErr := e.rt.SnapshotDatabase(ctx, db, cw)
	closeErr := tmp.Close()
	if snapErr == nil {
		snapErr = closeErr
	}
	if snapErr == nil {
		snapErr = os.Rename(tmp.Name(), sn.Path)
	}
	if snapErr != nil {
		os.Remove(tmp.Name())
		if _, ok := apierr.As(snapErr); !ok {
			snapErr = apierr.New(502, "snapshot_failed", "snapshot of database %s failed: %v", d.Name, snapErr)
		}
		return nil, snapErr
	}
	sn.Size, sn.SHA256 = cw.n, hex.EncodeToString(h.Sum(nil))
	if err := e.st.SaveSnapshot(ctx, sn); err != nil {
		return nil, err
	}
	e.audit(ctx, actor, "database.snapshot", app, reason, map[string]any{"database": d.Name, "snapshot": sn.ID, "size": sn.Size})
	e.pruneSnapshots(ctx, app, d.Name)
	return sn, nil
}

// pruneSnapshots keeps the newest SnapshotKeep snapshots of a database.
func (e *Engine) pruneSnapshots(ctx context.Context, app, database string) {
	if e.SnapshotKeep <= 0 {
		return
	}
	snaps, err := e.st.ListSnapshots(ctx, app, database)
	if err != nil {
		return
	}
	for _, sn := range snaps[min(len(snaps), e.SnapshotKeep):] {
		os.Remove(sn.Path)
		e.st.DeleteSnapshot(ctx, sn.ID)
	}
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// removeDatabases snapshots each database, then deletes it. A database
// whose snapshot fails is kept.
func (e *Engine) removeDatabases(ctx context.Context, actor string, app *spec.App, dbs []spec.Database, reason string, snapshotFirst bool, log io.Writer) error {
	for _, d := range dbs {
		if snapshotFirst && app.Stage != spec.StagePreview {
			sn, err := e.snapshot(ctx, actor, app.Name, d, reason)
			if err != nil {
				return apierr.New(502, "snapshot_failed", "database %s was not deleted because its snapshot failed: %v", d.Name, err).
					WithHint("fix the database (see diagnose), or take a snapshot manually, then try again")
			}
			fmt.Fprintf(log, "coldship: snapshot %s of database %s saved (%d bytes)\n", sn.ID, d.Name, sn.Size)
		}
		if err := e.rt.RemoveDatabase(ctx, app.Name, d.Name); err != nil {
			return err
		}
		if err := e.st.DeleteDatabase(ctx, app.Name, d.Name); err != nil {
			return err
		}
		fmt.Fprintf(log, "coldship: database %s deleted\n", d.Name)
	}
	return nil
}

// planRestore builds a destructive plan that restores a database.
func (e *Engine) planRestore(ctx context.Context, r *RestoreRequest) (*spec.App, *store.App, *store.Snapshot, error) {
	app, err := e.st.GetApp(ctx, r.App)
	if err != nil {
		return nil, nil, nil, err
	}
	if app == nil || app.Spec == nil {
		return nil, nil, nil, apierr.NotFound("app", r.App)
	}
	var db *spec.Database
	for i := range app.Spec.Databases {
		if app.Spec.Databases[i].Name == r.Database {
			db = &app.Spec.Databases[i]
		}
	}
	if db == nil {
		return nil, nil, nil, apierr.NotFound("database", r.Database)
	}
	sn, err := e.st.GetSnapshot(ctx, r.Snapshot)
	if err != nil {
		return nil, nil, nil, err
	}
	if sn == nil || sn.App != r.App || sn.Database != r.Database {
		return nil, nil, nil, apierr.New(404, "snapshot_not_found", "snapshot %q of %s/%s not found", r.Snapshot, r.App, r.Database).
			WithHint("list snapshots with coldship db snapshots " + r.App)
	}
	if sn.Engine != db.Engine {
		return nil, nil, nil, apierr.New(400, "invalid_request", "snapshot %s is a %s snapshot; %s is %s", sn.ID, sn.Engine, db.Name, db.Engine)
	}
	return app.Spec.Clone(), app, sn, nil
}

// restore runs a restore plan: it saves the current data as a new snapshot,
// verifies the chosen snapshot's checksum, then restores it.
func (e *Engine) restore(ctx context.Context, actor string, p *store.Plan, reason string) (string, error) {
	r := p.Restore
	sn, err := e.st.GetSnapshot(ctx, r.Snapshot)
	if err != nil {
		return "", err
	}
	if sn == nil {
		return "", apierr.New(404, "snapshot_not_found", "snapshot %s no longer exists", r.Snapshot)
	}
	var db spec.Database
	for _, d := range p.Desired.Databases {
		if d.Name == r.Database {
			db = d
		}
	}
	f, err := os.Open(sn.Path)
	if err != nil {
		return "", apierr.New(500, "snapshot_missing", "snapshot file %s is missing: %v", sn.Path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sn.SHA256 {
		return "", apierr.New(500, "snapshot_corrupt", "snapshot %s does not match its checksum; refusing to restore it", sn.ID)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	before, err := e.snapshot(ctx, actor, p.App, db, "before restoring "+sn.ID)
	if err != nil {
		return "", apierr.New(502, "snapshot_failed", "not restoring: the safety snapshot of the current data failed: %v", err)
	}
	creds, err := e.dbCreds(ctx, p.App, db)
	if err != nil {
		return "", err
	}
	if err := e.rt.RestoreDatabase(ctx, creds, f, io.Discard); err != nil {
		return "", err
	}
	e.audit(ctx, actor, "database.restore", p.App, reason, map[string]any{"database": db.Name, "snapshot": sn.ID, "saved_before": before.ID})
	return fmt.Sprintf("restored database %s from snapshot %s (%s); the data from before the restore is saved as snapshot %s",
		db.Name, sn.ID, sn.CreatedAt.UTC().Format(time.RFC3339), before.ID), nil
}

func (e *Engine) Databases(ctx context.Context, app string) ([]DatabaseView, error) {
	a, err := e.st.GetApp(ctx, app)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, apierr.NotFound("app", app)
	}
	out := []DatabaseView{}
	if a.Spec == nil {
		return out, nil
	}
	for _, d := range a.Spec.Databases {
		v := DatabaseView{Name: d.Name, Engine: d.Engine, Version: d.Version, Env: d.Env, Host: runtime.DBHost(app, d.Name)}
		v.Status, _ = e.rt.DatabaseStatus(ctx, app, d.Name)
		if snaps, err := e.st.ListSnapshots(ctx, app, d.Name); err == nil {
			v.Snapshots = len(snaps)
			if len(snaps) > 0 {
				v.LastSnapshot = &snaps[0]
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (e *Engine) Snapshots(ctx context.Context, app, database string) ([]store.Snapshot, error) {
	return e.st.ListSnapshots(ctx, app, database)
}

// ScheduledSnapshots snapshots every database of every app except previews.
func (e *Engine) ScheduledSnapshots(ctx context.Context) (int, error) {
	apps, err := e.st.ListApps(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range apps {
		if a.Spec == nil || a.PreviewOf != "" {
			continue
		}
		for _, d := range a.Spec.Databases {
			if _, err := e.Snapshot(ctx, "coldship", a.Name, d.Name, "scheduled"); err != nil {
				return n, fmt.Errorf("%s/%s: %w", a.Name, d.Name, err)
			}
			n++
		}
	}
	return n, nil
}
