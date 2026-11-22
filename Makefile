.PHONY: up down logs test test-integration lint fmt build hooks

up:
	docker compose up --build

down:
	docker compose down -v

logs:
	docker compose logs -f api

test:
	go test -v -race ./internal/... ./migrations/... ./cmd/...

test-integration:
	go test -v -race -tags=integration ./tests/...

lint:
	docker run --rm -v "$(CURDIR):/app" -v golangci-lint-cache:/root/.cache -w /app golangci/golangci-lint:$(shell tr -d '[:space:]' < .golangci-version) golangci-lint run

fmt:
	gofmt -s -w .

build:
	go build -o bin/api ./cmd/api

hooks:
	git config core.hooksPath .githooks
