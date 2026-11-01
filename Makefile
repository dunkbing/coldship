.PHONY: build dashboard go test vet run website website-dev docker-smoke screenshots release clean

# The dashboard is built into internal/web/dist and embedded in the binary.
build: dashboard go

dashboard:
	cd dashboard && bun install --frozen-lockfile && bun run build

VERSION ?= 0.1.0-dev
# Where servers look for new releases. Empty for local builds, so a dev build
# never replaces itself; the release workflow sets it to GitHub Releases.
UPDATE_URL ?=
RELEASE_PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
# Release notes come from the tag message. They are passed to the recipe
# through the environment, never expanded into it, so no tag or commit text
# ever runs as shell.
NOTES ?=
NOTES_URL ?=
export NOTES NOTES_URL
LDFLAGS = -s -w -X main.version=$(VERSION) -X main.defaultUpdateURL=$(UPDATE_URL)

go:
	go build -ldflags "$(LDFLAGS)" -o bin/coldship ./cmd/coldship

test:
	go test ./...

vet:
	go vet ./...

run: build
	./bin/coldship server

website:
	cd website && bun install --frozen-lockfile && bun run build

website-dev:
	cd website && bun run dev

# Regenerate the docs screenshots (needs Docker).
# End-to-end deploys against real Docker.
docker-smoke: build
	./scripts/docker-smoke.sh

screenshots:
	./scripts/screenshots.sh

# Cross-compile release binaries into dist/, with latest.json and SHA256SUMS.
# Upload dist/ as-is: the manifest refers to the binaries next to it.
release: dashboard go
	rm -rf dist && mkdir -p dist
	for p in $(RELEASE_PLATFORMS); do \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build -trimpath -ldflags "$(LDFLAGS)" -o dist/coldship-$${p%/*}-$${p#*/} ./cmd/coldship || exit 1; \
	done
	./bin/coldship release manifest --version $(VERSION) --out dist/latest.json \
		--notes "$$NOTES" --notes-url "$$NOTES_URL" \
		$(foreach p,$(RELEASE_PLATFORMS),$(p)=dist/coldship-$(subst /,-,$(p)))
	cd dist && sha256sum coldship-* latest.json > SHA256SUMS

clean:
	rm -rf bin dist coldship.db* website/dist
	find internal/web/dist -mindepth 1 ! -name .gitkeep -delete
