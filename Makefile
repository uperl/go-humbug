.PHONY: build test golden example licenses

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

# Regenerate THIRD_PARTY_LICENSES after a dependency change; review the diff.
licenses:
	python3 scripts/third_party_licenses.py
