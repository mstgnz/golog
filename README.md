# GoLog

Real-time Log Monitoring Tool

GoLog is a lightweight log monitoring tool that uses PostgreSQL's LISTEN/NOTIFY feature to provide real-time log monitoring through both web and terminal interfaces.

## Features

- **Real-time Log Monitoring**: View logs as they are added to the database
- **Web Interface**: Modern web interface with filtering capabilities
- **Terminal Interface**: CLI tool for terminal-based log monitoring
- **Log Filtering**: Filter logs by level (INFO, WARNING, ERROR, DEBUG) and type (SYSTEM, AUTH, DATABASE, USER, API)
- **Docker Support**: Run the entire application stack in Docker

## Getting Started

### Prerequisites

- Go 1.22 or higher
- PostgreSQL 16 or higher
- Docker and Docker Compose (optional)

### Running with Docker

The easiest way to run GoLog is using Docker Compose:

```bash
# Clone the repository
git clone https://github.com/mstgnz/golog.git
cd golog

# Start the application
docker-compose up
```

The web interface will be available at http://localhost:8080

### Running Locally

To run the application locally:

1. Set up PostgreSQL and create a database
2. Create a `.env` file with your database configuration
3. Run the initialization SQL script
4. Build and run the application

```bash
# Clone the repository
git clone https://github.com/mstgnz/golog.git
cd golog

# Create .env file (edit as needed)
cp .env.example .env

# Initialize the database
psql -U postgres -d golog -f init.sql

# Build and run the web server
go build -o golog-server cmd/main.go
./golog-server

# In another terminal, run the CLI tool
go build -o golog-cli cmd/cli/main.go
./golog-cli
```

## CLI Usage

The CLI tool supports filtering logs by level and type:

```bash
# Show all logs
./golog-cli

# Filter by level
./golog-cli -level=ERROR

# Filter by type
./golog-cli -type=DATABASE

# Filter by both level and type
./golog-cli -level=ERROR -type=DATABASE
```

## API Endpoints

- `GET /api/logs`: Get logs with optional filtering
- `POST /api/logs`: Add a new log
- `GET /api/logs/stream`: Stream logs in real-time using Server-Sent Events (SSE)

## Running Tests

GoLog includes a comprehensive test suite. You can run the tests using the provided Makefile:

```bash
# Run all tests
make test

# Run tests with coverage report
make test-coverage

# Run integration tests (requires a running PostgreSQL instance)
make test-integration
```

The test suite includes:

- Unit tests for models
- Unit tests for configuration
- Mock-based tests for database operations
- Mock-based tests for HTTP handlers
- Integration tests for the main application

## Development

A Makefile is provided to make development easier:

```bash
# Build the application
make build

# Run the web server
make run

# Run the CLI tool
make run-cli

# Clean build artifacts
make clean

# Build Docker image
make docker-build

# Run with Docker Compose
make docker-run
```

## License

This project is licensed under the MIT License - see the LICENSE file for details.
