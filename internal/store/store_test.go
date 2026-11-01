package store

import (
	"database/sql"
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

	st, err := Open(path)
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
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st2.Close()
}
