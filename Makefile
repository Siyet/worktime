.PHONY: dev-api dev-web build build-web test e2e lint

dev-api:
	go run ./cmd/worktime

dev-web:
	cd web && npm run dev

build-web:
	cd web && npm run build

# The e2e fixture launches bin/worktime.exe on Windows, so the binary name must
# match the platform.
ifeq ($(OS),Windows_NT)
BIN := bin/worktime.exe
else
BIN := bin/worktime
endif

build: build-web
	go build -ldflags "-s -w" -o $(BIN) ./cmd/worktime

test:
	go test ./...

e2e: build
	cd web && npx playwright test

lint:
	go vet ./...
	cd web && npm run check
