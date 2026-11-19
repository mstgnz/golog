.PHONY: build test test-coverage test-race test-integration test-db test-db-stop clean run run-cli docker-build docker-run vet fmt lint help

TEST_DB_PORT ?= 54329
TEST_DB_DSN  ?= postgres://postgres:postgres@127.0.0.1:$(TEST_DB_PORT)/golog_test?sslmode=disable

# Build targets
build:
	go build -o golog-server ./cmd
	go build -o golog-cli ./cmd/cli

# Test targets (unit tests; integration tests skip without GOLOG_TEST_DSN)
test:
	go test ./...

test-race:
	go test -race ./...

test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

# Integration tests against a throwaway Postgres: run `make test-db` first.
# -p 1: all packages share one NOTIFY channel, so they must not run at once.
test-integration:
	GOLOG_TEST_DSN='$(TEST_DB_DSN)' go test -p 1 -race -count=1 ./...

test-db:
	docker run -d --rm --name golog-test-db -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=golog_test \
		-p 127.0.0.1:$(TEST_DB_PORT):5432 postgres:16
	@until docker exec golog-test-db pg_isready -h 127.0.0.1 -U postgres -q; do sleep 1; done

test-db-stop:
	docker stop golog-test-db

# Code quality
vet:
	go vet ./...

fmt:
	gofmt -w .

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "Run 'make fmt' to fix formatting" && exit 1)

# Clean targets
clean:
	rm -f golog-server golog-cli coverage.out

# Run targets
run:
	go run ./cmd

run-cli:
	go run ./cmd/cli

# Docker targets
docker-build:
	docker build -t golog .

docker-run:
	docker compose up --build

# Help
help:
	@echo "Available targets:"
	@echo "  build            - Build the application"
	@echo "  test             - Run tests"
	@echo "  test-race        - Run tests with race detector"
	@echo "  test-coverage    - Run tests with coverage report"
	@echo "  test-db          - Start a throwaway Postgres for integration tests"
	@echo "  test-integration - Run all tests including integration (needs test-db)"
	@echo "  test-db-stop     - Stop the throwaway Postgres"
	@echo "  vet              - Run go vet"
	@echo "  fmt              - Format source code"
	@echo "  lint             - Run vet + format check"
	@echo "  clean            - Remove build artifacts"
	@echo "  run              - Run the web server"
	@echo "  run-cli          - Run the CLI tool"
	@echo "  docker-build     - Build Docker image"
	@echo "  docker-run       - Run with Docker Compose" 