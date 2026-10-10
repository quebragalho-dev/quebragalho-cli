# QG build/test/lint targets. AGENTS.md says to build and run quality checks
# with `make` — these targets back those instructions.
.DEFAULT_GOAL := build
GO_VERSION = $(word 2,$(shell git grep -G -h "^go[[:space:]]" -- go.mod))
GO_TOOLCHAIN = go$(GO_VERSION)
DEADCODE_VERSION := v0.46.0
GOLANGCI_LINT_VERSION := v2.12.2
GOVULNCHECK_VERSION := v1.3.0

.PHONY: build build-all release test test-race vet fmt fmt-check lint lint-static deadcode vulncheck vulncheck-windows tidy clean baseline help

# Build the main CLI binary into ./qg plus its real on-disk alias ./quebragalho
# (same bytes; the CLI is argv0-agnostic, so the alias needs no wrapper).
build:
	go build -o qg ./cmd/qg
	cp qg quebragalho

# Build every command in cmd/.
build-all:
	go build ./...

# Package release archives for every supported desktop/server target with
# SHA-256 checksums, then verify them all. Cross targets skip the executable
# smoke (they cannot run here); `qg-release verify` checks every archive digest.
release:
	rm -rf dist/release
	go run ./cmd/qg-release package --goos darwin --goarch arm64 --allow-cross
	go run ./cmd/qg-release package --goos darwin --goarch amd64 --allow-cross
	go run ./cmd/qg-release package --goos linux  --goarch amd64 --allow-cross
	go run ./cmd/qg-release package --goos linux  --goarch arm64 --allow-cross
	go run ./cmd/qg-release verify

# Run the full test suite with the race detector (matches CI expectations).
test:
	go test ./... -race -count=1

# Faster, no race detector.
test-quick:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w $(shell git ls-files '*.go')

# Fail if any tracked Go file is not gofmt-clean.
fmt-check:
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Lint = formatting check + vet (no extra tooling required).
lint: fmt-check vet

# Versioned tools select the toolchain from their own modules when invoked with
# package@version. Read this module's go directive directly, without invoking
# the possibly stale Go toolchain or consulting a multi-module GOWORK. git grep
# works with both POSIX shells and cmd.exe, including GNU Make 3.81 on macOS.
# The target-specific export is shell-independent.
lint-static deadcode vulncheck vulncheck-windows: export GOTOOLCHAIN = $(GO_TOOLCHAIN)
lint-static deadcode vulncheck vulncheck-windows: export GOWORK = off

lint-static:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --enable-only unused,ineffassign,staticcheck ./...

deadcode:
	go run golang.org/x/tools/cmd/deadcode@$(DEADCODE_VERSION) -test=false ./...

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

vulncheck-windows:
	mkdir -p "$(CURDIR)/.cache/gobin"
	GOBIN="$(CURDIR)/.cache/gobin" go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	GOOS=windows "$(CURDIR)/.cache/gobin/govulncheck" ./...

tidy:
	go mod tidy

clean:
	rm -f qg quebragalho
	go clean ./...

# Run the per-turn benchmark harness over the checked-in baseline manifest and
# write the JSON result to internal/perfbench/reports/baseline.json. Requires a
# built `qg` binary and a model; set QG_BENCH_MODEL (required) and
# QG_BENCH_BINARY (defaults to ./qg) to configure the run. The report is
# machine-specific and regenerated, not hand-edited.
baseline: build
	@if [ -z "$(QG_BENCH_MODEL)" ]; then echo "Set QG_BENCH_MODEL (and optionally QG_BENCH_BINARY) before running 'make baseline'"; exit 2; fi
	@QG_BIN="$${QG_BENCH_BINARY:-./qg}"; \
	go run ./cmd/qg-perf-bench turn \
		--suite internal/perfbench/manifests/baseline.json \
		--model $(QG_BENCH_MODEL) \
		--binary "$$QG_BIN" \
		--output internal/perfbench/reports/baseline.json

help:
	@echo "Targets: build (default; qg + quebragalho), build-all, release (cross archives + checksums), test, test-quick, vet, fmt, fmt-check, lint, lint-static, deadcode, vulncheck, vulncheck-windows, tidy, clean, baseline"
