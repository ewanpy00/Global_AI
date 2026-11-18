# Loads .env if present, so `make run` picks up API keys.
-include .env
export

.PHONY: run dev-api dev-web build test web-install models

web-install:
	cd web && npm install --no-audit --no-fund

## Build the UI and serve everything from the Go binary on :8080
run: build
	cd backend && FRESHGATE_WEB_DIR=../web/dist ./bin/freshgate

build: web/node_modules
	cd web && npm run build
	cd backend && go build -o bin/freshgate ./cmd/freshgate

web/node_modules:
	cd web && npm install --no-audit --no-fund

## Development: run these two in separate terminals, open http://localhost:5173
dev-api:
	cd backend && go run ./cmd/freshgate

dev-web: web/node_modules
	cd web && npm run dev

test:
	cd backend && go test -race ./...
	cd web && npm run typecheck

## List the model ids each configured provider offers (to pick FRESHGATE_*_MODEL)
models:
	cd backend && go run ./cmd/freshgate -list-models
