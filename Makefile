BIN      ?= holectl
PREFIX   ?= $(HOME)/bin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: build test lint fmt check install skill clean snapshot

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

test:
	go test -race ./...

fmt:
	gofmt -s -w .

# golangci-lint when installed (uses .golangci.yml); go vet and gofmt always.
lint:
	go vet ./...
	@test -z "$$(gofmt -s -l .)" || { gofmt -s -l .; echo "run 'make fmt'"; exit 1; }
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; fi

check: lint build test

install: check
	install -d $(PREFIX)
	install -m 0755 $(BIN) $(PREFIX)/$(BIN)
	@$(PREFIX)/$(BIN) version -o json

# Install or update the agent skill in ~/.claude/skills/holectl/SKILL.md.
skill: build
	./$(BIN) skill install

clean:
	rm -f $(BIN)

# Build every release archive into dist/ without publishing (needs goreleaser).
snapshot:
	goreleaser release --snapshot --clean
