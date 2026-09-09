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

# Release artifacts the installer downloads: static binaries plus the checksum
# file it verifies against.
VERSION ?= dev
PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# The pinned key is what stops a compromised gateway telling the fleet which
# weights to download. Built without it, -X sets the default to the empty string,
# the manifest silently disables itself, and every operator is left demanding a
# hand-written MODELS list — so this refuses rather than shipping that quietly.
.PHONY: release
release:
ifndef MANIFEST_PUBKEY
	$(error MANIFEST_PUBKEY is required: the base64 Ed25519 key the model catalog \
	is verified against. Building without it silently disables the manifest.)
endif
	@rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "-s -w -X github.com/blockreigntech/gnodi-ai-node/internal/config.DefaultManifestPubKey=$(MANIFEST_PUBKEY)" \
			-o dist/gnodi-ai-node_$${os}_$${arch} ./cmd/gnodi-ai-node; \
	done
	@cd dist && (command -v sha256sum >/dev/null && sha256sum gnodi-ai-node_* || shasum -a 256 gnodi-ai-node_*) > SHA256SUMS
	@echo "" && echo "dist/ (manifest key $(MANIFEST_PUBKEY)):" && ls -1 dist
