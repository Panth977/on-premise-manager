# opm-agent build. Go ≥ 1.26 is required by Caddy v2.11 (GOTOOLCHAIN=auto downloads it).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILT_AT := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X opm/agent/internal/agent.Version=$(VERSION) -X opm/agent/internal/agent.Commit=$(COMMIT) -X opm/agent/internal/agent.BuiltAt=$(BUILT_AT)
export GOTOOLCHAIN ?= auto
export CGO_ENABLED = 0
PLATFORMS := linux/amd64 linux/arm64 darwin/arm64

.PHONY: all build dist vet test test-short gui clean opm-export-wasm
all: vet test dist

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o dist/opm-agent ./cmd/opm-agent

dist:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; out=dist/opm-agent-$$os-$$arch; \
	  echo "build $$out"; \
	  GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/opm-agent || exit 1; \
	  shasum -a 256 $$out | cut -d' ' -f1 > $$out.sha256; \
	done
	@# §21 #91 build metadata (version, schema, minSchema, commit, builtAt) for agent/<v>/release.json
	@go build -trimpath -ldflags '$(LDFLAGS)' -o dist/.opm-agent-host ./cmd/opm-agent && dist/.opm-agent-host version --release-json > dist/release.json && rm -f dist/.opm-agent-host
	@echo "wrote dist/release.json"; cat dist/release.json
	@$(MAKE) --no-print-directory opm-export-wasm

# the compose export (GitHub mirror) as wasm for the cloud (§21 #106)
opm-export-wasm:
	@mkdir -p dist
	GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -ldflags '-s -w -buildid=' -o dist/opm-export.wasm ./cmd/opm-export
	@shasum -a 256 dist/opm-export.wasm | cut -d' ' -f1 > dist/opm-export.wasm.sha256

vet:
	go vet ./...

test:
	go test -count=1 -timeout 30m ./...

test-short:
	go test -count=1 -short ./...

# rebuild the embedded agent GUI (T4) into internal/webui/dist
gui:
	cd gui && npm ci && npm run build

clean:
	rm -rf dist
