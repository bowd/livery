# Livery (`liv`) — Go static CLI + multi-host agent configuration controller.
GO ?= go
BIN := build/liv
INSTALL_DIR ?= $(HOME)/.local/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X github.com/bowd/livery/internal/version.Version=$(VERSION) -X github.com/bowd/livery/internal/version.Commit=$(COMMIT) -X github.com/bowd/livery/internal/version.Date=$(DATE)

# Remote Linux hosts may lack en_US.UTF-8; force C.UTF-8 so locale warnings never break rsync or JSON stdio.
export LC_ALL = C.UTF-8
export LANG = C.UTF-8
export CGO_ENABLED = 0

T ?=
ARGS = $(if $(T),--target $(T),)

.PHONY: build install dist test check plan sync matrix doctor audit ui onboard discover herdr-install secrets-status secrets-sync secrets-import-t3

build:
	@mkdir -p build
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/liv

install: build
	@mkdir -p $(INSTALL_DIR)
	cp $(BIN) $(INSTALL_DIR)/.liv.tmp
	chmod 755 $(INSTALL_DIR)/.liv.tmp
	mv -f $(INSTALL_DIR)/.liv.tmp $(INSTALL_DIR)/liv
	@echo "Installed $(INSTALL_DIR)/liv"

dist: build
	@mkdir -p dist
	@for pair in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do \
	  os=$${pair%/*}; arch=$${pair#*/}; \
	  echo "Building livery $(VERSION) ($${os}/$${arch})..."; \
	  mkdir -p dist/stage; \
	  CGO_ENABLED=0 GOOS=$${os} GOARCH=$${arch} $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/stage/liv ./cmd/liv; \
	  tar -czf dist/livery_$(VERSION)_$${os}_$${arch}.tar.gz -C dist/stage liv; \
	  rm -rf dist/stage; \
	done
	./$(BIN) herdr export-mod --out dist/livery-launcher-mod_$(VERSION).tar.gz
	@(cd dist && (sha256sum *.tar.gz 2>/dev/null || shasum -a 256 *.tar.gz) > checksums.txt)

onboard: build
	./$(BIN) onboard

test:
	$(GO) test -v ./...

check: build
	./$(BIN) check --repo testdata/demo-repo
	$(GO) test ./...

plan: build
	./$(BIN) plan $(ARGS)

sync: build
	./$(BIN) sync $(ARGS)

matrix: build
	./$(BIN) matrix

doctor: build
	./$(BIN) doctor $(ARGS)

audit: build
	./$(BIN) audit $(ARGS)

ui: build
	./$(BIN) ui $(UI_ARGS)

discover: build
	./$(BIN) env discover

herdr-install: build
	./$(BIN) herdr install-mod

secrets-status: build
	./$(BIN) secrets status $(ARGS)

secrets-sync: build
	./$(BIN) secrets sync $(ARGS)

secrets-import-t3: build
	./$(BIN) secrets import-t3 $(ARGS)
