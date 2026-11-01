package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dunkbing/coldship/internal/apierr"
)

// fakeDocker is a docker CLI stub: inspect prints labels, every call is
// logged.
func fakeDocker(t *testing.T, labels string) (*Docker, string) {
	t.Helper()
	dir := t.TempDir()
	logf := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + logf + "\n" +
		"case \"$1\" in inspect) printf '%s\\n' '" + labels + "';; esac\n"
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := NewDocker()
	d.Bin = bin
	return d, logf
}

func calls(t *testing.T, logf string) string {
	b, _ := os.ReadFile(logf)
	return string(b)
}

func TestRemoveRefusesContainersOfOthers(t *testing.T) {
	ctx := context.Background()
	for _, labels := range []string{"|", "web|main", "other|"} {
		d, logf := fakeDocker(t, labels)
		err := d.Remove(ctx, "web-db-main", nil)
		if ae, ok := apierr.As(err); !ok || ae.Code != "name_in_use" {
			t.Fatalf("labels %q: want name_in_use, got %v", labels, err)
		}
		if strings.Contains(calls(t, logf), "rm ") {
			t.Fatalf("labels %q: removed a container it doesn't own:\n%s", labels, calls(t, logf))
		}
	}

	d, logf := fakeDocker(t, "web|")
	if err := d.Remove(ctx, "web", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls(t, logf), "rm -f coldship-web") {
		t.Fatalf("own container not removed:\n%s", calls(t, logf))
	}

	d, _ = fakeDocker(t, "web|")
	if err := d.RemoveDatabase(ctx, "web", "main"); err == nil {
		t.Fatal("an app container must not be removed as a database")
	}
}
