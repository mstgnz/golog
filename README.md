# GoLog

[![CI](https://github.com/mstgnz/golog/actions/workflows/ci.yml/badge.svg)](https://github.com/mstgnz/golog/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/mstgnz/golog)](https://goreportcard.com/report/github.com/mstgnz/golog)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Real-time log monitoring tool powered by PostgreSQL LISTEN/NOTIFY.

GoLog provides a lightweight, zero-dependency log pipeline: your application inserts a row into the `logs` table and every connected client (web dashboard or CLI) receives the entry instantly -- no polling, no message queue.

## Features

- **Real-time streaming** via PostgreSQL LISTEN/NOTIFY and Server-Sent Events (SSE)
- **Web dashboard** with live filtering by level and type, compiled into the binary
- **CLI tool** with colored output and `-level` / `-type` flags
- **REST API** for inserting and querying logs
- **Automatic schema setup**: the server applies its own migrations on start
- **Single static binary** and a small non-root Docker image
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

The PostgreSQL trigger `log_notify_trigger` fires on every insert and publishes the new row's id on `log_channel`. The GoLog server holds a persistent `pq.Listener`, reads the row once and fans it out to every connected SSE client. Sending only the id keeps messages of any size working: PostgreSQL caps a NOTIFY payload at 8000 bytes.

If the listener loses its connection, it reconnects by itself and tells clients to `resync`, because entries written in between were not streamed.

## Getting started

### With Docker Compose (recommended)

```bash
git clone https://github.com/mstgnz/golog.git
cd golog
docker compose up --build
```

Open http://localhost:8080 in your browser. The compose file publishes Postgres on port 5432 and GoLog on 8080; change them there if those ports are taken.

### Locally

**Prerequisites:** Go 1.22+, PostgreSQL 11+ (CI tests against 16)

```bash
git clone https://github.com/mstgnz/golog.git
cd golog

# Configure database connection
cp .env.example .env   # edit DATABASE_URL or the DB_* variables

# Build
make build

# Start the web server; it creates the schema on first start
./golog-server

# In a second terminal, start the CLI
./golog-cli
```

## Configuration

Environment variables, or a `.env` file in the working directory (real environment variables win).

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | | Full connection URL. When set, the `DB_*` variables are ignored. |
| `DB_HOST` / `DB_PORT` | `localhost` / `5432` | |
| `DB_USER` / `DB_PASSWORD` | `postgres` / `postgres` | Special characters in the password are fine. |
| `DB_NAME` | `golog` | |
| `DB_SSLMODE` | `disable` | `disable`, `require`, `verify-ca` or `verify-full`. Managed databases usually need `require`. |
| `PORT` | `8080` | HTTP port of `golog-server`. |
| `CORS_ALLOWED_ORIGINS` | | Comma-separated origins allowed to call the API from a browser. Empty means no CORS headers; the bundled dashboard does not need them. |
| `GOLOG_AUTO_MIGRATE` | `true` | Apply schema migrations on server start. Set `false` if you manage the schema yourself. |

## Schema and migrations

`golog-server` applies the SQL files in [`database/migrations`](database/migrations) on start and records them in `golog_schema_migrations`. Several instances can start at once; a PostgreSQL advisory lock lets one of them migrate. The index on `logs` is built with `CREATE INDEX CONCURRENTLY`, so upgrading a large table does not block your application's inserts.

The CLI never changes the schema. Start the server once before using the CLI on a new database.

### Upgrading from 1.x

Point the 2.0 server at the existing database and start it. The `logs` table and its rows are kept; the trigger is replaced so that NOTIFY carries only the id. `init.sql` no longer exists and is not needed.

## CLI usage

```bash
./golog-cli                          # stream all logs
./golog-cli -level=ERROR             # filter by level
./golog-cli -type=DATABASE           # filter by type
./golog-cli -level=error -type=auth  # case does not matter
```

Supported levels: `INFO`, `WARNING`, `ERROR`, `DEBUG`

Supported types: `SYSTEM`, `AUTH`, `DATABASE`, `USER`, `API`

The CLI connects to PostgreSQL directly with the same configuration as the server. Control characters in log messages are replaced before printing, so a log line cannot rewrite your terminal.

## API reference

### GET /api/logs

Returns log entries, newest first.

**Query parameters** (all optional):

| Parameter | Description | Example |
|-----------|-------------|---------|
| `level` | Filter by log level | `level=ERROR` |
| `type` | Filter by log type | `type=DATABASE` |
| `limit` | Page size, default 100, max 500 | `limit=50` |
| `offset` | Entries to skip, default 0 | `offset=100` |

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

`message` may be up to 10000 bytes. **Validation errors** return `400 Bad Request` with a plain-text description; a body over 64 KiB returns `413`. Server errors return a generic `500` and the cause is written to the server log.

### GET /api/logs/stream

Server-Sent Events stream. Each event is a JSON-encoded log entry:

```
data: {"id":43,"timestamp":"2024-01-15T10:30:01Z","level":"INFO","type":"SYSTEM","message":"Application started"}

```

The stream also sends:

- `event: resync` when entries may have been missed (the server's database listener reconnected). Reload `GET /api/logs`.
- `: ping` comments every 30 seconds, so proxies do not close an idle stream.

**Query parameters:** same `level` and `type` filters as `GET /api/logs`.

## Running tests

```bash
make test             # unit tests; integration tests are skipped
make test-race        # with race detector
make test-coverage    # HTML coverage report
make lint             # vet + format check

make test-db          # start a throwaway Postgres in Docker (port 54329)
make test-integration # everything, including tests against that Postgres
make test-db-stop
```

Integration tests read `GOLOG_TEST_DSN` (a `postgres://` URL). Each test works in its own schema and drops it afterwards. Point it only at a database you can throw away.

## Development

```bash
make build         # build both binaries
make run           # go run web server
make run-cli       # go run CLI tool
make vet           # go vet ./...
make fmt           # gofmt -w .
make docker-build  # build Docker image
make docker-run    # docker compose up --build
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on setting up the dev environment, code style, and the pull request workflow.

## Security

GoLog has no authentication yet. Run it on a trusted network or behind an authenticating proxy; anyone who can reach the API can read and write logs. Details and how to report a vulnerability: [SECURITY.md](SECURITY.md).

## License

MIT -- see [LICENSE](LICENSE).
