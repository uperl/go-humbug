.PHONY: build test golden example

build:
	go build -o bin/humbug ./cmd/humbug

test:
	go vet ./...
	go test ./...

# Rewrite testdata/golden after an intentional output change; review the diff.
golden:
	go test ./internal/protocol -update

example: build
	python3 examples/python/client.py bin/humbug
