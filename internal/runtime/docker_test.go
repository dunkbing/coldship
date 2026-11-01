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

// Newer Docker versions report missing objects in lower case.
func TestMissingContainerIsNotAnError(t *testing.T) {
	for _, msg := range []string{"Error: No such container: x", "error: no such object: x"} {
		dir := t.TempDir()
		bin := filepath.Join(dir, "docker")
		script := "#!/bin/sh\ncase \"$1\" in inspect) echo '" + msg + "' >&2; exit 1;; esac\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		d := NewDocker()
		d.Bin = bin
		if err := d.Remove(context.Background(), "web", nil); err != nil {
			t.Fatalf("%q: %v", msg, err)
		}
		if st, err := d.Status(context.Background(), "web"); err != nil || st.Exists {
			t.Fatalf("%q: status %+v %v", msg, st, err)
		}
	}
}

func TestPingExplainsTheFix(t *testing.T) {
	cases := map[string]string{
		"permission denied while trying to connect to the Docker daemon socket":                             "docker_permission_denied",
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?": "docker_not_running",
	}
	for stderr, want := range cases {
		dir := t.TempDir()
		bin := filepath.Join(dir, "docker")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '"+stderr+"' >&2\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		d := NewDocker()
		d.Bin = bin
		ae, ok := apierr.As(d.Ping(context.Background()))
		if !ok || ae.Code != want || ae.Hint == "" {
			t.Fatalf("%q: want %s with a hint, got %+v", stderr, want, ae)
		}
	}
	d := NewDocker()
	d.Bin = "coldship-no-such-docker"
	if ae, ok := apierr.As(d.Ping(context.Background())); !ok || ae.Code != "docker_not_installed" {
		t.Fatalf("missing docker: got %+v", ae)
	}
}
