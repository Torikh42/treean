.PHONY: up down logs test test-integration lint install-lint fmt build hooks

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
	golangci-lint run ./...

install-lint:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(shell tr -d '[:space:]' < .golangci-version)

fmt:
	gofmt -s -w .

build:
	go build -o bin/api ./cmd/api

hooks:
	git config core.hooksPath .githooks
