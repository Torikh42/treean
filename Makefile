.PHONY: up down logs test test-integration lint fmt build

up:
	docker compose up --build

down:
	docker compose down -v

logs:
	docker compose logs -f api

test:
	go test -v -race ./internal/...

test-integration:
	go test -v -race -tags=integration ./tests/...

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .

build:
	go build -o bin/api ./cmd/api
