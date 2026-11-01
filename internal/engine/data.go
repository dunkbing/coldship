package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

// Data services are databases and storage that stand on their own, not
// owned by one app: apps connect to them with `uses`. They are stored as
// databases with no owning app.

// DataRequest creates a data service.
type DataRequest struct {
	Name    string `json:"name" jsonschema:"name, lowercase letters, digits and dashes"`
	Engine  string `json:"engine" jsonschema:"postgres, mysql, redis, or s3 (S3-compatible object storage)"`
	Version string `json:"version,omitempty" jsonschema:"version (default: the latest supported)"`
}

// DataView describes a data service, or a database owned by an app.
type DataView struct {
	Name    string `json:"name"`
	Engine  string `json:"engine"`
	Version string `json:"version"`
	// App owns the database; "" for a standalone data service.
	App    string           `json:"app,omitempty"`
	Host   string           `json:"host"`
	Status runtime.DBStatus `json:"status"`
	// UsedBy lists the apps that connect to it.
	UsedBy []string `json:"used_by"`
	// Env lists the variables it gives an app (names only).
	Env            []string        `json:"env"`
	LatestSnapshot *store.Snapshot `json:"latest_snapshot,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// envFor is what db injects into an app; override renames a URL variable.
func envFor(db runtime.DB, override string) map[string]string {
	if db.Engine == spec.EngineS3 {
		return db.S3Env()
	}
	name := override
	if name == "" {
		name = "DATABASE_URL"
		if db.Engine == spec.EngineRedis {
			name = "REDIS_URL"
		}
	}
	return map[string]string{name: db.URL()}
}

func (e *Engine) serviceDB(ctx context.Context, name string) (runtime.DB, error) {
	rec, err := e.st.GetDatabase(ctx, "", name)
	if err != nil {
		return runtime.DB{}, err
	}
	if rec == nil {
		return runtime.DB{}, apierr.NotFound("data_service", name).WithHint("see coldship data list, or create it with coldship data create")
	}
	return runtime.DB{Name: rec.Name, Engine: rec.Engine, Version: rec.Version, Password: rec.Password}, nil
}

// CreateData creates a data service and waits until it accepts connections.
func (e *Engine) CreateData(ctx context.Context, actor string, req DataRequest) (*DataView, error) {
	if !nameOK(req.Name) {
		return nil, apierr.Invalid("name", "data service name %q must be lowercase letters, digits and dashes", req.Name)
	}
	versions := spec.Versions(req.Engine)
	if len(versions) == 0 {
		return nil, apierr.Invalid("engine", "engine %q isn't supported; use postgres, mysql, redis or s3", req.Engine).
			WithFix(map[string]any{"engine": spec.EnginePostgres})
	}
	if req.Version == "" {
		req.Version = versions[len(versions)-1]
	}
	if !slices.Contains(versions, req.Version) {
		return nil, apierr.Invalid("version", "%s version %q isn't supported; use one of %s", req.Engine, req.Version, strings.Join(versions, ", ")).
			WithFix(map[string]any{"version": versions[len(versions)-1]})
	}
	defer e.lockApp("data:" + req.Name)()
	if rec, err := e.st.GetDatabase(ctx, "", req.Name); err != nil {
		return nil, err
	} else if rec != nil {
		return nil, apierr.New(409, "data_exists", "a data service named %s already exists", req.Name).WithField("name")
	}
	if err := e.checkDataName(ctx, req.Name); err != nil {
		return nil, err
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	rec := &store.Database{Name: req.Name, Engine: req.Engine, Version: req.Version, Password: hex.EncodeToString(b), CreatedAt: e.Now()}
	if err := e.st.SaveDatabase(ctx, rec); err != nil {
		return nil, err
	}
	db := runtime.DB{Name: rec.Name, Engine: rec.Engine, Version: rec.Version, Password: rec.Password}
	// Finish even if the caller goes away: a half-created service is worse.
	ctx = context.WithoutCancel(ctx)
	if err := e.rt.EnsureDatabase(ctx, db, io.Discard); err != nil {
		e.rt.RemoveDatabase(ctx, "", req.Name)
		e.st.DeleteDatabase(ctx, "", req.Name)
		return nil, err
	}
	e.audit(ctx, actor, "data.create", req.Name, "", map[string]any{"engine": req.Engine, "version": req.Version})
	return e.GetData(ctx, req.Name)
}

// checkDataName rejects a service whose container name an app already uses.
func (e *Engine) checkDataName(ctx context.Context, name string) error {
	host := runtime.ServiceHost(name)
	apps, err := e.st.ListApps(ctx)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.Spec == nil {
			continue
		}
		hosts, _ := runtime.Names(a.Spec)
		if slices.Contains(hosts, host) {
			return apierr.New(409, "name_in_use", "app %s already uses the Docker name %s; pick another name", a.Name, host).WithField("name")
		}
	}
	return nil
}

// usersOf lists the apps whose spec uses the data service.
func usersOf(apps []store.App, name string) []string {
	users := []string{}
	for _, a := range apps {
		if a.Spec == nil {
			continue
		}
		for _, u := range a.Spec.Uses {
			if n, _ := spec.ParseUse(u); n == name {
				users = append(users, a.Name)
			}
		}
	}
	return users
}

func (e *Engine) dataView(ctx context.Context, rec store.Database, apps []store.App) DataView {
	db := runtime.DB{App: rec.App, Name: rec.Name, Engine: rec.Engine}
	v := DataView{Name: rec.Name, Engine: rec.Engine, Version: rec.Version, App: rec.App, Host: db.Host(), CreatedAt: rec.CreatedAt}
	v.Status, _ = e.rt.DatabaseStatus(ctx, rec.App, rec.Name)
	if rec.App == "" {
		v.UsedBy = usersOf(apps, rec.Name)
	} else {
		v.UsedBy = []string{rec.App}
	}
	env := envFor(db, "")
	if rec.App != "" {
		for _, a := range apps {
			if a.Name == rec.App && a.Spec != nil {
				for _, d := range a.Spec.Databases {
					if d.Name == rec.Name {
						env = map[string]string{d.Env: ""}
					}
				}
			}
		}
	}
	for k := range env {
		v.Env = append(v.Env, k)
	}
	sort.Strings(v.Env)
	if snaps, err := e.st.ListSnapshots(ctx, rec.App, rec.Name); err == nil && len(snaps) > 0 {
		v.LatestSnapshot = &snaps[0]
	}
	return v
}

// ListData lists data services, then apps' own databases.
func (e *Engine) ListData(ctx context.Context) ([]DataView, error) {
	recs, err := e.st.AllDatabases(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := e.st.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DataView, 0, len(recs))
	for _, r := range recs {
		out = append(out, e.dataView(ctx, r, apps))
	}
	return out, nil
}

func (e *Engine) GetData(ctx context.Context, name string) (*DataView, error) {
	rec, err := e.st.GetDatabase(ctx, "", name)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, apierr.NotFound("data_service", name)
	}
	apps, err := e.st.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	v := e.dataView(ctx, *rec, apps)
	return &v, nil
}

// DataConnection returns a data service's full connection details,
// secrets included, for a human connecting with their own tools.
func (e *Engine) DataConnection(ctx context.Context, actor, name string) (map[string]string, error) {
	db, err := e.serviceDB(ctx, name)
	if err != nil {
		return nil, err
	}
	e.audit(ctx, actor, "data.reveal", name, "", nil)
	return envFor(db, ""), nil
}

// DeleteData deletes a data service and its data, after a snapshot. It
// refuses while apps use it.
func (e *Engine) DeleteData(ctx context.Context, actor, name, reason string) error {
	defer e.lockApp("data:" + name)()
	db, err := e.serviceDB(ctx, name)
	if err != nil {
		return err
	}
	apps, err := e.st.ListApps(ctx)
	if err != nil {
		return err
	}
	if users := usersOf(apps, name); len(users) > 0 {
		return apierr.New(409, "data_in_use", "%s is used by %s; disconnect them first", name, strings.Join(users, ", ")).
			WithDetail("used_by", users).
			WithHint("remove it from each app's uses (coldship deploy --name <app> --unuse " + name + ")")
	}
	ctx = context.WithoutCancel(ctx)
	if db.Engine != spec.EngineS3 {
		if _, err := e.takeSnapshot(ctx, actor, "", spec.Database{Name: name, Engine: db.Engine, Version: db.Version},
			"before deleting data service "+name, false); err != nil {
			return apierr.New(502, "snapshot_failed", "%s was not deleted because its snapshot failed: %v", name, err)
		}
	}
	if err := e.rt.RemoveDatabase(ctx, "", name); err != nil {
		return err
	}
	if err := e.st.DeleteDatabase(ctx, "", name); err != nil {
		return err
	}
	e.audit(ctx, actor, "data.delete", name, reason, nil)
	return nil
}

// SnapshotData snapshots a data service now.
func (e *Engine) SnapshotData(ctx context.Context, actor, name, reason string) (*store.Snapshot, error) {
	defer e.lockApp("data:" + name)()
	db, err := e.serviceDB(ctx, name)
	if err != nil {
		return nil, err
	}
	if db.Engine == spec.EngineS3 {
		return nil, apierr.New(400, "snapshot_unsupported", "snapshots of S3 storage aren't supported yet").
			WithHint("back up the bucket with an S3 tool, such as rclone or aws s3 sync")
	}
	if reason == "" {
		reason = "manual"
	}
	return e.takeSnapshot(ctx, actor, "", spec.Database{Name: name, Engine: db.Engine, Version: db.Version}, reason, true)
}

func (e *Engine) DataSnapshots(ctx context.Context, name string) ([]store.Snapshot, error) {
	if _, err := e.serviceDB(ctx, name); err != nil {
		return nil, err
	}
	return e.st.ListSnapshots(ctx, "", name)
}

// RestoreData replaces a data service's contents with a snapshot, saving
// the current data as a snapshot first.
func (e *Engine) RestoreData(ctx context.Context, actor, name, snapshot, reason string) (string, error) {
	defer e.lockApp("data:" + name)()
	db, err := e.serviceDB(ctx, name)
	if err != nil {
		return "", err
	}
	sn, err := e.st.GetSnapshot(ctx, snapshot)
	if err != nil {
		return "", err
	}
	if sn == nil || sn.App != "" || sn.Database != name {
		return "", apierr.New(404, "snapshot_not_found", "%s has no snapshot %s", name, snapshot).WithHint("coldship data snapshots " + name)
	}
	return e.restoreFrom(context.WithoutCancel(ctx), actor, "", spec.Database{Name: name, Engine: db.Engine, Version: db.Version}, sn, reason)
}

// ensureUses starts the data services run uses and injects their env.
func (e *Engine) ensureUses(ctx context.Context, run *spec.App, log io.Writer) error {
	for _, u := range run.Uses {
		name, override := spec.ParseUse(u)
		db, err := e.serviceDB(ctx, name)
		if err != nil {
			return err
		}
		if err := e.rt.EnsureDatabase(ctx, db, log); err != nil {
			return err
		}
		if run.Env == nil {
			run.Env = map[string]string{}
		}
		env := envFor(db, override)
		keys := make([]string, 0, len(env))
		for k, v := range env {
			run.Env[k] = v
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(log, "coldship: data service %s (%s) connected: %s\n", name, db.Engine, strings.Join(keys, ", "))
	}
	return nil
}

// checkUses rejects a spec that uses a missing data service, or that would
// get one env var from two places.
func (e *Engine) checkUses(ctx context.Context, a *spec.App) error {
	from := map[string]string{}
	for k := range a.Env {
		from[k] = "env"
	}
	for _, d := range a.Databases {
		from[d.Env] = "database " + d.Name
	}
	for i, u := range a.Uses {
		name, override := spec.ParseUse(u)
		db, err := e.serviceDB(ctx, name)
		if err != nil {
			if ae, ok := apierr.As(err); ok {
				ae.Field = fmt.Sprintf("uses[%d]", i)
			}
			return err
		}
		if override != "" && db.Engine == spec.EngineS3 {
			return apierr.Invalid(fmt.Sprintf("uses[%d]", i), "%s is S3 storage, which sets several AWS_* and S3_* variables; it can't be renamed", name)
		}
		for k := range envFor(db, override) {
			if prev, ok := from[k]; ok {
				return apierr.Invalid(fmt.Sprintf("uses[%d]", i), "%s would set %s, which %s already sets", name, k, prev).
					WithFix(map[string]any{"uses": name + "=" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_URL"})
			}
			from[k] = "data service " + name
		}
	}
	return nil
}
