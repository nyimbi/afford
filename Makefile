.PHONY: build test vet canary install fmt clean

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

install:
	go install ./cmd/afford

clean:
	rm -f bin/afford
