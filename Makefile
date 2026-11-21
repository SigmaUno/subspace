.PHONY: all build test vet lint fmt run loadtest loadtest-short loadtest-1000 loadtest-report release-dist release-dry clean
.PHONY: all build test vet lint fmt gen-ts run loadtest loadtest-short loadtest-1000 loadtest-report clean
.PHONY: all build test test-full vet lint fmt run seed demo loadtest loadtest-short loadtest-1000 loadtest-report clean
.PHONY: all build test vet lint fmt run loadtest loadtest-short loadtest-1000 loadtest-3000 loadtest-report loadtest-scenarios loadtest-contention loadtest-big-plan loadtest-many-briefs loadtest-thread-heavy loadtest-reconnect-storm loadtest-restart clean

.PHONY: sync

GO ?= go
BIN := bin
LDFLAGS := -s -w
LOADTEST_OUT ?= $(BIN)/loadtest-1000.json
LOADTEST_PROFILE ?= $(BIN)/loadtest-1000-profile
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DIST := dist
RELEASE_CMDS := subspace subspacectl subspace-join
RELEASE_ARCHES := amd64 arm64
RELEASE_LDFLAGS := -s -w -X github.com/SigmaUno/subspace/internal/version.Version=$(VERSION) -X github.com/SigmaUno/subspace/internal/board.Version=$(VERSION)

all: vet lint test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/subspace ./cmd/subspace
	@if [ -d cmd/loadtest ]; then CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/loadtest ./cmd/loadtest; fi

test:
	$(GO) test -race -count=1 -gcflags=all=-d=checkptr=0 ./...

test-full:
	SUBSPACE_FULL=1 $(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

gen-ts:
	$(GO) run ./cmd/gen-ts -o web/lib/generated-types.ts

sync:
	./scripts/sync.sh

run: build
	SUBSPACE_OWNER_TOKEN=$${SUBSPACE_OWNER_TOKEN:-dev-owner-token} SUBSPACE_DB=$${SUBSPACE_DB:-./data/subspace.db} SUBSPACE_ADDR=$${SUBSPACE_ADDR:-:8080} $(BIN)/subspace

SEED ?= 1
SEED_URL ?= http://localhost:8080
DEMO_FLAGS ?=

seed:
	$(GO) run ./cmd/subspace-seed -url $(SEED_URL) -seed $(SEED) $(DEMO_FLAGS)

demo:
	$(GO) run ./cmd/subspace-seed -demo -seed $(SEED) $(DEMO_FLAGS)

# 300 simulated agents against an in-process server over real HTTP MCP.
loadtest:
	$(GO) run ./cmd/loadtest -agents 300 -duration 60s

# Short version used in CI.
loadtest-short:
	$(GO) run ./cmd/loadtest -agents 50 -duration 20s -max-error-rate 0

# 1000 agents ramped in over 20s, 500 SSE subscribers; JSON report and pprof profiles under bin/.
loadtest-1000:
	$(GO) run ./cmd/loadtest -agents 1000 -duration 90s -ramp 20s -sse-subs 500 -out $(LOADTEST_OUT) -profile $(LOADTEST_PROFILE)

# 3000 agents ramped in over 40s, 1000 SSE subscribers; JSON report and pprof profiles under bin/.
loadtest-3000:
	$(GO) run ./cmd/loadtest -agents 3000 -duration 120s -ramp 40s -sse-subs 1000 -out $(BIN)/loadtest-3000.json -profile $(BIN)/loadtest-3000-profile

loadtest-scenarios: loadtest-contention loadtest-big-plan loadtest-many-briefs loadtest-thread-heavy loadtest-reconnect-storm loadtest-restart

loadtest-contention:
	$(GO) run ./cmd/loadtest -scenario contention -agents 200 -sse-subs 0 -duration 60s

loadtest-big-plan:
	$(GO) run ./cmd/loadtest -scenario big-plan -agents 50 -sse-subs 0 -duration 120s

loadtest-many-briefs:
	$(GO) run ./cmd/loadtest -scenario many-briefs -agents 50 -sse-subs 0 -duration 120s

loadtest-thread-heavy:
	$(GO) run ./cmd/loadtest -scenario thread-heavy -agents 20 -messages 500 -sse-subs 0 -duration 60s

loadtest-reconnect-storm:
	$(GO) run ./cmd/loadtest -scenario reconnect-storm -agents 50 -sse-subs 500 -duration 20s

loadtest-restart:
	$(GO) run ./cmd/loadtest -scenario restart -agents 50 -tasks-per-agent 6 -sse-subs 100 -duration 60s

loadtest-report:
	@test -f $(LOADTEST_OUT) || { echo "$(LOADTEST_OUT) not found; run make loadtest-1000 first" >&2; exit 1; }
	@cat $(LOADTEST_OUT)

release-dist:
	$(RM) -r $(DIST)
	mkdir -p $(DIST)
	for cmd in $(RELEASE_CMDS); do \
		for arch in $(RELEASE_ARCHES); do \
			CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "$(RELEASE_LDFLAGS)" -o $(DIST)/$$cmd-linux-$$arch ./cmd/$$cmd || exit 1; \
		done; \
	done
	cd $(DIST) && sha256sum *-linux-* > SHA256SUMS

release-dry: release-dist
	scripts/changelog.sh HEAD > $(DIST)/CHANGELOG.md
	@if [ -n "$(RELEASE_IMAGES)" ]; then \
		docker build --target server -t subspace-server:$(VERSION) . && docker build -t subspace-web:$(VERSION) web; \
	fi

clean:
	$(GO) clean -cache -testcache
	$(RM) -r $(BIN)
