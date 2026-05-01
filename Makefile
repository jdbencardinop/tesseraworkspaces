.PHONY: build test fmt install clean lint all

BINARY=bin/ts
BUILD_DIR=./cmd/ts

build:
	@mkdir -p bin
	go build -o $(BINARY) $(BUILD_DIR)

test:
	go test ./...

fmt:
	gofmt -w .

lint:
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...

install:
	go install $(BUILD_DIR)

clean:
	rm -rf bin/

all: fmt lint test build
