.PHONY: dev-api dev-web build build-web test test-go test-web test-hook e2e lint

dev-api:
	go run ./cmd/worktime

dev-web:
	cd web && npm run dev

build-web:
	cd web && VITE_WORKTIME_VERSION="$(VERSION)" npm run build

# The e2e fixture launches bin/worktime.exe on Windows, so the binary name must
# match the platform.
ifeq ($(OS),Windows_NT)
BIN := bin/worktime.exe
else
BIN := bin/worktime
endif

VERSION ?= dev
REVISION ?= $(shell git rev-parse --verify HEAD 2>/dev/null || echo unknown)
BUILT_AT ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PACKAGING ?= dev
BUILDINFO_PACKAGE := github.com/Siyet/worktime/internal/buildinfo
LDFLAGS := -s -w \
	-X $(BUILDINFO_PACKAGE).Version=$(VERSION) \
	-X $(BUILDINFO_PACKAGE).Revision=$(REVISION) \
	-X $(BUILDINFO_PACKAGE).BuiltAt=$(BUILT_AT)
LDFLAGS += -X $(BUILDINFO_PACKAGE).Packaging=$(PACKAGING)

build: build-web
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/worktime

# Everything that runs in under two seconds. The vitest suite covers the report
# arithmetic, the grouping and the time parsing - none of which go test can reach - and
# it used to be reachable only by typing the npm script by hand.
test: test-go test-web

test-go:
	go test ./...

test-web:
	cd web && npm run test:unit

# The Claude Code integration scripts are plain sh and are covered by neither
# go test nor Playwright.
test-hook:
	sh integrations/claude-code/wt-hook_test.sh
	sh integrations/claude-code/wt-statusline_test.sh

e2e: build
	cd web && npx playwright test

lint:
	go vet ./...
	cd web && npm run check
