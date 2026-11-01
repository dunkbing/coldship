.PHONY: build dashboard go test vet run website website-dev screenshots release clean

# The dashboard is built into internal/web/dist and embedded in the binary.
build: dashboard go

dashboard:
	cd dashboard && bun install --frozen-lockfile && bun run build

VERSION ?= 0.1.0-dev
# Base64 Ed25519 public key from `hangar release keygen`; binaries built
# without it never auto-upgrade (they can't verify releases).
UPDATE_PUBLIC_KEY ?=
# Where servers look for the signed manifest (e.g. https://releases.example.com/hangar/latest.json).
UPDATE_URL ?=
RELEASE_KEY ?= hangar-release.key
RELEASE_PLATFORMS ?= linux/amd64 linux/arm64
NOTES ?=
NOTES_URL ?=
LDFLAGS = -s -w -X main.version=$(VERSION) -X main.updatePublicKey=$(UPDATE_PUBLIC_KEY) -X main.defaultUpdateURL=$(UPDATE_URL)

go:
	go build -ldflags "$(LDFLAGS)" -o bin/hangar ./cmd/hangar

test:
	go test ./...

vet:
	go vet ./...

run: build
	./bin/hangar server

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
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build -trimpath -ldflags "$(LDFLAGS)" -o dist/hangar-$${p%/*}-$${p#*/} ./cmd/hangar || exit 1; \
	done
	./bin/hangar release manifest --version $(VERSION) --key-file $(RELEASE_KEY) --out dist/latest.json \
		--notes "$(NOTES)" --notes-url "$(NOTES_URL)" \
		$(foreach p,$(RELEASE_PLATFORMS),$(p)=dist/hangar-$(subst /,-,$(p)))

clean:
	rm -rf bin dist hangar.db* website/dist
	find internal/web/dist -mindepth 1 ! -name .gitkeep -delete
