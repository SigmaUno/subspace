.PHONY: all build test vet lint fmt run loadtest loadtest-short clean

GO ?= go
BIN := bin
LDFLAGS := -s -w

all: vet lint test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/subspace ./cmd/subspace
	@if [ -d cmd/loadtest ]; then CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/loadtest ./cmd/loadtest; fi

test:
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

run: build
	SUBSPACE_OWNER_TOKEN=$${SUBSPACE_OWNER_TOKEN:-dev-owner-token} SUBSPACE_DB=$${SUBSPACE_DB:-./data/subspace.db} SUBSPACE_ADDR=$${SUBSPACE_ADDR:-:8080} $(BIN)/subspace

# 300 simulated agents against an in-process server over real HTTP MCP.
loadtest:
	$(GO) run ./cmd/loadtest -agents 300 -duration 60s

# Short version used in CI.
loadtest-short:
	$(GO) run ./cmd/loadtest -agents 50 -duration 20s -max-error-rate 0

clean:
	$(GO) clean -cache -testcache
	$(RM) -r $(BIN)
