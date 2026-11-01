package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dunkbing/coldship/internal/secrets"
)

func TestIntegrationSecretIsEncrypted(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "h.db"), secrets.New())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SaveIntegration(ctx, &Integration{Name: "github", Secret: "ghp_secret", Account: "octo", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var raw string
	st.db.QueryRow(`SELECT secret FROM integrations WHERE name = 'github'`).Scan(&raw)
	if strings.Contains(raw, "ghp_secret") {
		t.Fatal("token stored in plaintext")
	}
	in, err := st.GetIntegration(ctx, "github")
	if err != nil || in.Secret != "ghp_secret" || in.Account != "octo" {
		t.Fatalf("%+v %v", in, err)
	}
}
