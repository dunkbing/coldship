package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"hangar/internal/apierr"
	"hangar/internal/spec"

	"hangar/internal/secrets"
	"path/filepath"
	"testing"
	"time"
)

// A database created before previews existed must gain the new columns.
func TestMigratesOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE apps (name TEXT PRIMARY KEY, spec TEXT, current_deployment TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
		INSERT INTO apps VALUES ('web', NULL, '', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z');`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	keys := secrets.New()
	st, err := Open(path, keys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetPreview(t.Context(), "web", "parent", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a, err := st.GetApp(t.Context(), "web")
	if err != nil || a.PreviewOf != "parent" || a.ExpiresAt == nil {
		t.Fatalf("migrated app: %+v %v", a, err)
	}
	// Opening again must not fail on the already-added columns.
	st.Close()
	st2, err := Open(path, keys)
	if err != nil {
		t.Fatal(err)
	}
	st2.Close()
}

const (
	envSecret     = "env-hunter2-6f1c"
	dbPassword    = "dbpass-9a7e3b"
	webhookSecret = "whsec-41d2c8"
	logSecret     = "log-token-77ab"
)

func writeSecrets(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	sp := &spec.App{Name: "web", Image: "x", Env: map[string]string{"SECRET": envSecret}}
	now := time.Now()
	if err := st.UpsertApp(ctx, &App{Name: "web", Spec: sp, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePlan(ctx, &Plan{ID: "plan_1", App: "web", Desired: sp, Status: PlanPending, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveDeployment(ctx, &Deployment{ID: "dep_1", App: "web", Spec: sp, Status: DeployRunning, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveDatabase(ctx, &Database{App: "web", Name: "main", Engine: "postgres", Version: "17", Password: dbPassword, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWebhook(ctx, &Webhook{App: "web", Secret: webhookSecret, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveDeployLog(ctx, "dep_1", []string{"token=" + logSecret}); err != nil {
		t.Fatal(err)
	}
}

func checkSecrets(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	a, err := st.GetApp(ctx, "web")
	if err != nil || a.Spec.Env["SECRET"] != envSecret {
		t.Fatalf("app spec: %+v %v", a, err)
	}
	if p, err := st.GetPlan(ctx, "plan_1"); err != nil || p.Desired.Env["SECRET"] != envSecret {
		t.Fatalf("plan: %v", err)
	}
	if ps, err := st.ListPlans(ctx, "", 10); err != nil || len(ps) != 1 {
		t.Fatalf("list plans: %v", err)
	}
	if ds, err := st.ListDeployments(ctx, "web", 10); err != nil || ds[0].Spec.Env["SECRET"] != envSecret {
		t.Fatalf("deployments: %v", err)
	}
	if d, err := st.GetDatabase(ctx, "web", "main"); err != nil || d.Password != dbPassword {
		t.Fatalf("database: %+v %v", d, err)
	}
	if w, err := st.GetWebhook(ctx, "web"); err != nil || w.Secret != webhookSecret {
		t.Fatalf("webhook: %+v %v", w, err)
	}
	if l, err := st.GetDeployLog(ctx, "dep_1"); err != nil || l[0] != "token="+logSecret {
		t.Fatalf("deploy log: %v %v", l, err)
	}
}

// rawContains reports which secrets appear anywhere in the database files.
func rawContains(t *testing.T, path string) []string {
	t.Helper()
	var found []string
	for _, f := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, s := range []string{envSecret, dbPassword, webhookSecret, logSecret} {
			if bytes.Contains(b, []byte(s)) {
				found = append(found, filepath.Base(f)+": "+s)
			}
		}
	}
	return found
}

func TestSecretsNeverHitDiskInPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	keys := secrets.New()
	st, err := Open(path, keys)
	if err != nil {
		t.Fatal(err)
	}
	writeSecrets(t, st)
	checkSecrets(t, st)
	if found := rawContains(t, path); len(found) > 0 {
		t.Fatalf("secrets found in plaintext on disk: %v", found)
	}
	status, _ := st.SecretsStatus(context.Background())
	if status.Plaintext != 0 || status.Encrypted["webhooks.secret"] != 1 || status.KeyID != keys.CurrentID() {
		t.Fatalf("status: %+v", status)
	}
	st.Close()
	st, err = Open(path, keys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkSecrets(t, st)
}

// A database from before encryption: its plaintext must be encrypted on
// open, and no copy may survive in freed pages or the WAL.
func TestMigratesPlaintextAndScrubs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	keys := secrets.New()
	st, _ := Open(path, keys)
	writeSecrets(t, st)
	// Turn it into what an older version wrote: plaintext, no key check.
	for _, c := range sealedColumns {
		rows, _ := st.db.Query(`SELECT ` + strings.Join(c.keys, ", ") + `, ` + c.col + ` FROM ` + c.table)
		type r struct {
			keys []string
			v    string
		}
		var all []r
		for rows.Next() {
			k := make([]string, len(c.keys))
			dest := []any{}
			for i := range k {
				dest = append(dest, &k[i])
			}
			var v string
			rows.Scan(append(dest, &v)...)
			all = append(all, r{k, v})
		}
		rows.Close()
		for _, x := range all {
			plain, _ := keys.Open(x.v, c.aad(x.keys))
			where := []string{}
			args := []any{string(plain)}
			for i, k := range c.keys {
				where = append(where, k+" = ?")
				args = append(args, x.keys[i])
			}
			st.db.Exec(`UPDATE `+c.table+` SET `+c.col+` = ? WHERE `+strings.Join(where, " AND "), args...)
		}
	}
	st.db.Exec(`DELETE FROM meta`)
	st.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	st.Close()
	if found := rawContains(t, path); len(found) < 4 {
		t.Fatalf("test setup: expected plaintext on disk, found %v", found)
	}

	st, err := Open(path, secrets.New()) // a brand-new key, as on first upgrade
	if err != nil {
		t.Fatal(err)
	}
	checkSecrets(t, st)
	st.Close()
	if found := rawContains(t, path); len(found) > 0 {
		t.Fatalf("plaintext survived the migration: %v", found)
	}
}

func TestWrongKeyFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	st, _ := Open(path, secrets.New())
	writeSecrets(t, st)
	st.Close()
	if _, err := Open(path, secrets.New()); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("want ErrKeyMismatch, got %v", err)
	}
}

func TestRowBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	st, _ := Open(path, secrets.New())
	defer st.Close()
	writeSecrets(t, st)
	ctx := context.Background()
	st.SaveWebhook(ctx, &Webhook{App: "other", Secret: "x", CreatedAt: time.Now()})
	// Copy web's sealed secret into other's row.
	st.db.Exec(`UPDATE webhooks SET secret = (SELECT secret FROM webhooks WHERE app = 'web') WHERE app = 'other'`)
	_, err := st.GetWebhook(ctx, "other")
	if ae, ok := apierr.As(err); !ok || ae.Code != "secrets_unreadable" {
		t.Fatalf("a secret copied to another row must not decrypt: %v", err)
	}
}

func TestRotateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	old := secrets.New()
	st, _ := Open(path, old)
	writeSecrets(t, st)
	next := old.WithNewKey()
	n, err := st.RotateKey(context.Background(), next)
	if err != nil || n < 6 {
		t.Fatalf("rotate: %d %v", n, err)
	}
	st.Close()
	// Crash before dropping the old key: the two-key ring still works.
	st, err = Open(path, next)
	if err != nil {
		t.Fatal(err)
	}
	checkSecrets(t, st)
	st.Close()
	// After rotation only the new key is needed, and the old one is useless.
	st, err = Open(path, next.OnlyCurrent())
	if err != nil {
		t.Fatal(err)
	}
	checkSecrets(t, st)
	st.Close()
	if _, err := Open(path, old); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("the old key alone must no longer open the database: %v", err)
	}
}

func TestDecryptAllForDowngrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	keys := secrets.New()
	st, _ := Open(path, keys)
	writeSecrets(t, st)
	st.Close()
	n, err := DecryptAll(context.Background(), path, keys)
	if err != nil || n < 6 {
		t.Fatalf("decrypt: %d %v", n, err)
	}
	db, _ := sql.Open("sqlite", "file:"+path)
	var secret string
	db.QueryRow(`SELECT secret FROM webhooks WHERE app = 'web'`).Scan(&secret)
	db.Close()
	if secret != webhookSecret {
		t.Fatalf("an older version must be able to read plaintext: %q", secret)
	}
	// A newer version with a new key re-encrypts it.
	st, err = Open(path, secrets.New())
	if err != nil {
		t.Fatal(err)
	}
	checkSecrets(t, st)
	st.Close()
}

// Writes racing a rotation must not leave values under the dropped key.
func TestRotateKeyWithConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	old := secrets.New()
	st, _ := Open(path, old)
	writeSecrets(t, st)
	ctx := context.Background()
	stop := make(chan struct{})
	done := make(chan int)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				done <- n
				return
			default:
			}
			if err := st.SaveDeployLog(ctx, fmt.Sprintf("race_%d", n), []string{"line"}); err != nil {
				t.Error(err)
			}
			n++
		}
	}()
	time.Sleep(20 * time.Millisecond)
	next := old.WithNewKey()
	for i := 0; i < 3; i++ {
		if _, err := st.RotateKey(ctx, next); err != nil {
			t.Fatal(err)
		}
		next = next.WithNewKey()
	}
	close(stop)
	n := <-done
	// What the rotator checks before dropping old keys.
	inUse, err := st.KeyIDsInUse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current := st.kr.Load().CurrentID()
	for id := range inUse {
		if id != current {
			t.Fatalf("values still use key %s after rotation: %v", id, inUse)
		}
	}
	only := st.kr.Load().OnlyCurrent()
	st.Close()
	st, err = Open(path, only)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkSecrets(t, st)
	for i := 0; i < n; i++ {
		if _, err := st.GetDeployLog(ctx, fmt.Sprintf("race_%d", i)); err != nil {
			t.Fatalf("log %d written during rotation is unreadable with the new key alone: %v", i, err)
		}
	}
	if n == 0 {
		t.Fatal("the writer should have run during rotation")
	}
}

func TestMissingKeyCheckWithEncryptedValuesFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	keys := secrets.New()
	st, _ := Open(path, keys)
	writeSecrets(t, st)
	st.db.Exec(`DELETE FROM meta WHERE k = 'key_check'`)
	st.Close()
	_, err := Open(path, secrets.New())
	if !errors.Is(err, ErrKeyMismatch) || !strings.Contains(err.Error(), keys.CurrentID()) {
		t.Fatalf("a lost key check must not let a new key take over: %v", err)
	}
}

func TestMismatchNamesKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	a := secrets.New()
	st, _ := Open(path, a)
	st.Close()
	b := secrets.New()
	_, err := Open(path, b)
	if err == nil || !strings.Contains(err.Error(), a.CurrentID()) || !strings.Contains(err.Error(), b.CurrentID()) {
		t.Fatalf("the error should name both key ids: %v", err)
	}
}

func TestScrubRetriedOnNextStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	keys := secrets.New()
	st, _ := Open(path, keys)
	st.db.Exec(`INSERT INTO meta(k, v) VALUES('scrub_pending', '1')`) // as if the scrub had failed
	st.Close()
	st, err := Open(path, keys)
	if err != nil || st.ScrubWarning != nil {
		t.Fatalf("reopen: %v %v", err, st.ScrubWarning)
	}
	var v string
	if st.db.QueryRow(`SELECT v FROM meta WHERE k = 'scrub_pending'`).Scan(&v) == nil {
		t.Fatal("a successful scrub should clear the pending flag")
	}
	st.Close()
}

// Deterministic version of the race: a write that sealed its value with the
// old key and hasn't stored it yet must finish before rotation re-encrypts,
// so its value ends up under the new key.
func TestRotationWaitsForInFlightWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	old := secrets.New()
	st, _ := Open(path, old)
	defer st.Close()
	ctx := context.Background()

	st.mu.RLock()                                                              // an in-flight write...
	sealed, err := st.seal([]byte(`["in flight"]`), "deploy_logs.lines:dep_x") // ...sealed with the old key
	if err != nil {
		t.Fatal(err)
	}
	next := old.WithNewKey()
	rotated := make(chan error, 1)
	go func() {
		_, err := st.RotateKey(ctx, next)
		rotated <- err
	}()
	select {
	case <-rotated:
		t.Fatal("rotation must wait for in-flight writes")
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := st.db.Exec(`INSERT INTO deploy_logs(deployment, lines) VALUES('dep_x', ?)`, sealed); err != nil {
		t.Fatal(err)
	}
	st.mu.RUnlock()
	if err := <-rotated; err != nil {
		t.Fatal(err)
	}
	inUse, _ := st.KeyIDsInUse(ctx)
	for id := range inUse {
		if id != next.CurrentID() {
			t.Fatalf("a value still uses the old key %s after rotation: %v", id, inUse)
		}
	}
	st.SetKeyring(next.OnlyCurrent())
	if l, err := st.GetDeployLog(ctx, "dep_x"); err != nil || l[0] != "in flight" {
		t.Fatalf("the in-flight write must be readable with the new key alone: %v %v", l, err)
	}
}
