.PHONY: build test fmt install clean lint all

BINARY=bin/tws
BUILD_DIR=./cmd/tws
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

build:
	@mkdir -p bin
	go build $(LDFLAGS) -o $(BINARY) $(BUILD_DIR)

test:
	go test ./...

fmt:
	gofmt -w .

lint:
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...

install:
	go install $(LDFLAGS) $(BUILD_DIR)

clean:
	rm -rf bin/

all: fmt lint test build
