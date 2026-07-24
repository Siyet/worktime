.PHONY: dev-api dev-web build build-web test lint

dev-api:
	go run ./cmd/worktime

dev-web:
	cd web && npm run dev

build-web:
	cd web && npm run build

build: build-web
	go build -ldflags "-s -w" -o bin/worktime ./cmd/worktime

test:
	go test ./...

lint:
	go vet ./...
	cd web && npm run check
