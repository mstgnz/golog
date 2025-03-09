.PHONY: build test test-coverage clean run docker-build docker-run

# Build targets
build:
	go build -o golog-server cmd/main.go
	go build -o golog-cli cmd/cli/main.go

# Test targets
test:
	go test -v ./...

test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

# Integration test
test-integration:
	INTEGRATION_TEST=true go test -v ./cmd/...

# Clean targets
clean:
	rm -f golog-server golog-cli coverage.out

# Run targets
run:
	go run cmd/main.go

run-cli:
	go run cmd/cli/main.go

# Docker targets
docker-build:
	docker build -t golog .

docker-run:
	docker-compose up

# Help
help:
	@echo "Available targets:"
	@echo "  build           - Build the application"
	@echo "  test            - Run tests"
	@echo "  test-coverage   - Run tests with coverage report"
	@echo "  test-integration - Run integration tests"
	@echo "  clean           - Remove build artifacts"
	@echo "  run             - Run the web server"
	@echo "  run-cli         - Run the CLI tool"
	@echo "  docker-build    - Build Docker image"
	@echo "  docker-run      - Run with Docker Compose" 