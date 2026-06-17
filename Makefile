BINARY := gnodi-ai-node
PKG := ./cmd/gnodi-ai-node
VERSION ?= dev
LDFLAGS := -s -w

.PHONY: build test vet build-linux clean run

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 $(PKG)
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 $(PKG)

run: build
	./bin/$(BINARY)

clean:
	rm -rf bin
