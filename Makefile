GO ?= go
BINARY ?= bin/mig-est
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/eterny13/mig-est/internal/version.Version=$(VERSION) \
	-X github.com/eterny13/mig-est/internal/version.Commit=$(COMMIT) \
	-X github.com/eterny13/mig-est/internal/version.Date=$(DATE)

.PHONY: all build test vet fmt fmt-check lint smoke install clean

all: fmt-check vet test build

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/mig-est

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

lint: fmt-check vet

smoke: build
	./$(BINARY) check --offline \
		--file ./testdata/migrations/03_copy_charset.sql \
		--table-size orders=8200MB --table-rows orders=12400000 \
		--disk-free 50GB --fail-on critical --no-color

install:
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' ./cmd/mig-est

clean:
	rm -rf bin dist coverage.out
