.PHONY: build dashboard go test vet run website website-dev screenshots clean

# The dashboard is built into internal/web/dist and embedded in the binary.
build: dashboard go

dashboard:
	cd dashboard && bun install --frozen-lockfile && bun run build

go:
	go build -o bin/hangar ./cmd/hangar

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

clean:
	rm -rf bin hangar.db* website/dist
	find internal/web/dist -mindepth 1 ! -name .gitkeep -delete
