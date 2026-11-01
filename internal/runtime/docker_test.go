package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/client"

	"github.com/dunkbing/coldship/internal/apierr"
)

// fakeEngine is a minimal Docker Engine API: containers can be inspected and
// removed, and every removal is recorded.
type fakeEngine struct {
	mu         sync.Mutex
	containers map[string]map[string]string // name -> labels
	removed    []string
}

var versioned = regexp.MustCompile(`^/v[0-9.]+`)

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := versioned.ReplaceAllString(r.URL.Path, "")
	w.Header().Set("Api-Version", "1.47")
	notFound := func() {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"message": "No such container: " + path})
	}
	switch {
	case path == "/_ping":
		w.Write([]byte("OK"))
	case path == "/version":
		json.NewEncoder(w).Encode(map[string]string{"Version": "27.0.0", "ApiVersion": "1.47"})
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json") && r.Method == "GET":
		name := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		labels, ok := f.containers[name]
		if !ok {
			notFound()
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"Name": "/" + name, "Config": map[string]any{"Labels": labels},
			"State": map[string]any{"Status": "running", "Running": true}})
	case strings.HasPrefix(path, "/containers/") && r.Method == "DELETE":
		name := strings.TrimPrefix(path, "/containers/")
		if _, ok := f.containers[name]; !ok {
			notFound()
			return
		}
		delete(f.containers, name)
		f.removed = append(f.removed, name)
		w.WriteHeader(204)
	default:
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"message": "fake engine: unexpected " + r.Method + " " + path})
	}
}

func fakeDocker(t *testing.T, containers map[string]map[string]string) (*Docker, *fakeEngine) {
	t.Helper()
	f := &fakeEngine{containers: containers}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cli, err := client.New(client.WithHost("tcp://" + strings.TrimPrefix(srv.URL, "http://")))
	if err != nil {
		t.Fatal(err)
	}
	return &Docker{cli: cli}, f
}

func TestRemoveRefusesContainersOfOthers(t *testing.T) {
	ctx := context.Background()
	for _, labels := range []map[string]string{
		{}, // not made by Coldship
		{"coldship.app": "web", "coldship.db": "main"}, // web's database
		{"coldship.app": "other"},
	} {
		d, f := fakeDocker(t, map[string]map[string]string{"coldship-web-db-main": labels})
		err := d.Remove(ctx, "web-db-main", nil)
		if ae, ok := apierr.As(err); !ok || ae.Code != "name_in_use" {
			t.Fatalf("labels %v: want name_in_use, got %v", labels, err)
		}
		if len(f.removed) != 0 {
			t.Fatalf("labels %v: removed %v", labels, f.removed)
		}
	}

	d, f := fakeDocker(t, map[string]map[string]string{"coldship-web": {"coldship.app": "web"}})
	if err := d.Remove(ctx, "web", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 1 || f.removed[0] != "coldship-web" {
		t.Fatalf("own container not removed: %v", f.removed)
	}

	d, _ = fakeDocker(t, map[string]map[string]string{"coldship-web-db-main": {"coldship.app": "web"}})
	if err := d.RemoveDatabase(ctx, "web", "main"); err == nil {
		t.Fatal("an app container must not be removed as a database")
	}
}

func TestMissingContainerIsNotAnError(t *testing.T) {
	d, _ := fakeDocker(t, map[string]map[string]string{})
	if err := d.Remove(context.Background(), "web", nil); err != nil {
		t.Fatal(err)
	}
	if st, err := d.Status(context.Background(), "web"); err != nil || st.Exists {
		t.Fatalf("status %+v %v", st, err)
	}
}

func TestPingExplainsTheFix(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.sock")
	cli, _ := client.New(client.WithHost("unix://" + missing))
	if ae, ok := apierr.As((&Docker{cli: cli}).Ping(context.Background())); !ok || ae.Code != "docker_not_running" || ae.Hint == "" {
		t.Fatalf("no daemon: got %+v", ae)
	}

	if os.Geteuid() != 0 { // root can open any socket
		sock := filepath.Join(dir, "docker.sock")
		l, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		os.Chmod(sock, 0)
		cli, _ := client.New(client.WithHost("unix://" + sock))
		if ae, ok := apierr.As((&Docker{cli: cli}).Ping(context.Background())); !ok || ae.Code != "docker_permission_denied" || ae.Hint == "" {
			t.Fatalf("permission denied: got %+v", ae)
		}
	}

	d, _ := fakeDocker(t, nil)
	if err := d.Ping(context.Background()); err != nil {
		t.Fatalf("working daemon: %v", err)
	}
}

func TestParseMemory(t *testing.T) {
	for in, want := range map[string]int64{"512k": 512 << 10, "256m": 256 << 20, "2g": 2 << 30, "": 0, "5x": 0} {
		if got := parseMemory(in); got != want {
			t.Errorf("%q: got %d, want %d", in, got, want)
		}
	}
}

func TestNormalizeImage(t *testing.T) {
	for in, want := range map[string]string{
		"docker.io/library/postgres:17-alpine": "postgres:17-alpine",
		"postgres:17-alpine":                   "postgres:17-alpine",
		"docker.io/chrislusf/seaweedfs:4.51":   "chrislusf/seaweedfs:4.51",
		"ghcr.io/acme/app:1":                   "ghcr.io/acme/app:1",
	} {
		if got := normalizeImage(in); got != want {
			t.Errorf("%s: got %s", in, got)
		}
	}
}
