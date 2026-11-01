.PHONY: build dashboard go test vet run website website-dev screenshots release clean

# The dashboard is built into internal/web/dist and embedded in the binary.
build: dashboard go

dashboard:
	cd dashboard && bun install --frozen-lockfile && bun run build

VERSION ?= 0.1.0-dev
# Public key that official releases are signed with (the private half is the
# COLDSHIP_RELEASE_KEY Actions secret). Builds trust releases signed with it.
UPDATE_PUBLIC_KEY ?= $(shell cat release/coldship-release.pub 2>/dev/null)
# Where servers look for new releases. Empty for local builds, so a dev build
# never replaces itself; the release workflow sets it to GitHub Releases.
UPDATE_URL ?=
RELEASE_KEY ?= coldship-release.key
RELEASE_PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
NOTES ?=
NOTES_URL ?=
LDFLAGS = -s -w -X main.version=$(VERSION) -X main.updatePublicKey=$(UPDATE_PUBLIC_KEY) -X main.defaultUpdateURL=$(UPDATE_URL)

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
screenshots:
	./scripts/screenshots.sh

# Cross-compile release binaries into dist/ and write a signed dist/latest.json.
# Upload dist/ as-is: the manifest refers to the binaries next to it.
release: dashboard go
	rm -rf dist && mkdir -p dist
	for p in $(RELEASE_PLATFORMS); do \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build -trimpath -ldflags "$(LDFLAGS)" -o dist/coldship-$${p%/*}-$${p#*/} ./cmd/coldship || exit 1; \
	done
	./bin/coldship release manifest --version $(VERSION) --key-file $(RELEASE_KEY) --out dist/latest.json \
		--notes "$(NOTES)" --notes-url "$(NOTES_URL)" \
		$(foreach p,$(RELEASE_PLATFORMS),$(p)=dist/coldship-$(subst /,-,$(p)))
	cd dist && sha256sum coldship-* latest.json > SHA256SUMS

clean:
	rm -rf bin dist coldship.db* website/dist
	find internal/web/dist -mindepth 1 ! -name .gitkeep -delete
