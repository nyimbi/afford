.PHONY: build test vet canary mcp-smoke ci install fmt clean

build:
	go build -o bin/afford ./cmd/afford

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal

canary: build
	./scripts/canary.sh

mcp-smoke: build
	./scripts/mcp_smoke.sh

ci:
	llocal-ci --tier default

install:
	go install ./cmd/afford

clean:
	rm -f bin/afford
