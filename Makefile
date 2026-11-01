.PHONY: build test vet run clean

build:
	go build -o bin/hangar ./cmd/hangar

test:
	go test ./...

vet:
	go vet ./...

run: build
	./bin/hangar server

clean:
	rm -rf bin hangar.db*
