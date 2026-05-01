.PHONY: build test fmt install clean lint all

BINARY=bin/tws
BUILD_DIR=./cmd/tws

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
