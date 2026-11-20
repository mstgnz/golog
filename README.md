# GoLog

[![CI](https://github.com/mstgnz/golog/actions/workflows/ci.yml/badge.svg)](https://github.com/mstgnz/golog/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/mstgnz/golog)](https://goreportcard.com/report/github.com/mstgnz/golog)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Real-time log monitoring tool powered by PostgreSQL LISTEN/NOTIFY.

GoLog provides a lightweight, zero-dependency log pipeline: your application inserts a row into the `logs` table and every connected client (web dashboard or CLI) receives the entry instantly -- no polling, no message queue.

## Features

- **Real-time streaming** via PostgreSQL LISTEN/NOTIFY and Server-Sent Events (SSE)
- **Your own fields**: free-form `type` and `service`, plus a JSON `attrs` object per entry (`user_id`, `trace_id`, ...)
- **Search**: filter by level, type, service, message text and attribute values; the list and the live stream use the same rules
- **Go `log/slog` handler** ([`gologslog`](gologslog)): one line of setup, batched in the background, never blocks your app
- **Read and write tokens**: apps can send logs without being able to read them
- **Retention**: optionally delete logs older than N days
- **Web dashboard** with live filters, compiled into the binary
- **CLI tool** that reads the database directly or a remote server over HTTP
- **Automatic schema setup**: the server applies its own migrations on start
- **Single static binary** and a small non-root Docker image

## Architecture

```
Go app  ── gologslog: POST /api/logs/batch ──┐
any app ── POST /api/logs ───────────────────┤──> GoLog server ── INSERT ──┐
any app ── INSERT INTO logs ─────────────────┼─────────────────────────────┤
                                                                           v
                                                                      PostgreSQL
                                                                           │ NOTIFY log_channel
                                                                           v
                              Web dashboard <── SSE /api/logs/stream ── GoLog server
                              CLI           <── API, or reads PostgreSQL directly
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

Open http://localhost:8080 in your browser. The compose file publishes Postgres on port 5432 and GoLog on 8080, both on `127.0.0.1` only. Set the tokens (see [Authentication](#authentication)) before you publish GoLog beyond your machine.

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
| `GOLOG_READ_TOKEN` | | Token for reading: list, stream, facets, dashboard, CLI. Empty means anyone can read. At least 16 characters. |
| `GOLOG_WRITE_TOKEN` | | Token for `POST /api/logs` and `/api/logs/batch`. Empty means anyone can write. At least 16 characters, different from the read token. |
| `GOLOG_SECURE_COOKIES` | `false` | Mark the dashboard cookie `Secure` even when TLS ends at a proxy in front of GoLog. |
| `GOLOG_RETENTION_DAYS` | `0` | Delete logs older than this many days, checked every hour. `0` keeps everything. **Deleted logs cannot be recovered.** |

`DB_HOST` may also be a unix socket directory, such as `/var/run/postgresql`.

## Authentication

Set two tokens, for example with `openssl rand -hex 24`:

- **`GOLOG_WRITE_TOKEN`** goes to your applications. It can send logs and nothing else.
- **`GOLOG_READ_TOKEN`** goes to people. It opens the dashboard, the CLI and the read API.

API clients send `Authorization: Bearer <token>`. The dashboard asks for the read token once and keeps an `HttpOnly`, `SameSite=Strict` session cookie for 7 days; changing the read token signs everyone out. The dashboard's **Add Log** button only appears when writes need no token, so the write token never reaches a browser.

Without tokens GoLog is open and says so in its startup log. Applications that `INSERT` into PostgreSQL directly do not go through GoLog's auth at all.

## Sending logs

**From Go**, with the `log/slog` handler:

```go
import "github.com/mstgnz/golog/v2/gologslog"

h, err := gologslog.New(gologslog.Options{
    URL:     "https://logs.example.com",
    Token:   os.Getenv("GOLOG_WRITE_TOKEN"),
    Service: "billing",
})
if err != nil {
    log.Fatal(err)
}
defer h.Close(context.Background()) // sends what is still buffered

logger := slog.New(h)
logger.Error("card declined", "user_id", 42, "order", 7)
```

Records are sent in batches (every second or every 100 records). A log call never waits on the network: if GoLog is slow or down, records are retried and then dropped, and `h.Dropped()` tells you how many. The package uses only the standard library.

**Over HTTP**, see `POST /api/logs` and `POST /api/logs/batch` below.

**With SQL**, insert into the table yourself; the dashboard and CLI still see it live:

```sql
INSERT INTO logs (level, type, service, message, attrs)
VALUES ('ERROR', 'PAYMENT', 'billing', 'card declined', '{"user_id": 42}');
```

## Schema and migrations

`golog-server` applies the SQL files in [`database/migrations`](database/migrations) on start and records them in `golog_schema_migrations`. Several instances can start at once; a PostgreSQL advisory lock lets one of them migrate. Indexes are built with `CREATE INDEX CONCURRENTLY`, so upgrading a large table does not block your application's inserts.

If a start fails in such an index migration (the process was killed, or the build failed), drop the half-built index and start again. The error names the migration; the index name is in that file:

```sql
DROP INDEX CONCURRENTLY IF EXISTS logs_attrs_idx;
```

The CLI never changes the schema. Start the server once before using the CLI on a new database.

### Upgrading

- **From 2.0:** start the 2.1 server. It adds the `service` and `attrs` columns (no table rewrite) and a GIN index on `attrs`, built concurrently. Existing rows read as `service: ""` and `attrs: {}`.
- **From 1.x:** start the server the same way. The `logs` table and its rows are kept; the trigger is replaced so that NOTIFY carries only the id. `init.sql` no longer exists and is not needed.

## CLI usage

```bash
./golog-cli                                # stream all logs
./golog-cli -level=error                   # level, any case
./golog-cli -type=PAYMENT -service=billing # exact type and service
./golog-cli -q="card declined"             # text in the message, any case
./golog-cli -attr user_id=42 -attr env=prod
```

By default the CLI reads PostgreSQL directly with the same configuration as the server. To read a remote server instead, give its URL and the read token:

```bash
GOLOG_READ_TOKEN=... ./golog-cli -url=https://logs.example.com -level=ERROR
```

The token is read from the environment only, so it does not end up in your shell history. Control characters in log messages are replaced before printing, so a log line cannot rewrite your terminal.

## API reference

With tokens set, every endpoint below except `/api/session` needs `Authorization: Bearer <token>`: the read token for `GET`, the write token for `POST /api/logs*`.

### GET /api/logs

Returns log entries, newest first.

**Query parameters** (all optional):

| Parameter | Description | Example |
|-----------|-------------|---------|
| `level` | Exact level | `level=ERROR` |
| `type` | Exact type (case-sensitive) | `type=PAYMENT` |
| `service` | Exact service | `service=billing` |
| `q` | Message contains this text, any case | `q=timeout` |
| `attr.<key>` | Attribute equals the value. `attr.user_id=42` matches both `42` and `"42"`; up to 5 | `attr.trace_id=ab12` |
| `limit` | Page size, default 100, max 500 | `limit=50` |
| `offset` | Entries to skip, default 0 | `offset=100` |

`q` scans the matching rows, so on a very large table combine it with other filters. `attr.*` uses a GIN index.

**Response**

```json
[
  {
    "id": 42,
    "timestamp": "2026-01-15T10:30:00Z",
    "level": "ERROR",
    "type": "PAYMENT",
    "service": "billing",
    "message": "card declined",
    "attrs": {"user_id": 42}
  }
]
```

### POST /api/logs

Insert one entry.

```json
{
  "level": "INFO",
  "type": "SYSTEM",
  "service": "billing",
  "message": "Application started",
  "attrs": {"version": "2.1.0"},
  "timestamp": "2026-01-15T10:30:00Z"
}
```

Response: `{ "id": 43 }`

| Field | Rules |
|-------|-------|
| `level` | `DEBUG`, `INFO`, `WARNING` or `ERROR` |
| `type` | required, 1-50 characters, no control characters |
| `service` | optional, up to 100 characters, no control characters |
| `message` | required, up to 10000 characters, no NUL |
| `attrs` | optional JSON object, up to 16 KiB; numbers at most 100 characters with an exponent within ±1000 (what PostgreSQL can store) |
| `timestamp` | optional; the server's time when missing; at most one hour in the future |

**Validation errors** return `400 Bad Request` with a plain-text description; a body over 128 KiB returns `413`. Server errors return a generic `500` and the cause is written to the server log.

### POST /api/logs/batch

A JSON array of 1-100 entries in the format above, stored all or nothing in one statement. Response: `{ "ids": [44, 45] }` in input order. A bad entry returns `400` naming its index (`entry 3: message is required`). Body limit 2 MiB.

### GET /api/logs/stream

Server-Sent Events stream. Each event is a JSON-encoded log entry:

```
data: {"id":43,"timestamp":"2026-01-15T10:30:01Z","level":"INFO","type":"SYSTEM","service":"","message":"Application started","attrs":{}}

```

The stream also sends:

- `event: resync` when entries may have been missed (the server's database listener reconnected). Reload `GET /api/logs`.
- `: ping` comments every 30 seconds, so proxies do not close an idle stream.

**Query parameters:** the same filters as `GET /api/logs`.

### GET /api/facets

The types and services seen in the newest 10000 entries, for filter dropdowns: `{"types": [...], "services": [...]}`.

### /api/session

For the dashboard. `GET` returns `{"auth": bool, "authenticated": bool, "canWrite": bool}`. `POST {"token": "<read token>"}` sets the session cookie (5 attempts per minute per client address; `X-Forwarded-For` is ignored). `DELETE` signs out.

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

Set both tokens before GoLog is reachable from a network you do not fully trust, and put TLS in front of it. Details and how to report a vulnerability: [SECURITY.md](SECURITY.md).

## License

MIT -- see [LICENSE](LICENSE).
