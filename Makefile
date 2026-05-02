.PHONY: help build test integration lint tidy fmt vet run-api run-watcher run-orchestrator run-dispatcher run-dashboard run-recon up down logs migrate sqlc oapi demo recon clean

BIN_DIR := bin
BIN     := $(BIN_DIR)/selatpayd
PKG     := ./...
GOFLAGS := -trimpath
GOOSE_DRIVER := postgres
GOOSE_DBSTRING ?= $(shell sed -n 's/^SELATPAY_DB_URL=//p' .env 2>/dev/null || echo postgres://selatpay:selatpay@localhost:5432/selatpay?sslmode=disable)

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?##' Makefile | awk 'BEGIN{FS=":.*?##"} {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: ## compile selatpayd into bin/
	@mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -o $(BIN) ./cmd/selatpayd

test: ## fast unit tests
	go test -race -count=1 -short $(PKG)

integration: ## integration tests against compose services (postgres, redis, solana-test-validator)
	go test -race -count=1 -tags=integration $(PKG)

lint: ## golangci-lint
	@command -v golangci-lint >/dev/null || (echo "install: brew install golangci-lint"; exit 1)
	golangci-lint run

fmt: ## go fmt
	gofmt -l -w .

vet: ## go vet
	go vet $(PKG)

tidy: ## go mod tidy
	go mod tidy

up: ## bring up local stack (postgres, redis, jaeger, solana-test-validator, mock-bank)
	docker compose -f deploy/docker-compose.yaml up -d
	@echo "waiting for postgres..."
	@until docker compose -f deploy/docker-compose.yaml exec -T postgres pg_isready -U selatpay >/dev/null 2>&1; do sleep 1; done
	@$(MAKE) migrate
	@echo "stack ready -> jaeger http://localhost:16686  dashboard http://localhost:8081"

down: ## tear down local stack (keeps volumes)
	docker compose -f deploy/docker-compose.yaml down

logs: ## tail compose logs
	docker compose -f deploy/docker-compose.yaml logs -f --tail=100

migrate: ## apply DB migrations
	@command -v goose >/dev/null || (echo "install: go install github.com/pressly/goose/v3/cmd/goose@latest"; exit 1)
	GOOSE_DRIVER=$(GOOSE_DRIVER) GOOSE_DBSTRING="$(GOOSE_DBSTRING)" goose -dir internal/db/migrations up

sqlc: ## regenerate sqlc bindings
	@command -v sqlc >/dev/null || (echo "install: brew install sqlc"; exit 1)
	sqlc generate -f internal/db/sqlc.yaml

oapi: ## regenerate OpenAPI server stubs
	@command -v oapi-codegen >/dev/null || (echo "install: go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest"; exit 1)
	oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml

run-api:           build ; $(BIN) api
run-watcher:       build ; $(BIN) watcher
run-orchestrator:  build ; $(BIN) orchestrator
run-dispatcher:    build ; $(BIN) dispatcher
run-dashboard:     build ; $(BIN) dashboard
run-recon:         build ; $(BIN) recon

demo: build ## end-to-end happy-path against local devnet/test-validator
	./scripts/demo.sh

recon: build ## one-shot reconciliation report
	$(BIN) recon

clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html
