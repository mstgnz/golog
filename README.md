# GoLog

[![CI](https://github.com/mstgnz/golog/actions/workflows/ci.yml/badge.svg)](https://github.com/mstgnz/golog/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/mstgnz/golog)](https://goreportcard.com/report/github.com/mstgnz/golog)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Real-time log monitoring tool powered by PostgreSQL LISTEN/NOTIFY.

GoLog provides a lightweight, zero-dependency log pipeline: your application inserts a row into the `logs` table and every connected client (web dashboard or CLI) receives the entry instantly -- no polling, no message queue.

## Features

- **Real-time streaming** via PostgreSQL LISTEN/NOTIFY and Server-Sent Events (SSE)
- **Web dashboard** with live filtering by level and type
- **CLI tool** with colored output and `-level` / `-type` flags
- **REST API** for inserting and querying logs
- **Input validation** enforcing allowed levels and types
- **Docker Compose** setup for instant local development

## Architecture

```
Your app
  |
  | INSERT INTO logs (level, type, message)
  v
PostgreSQL  ──NOTIFY log_channel──>  GoLog server
                                          |
                          ┌───────────────┴───────────────┐
                          v                               v
                    Web dashboard                    CLI tool
                  (SSE /api/logs/stream)       (LISTEN goroutine)
```

The PostgreSQL trigger `log_notify_trigger` fires on every insert and publishes a JSON payload on `log_channel`. The GoLog server holds a persistent `pq.Listener` and fans the notification out to every connected SSE client.

## Getting started

### With Docker Compose (recommended)

```bash
git clone https://github.com/mstgnz/golog.git
cd golog
docker-compose up
```

Open http://localhost:8080 in your browser.

### Locally

**Prerequisites:** Go 1.22+, PostgreSQL 16+

```bash
git clone https://github.com/mstgnz/golog.git
cd golog

# Configure database connection
cp .env.example .env   # edit DB_* variables

# Initialize schema and sample data
psql -U postgres -d golog -f init.sql

# Build
make build

# Start the web server
./golog-server

# In a second terminal, start the CLI
./golog-cli
```

## CLI usage

```bash
./golog-cli                          # stream all logs
./golog-cli -level=ERROR             # filter by level
./golog-cli -type=DATABASE           # filter by type
./golog-cli -level=ERROR -type=AUTH  # combine filters
```

Supported levels: `INFO`, `WARNING`, `ERROR`, `DEBUG`

Supported types: `SYSTEM`, `AUTH`, `DATABASE`, `USER`, `API`

## API reference

### GET /api/logs

Returns the most recent 100 log entries, newest first.

**Query parameters** (all optional):

| Parameter | Description | Example |
|-----------|-------------|---------|
| `level` | Filter by log level | `level=ERROR` |
| `type` | Filter by log type | `type=DATABASE` |

**Response**

```json
[
  {
    "id": 42,
    "timestamp": "2024-01-15T10:30:00Z",
    "level": "ERROR",
    "type": "DATABASE",
    "message": "Connection timeout"
  }
]
```

### POST /api/logs

Insert a new log entry.

**Request body**

```json
{
  "level": "INFO",
  "type": "SYSTEM",
  "message": "Application started"
}
```

**Response**

```json
{ "id": 43 }
```

**Validation errors** return `400 Bad Request` with a plain-text description.

### GET /api/logs/stream

Server-Sent Events stream. Each event is a JSON-encoded log entry:

```
data: {"id":43,"timestamp":"2024-01-15T10:30:01Z","level":"INFO","type":"SYSTEM","message":"Application started"}

```

**Query parameters:** same `level` and `type` filters as `GET /api/logs`.

## Running tests

```bash
make test          # unit + mock tests
make test-race     # with race detector
make test-coverage # HTML coverage report
make lint          # vet + format check
```

## Development

```bash
make build         # build both binaries
make run           # go run web server
make run-cli       # go run CLI tool
make vet           # go vet ./...
make fmt           # gofmt -w .
make docker-build  # build Docker image
make docker-run    # docker-compose up
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on setting up the dev environment, code style, and the pull request workflow.

## Security

For security issues please see [SECURITY.md](SECURITY.md) rather than opening a public issue.

## License

MIT -- see [LICENSE](LICENSE).
