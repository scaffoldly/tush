BINARY  := tush
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build run test lint clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

run:
	go run .

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY)
