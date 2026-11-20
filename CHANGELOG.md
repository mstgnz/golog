# Changelog

All notable changes to GoLog are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [2.1.1] - 2026-11-20

### Fixed
- Dashboard: signing out now also clears the type and service suggestions and the selected filters, and a facets response that arrives after sign-out is ignored. Before, type and service names could stay in the page.
- Login rate limiter: memory is now actually bounded. When it tracks the maximum number of recent addresses, new addresses are refused until entries expire (Bearer tokens are unaffected). IPv6 addresses share one budget per /64.
- `POST /api/logs/batch` reserves ids from the sequence before inserting, so each returned id belongs to the entry at the same position by construction, not by the undocumented order of `RETURNING`.

## [2.1.0] - 2026-11-20

Nothing changes for existing HTTP clients, SQL writers or databases unless noted below; the server migrates the schema on start.

### Added
- `gologslog`: a `log/slog` handler that sends records in background batches and never blocks the caller (standard library only).
- Free-form `type` (1-50 characters) and a new `service` field; a JSON `attrs` object per entry (16 KiB).
- Optional client `timestamp` on `POST /api/logs` (at most one hour ahead); missing means the server's time.
- Filters `service`, `q` (message text) and `attr.<key>` on `GET /api/logs`, the stream and the CLI. List and stream apply identical rules.
- `POST /api/logs/batch` (1-100 entries, all or nothing) and `GET /api/facets`.
- Read and write tokens (`GOLOG_READ_TOKEN`, `GOLOG_WRITE_TOKEN`), a dashboard sign-in with an HttpOnly session cookie, and a rate-limited login. Without tokens GoLog stays open and warns at startup.
- Retention: `GOLOG_RETENTION_DAYS` deletes old logs hourly in small batches. Off by default.
- CLI: `-service`, `-q`, `-attr key=value`, and `-url` / `GOLOG_URL` to read a remote server with the read token.
- Dashboard: sign-in, type and service dropdowns from real data, text and attribute search, attributes under each message, favicon.
- `DB_HOST` may be a unix socket directory.
- Migrations 0003 (`service`, `attrs` columns, no rewrite) and 0004 (GIN index on `attrs`, built concurrently).

### Changed
- `type` is no longer limited to the five built-in values, and the CLI no longer upper-cases `-type` (types are case-sensitive now; `-level` still accepts any case).
- The message limit counts characters, not bytes. Input PostgreSQL would refuse is a `400`, not a `500`: NUL characters, numbers in attrs beyond 100 characters or an exponent of ±1000, and invalid UTF-8 in query-string filters. Attrs are re-encoded before storage, so escapes such as a lone `\ud800` are stored as U+FFFD instead of failing.
- `POST /api/logs` body limit raised from 64 KiB to 128 KiB to fit a maximum entry with escaped characters.
- `docker-compose.yml` binds Postgres and GoLog to `127.0.0.1`.
- A failed no-transaction migration names the recovery step in its error.
- CI actions moved to their Node 24 majors.
- For Go code that implements `handlers.LogStore` outside this repository: the interface gained `InsertLogs` and `Facets`. The server packages are not meant as a library API; `gologslog` is.

## [2.0.0] - 2026-11-20

### Breaking
- Go module path is now `github.com/mstgnz/golog/v2`.
- The NOTIFY payload on `log_channel` is now the row id, not the JSON row. Anything else listening on that channel must read the row itself.
- `init.sql` is gone. `golog-server` creates and upgrades the schema itself (embedded migrations, `GOLOG_AUTO_MIGRATE`).
- CORS is off unless `CORS_ALLOWED_ORIGINS` is set, and credentials are never allowed. Before, every origin was allowed.
- `database` package: `Connect`, `Close`, the global `DB` and `NewStore` are replaced by `database.Open(ctx, dsn)`; store methods take a `context.Context`.
- `handlers.NewServer` takes `handlers.Options`; `LogStore.ListenForLogs` delivers `models.LogEvent`.

### Fixed
- Stored XSS in the dashboard: log fields were inserted with `innerHTML`.
- Inserts with a message over roughly 8000 bytes failed, because the trigger put the whole row into NOTIFY. Applications writing to the table directly were affected too.
- The dashboard opened twice as many SSE connections on every filter change.
- The dashboard showed the initial list oldest first and live entries newest first.
- A slow SSE client caused a "close of closed channel" panic.
- Shutdown with an open dashboard waited for the timeout and exited with an error.
- `DB_*` defaults in `config` were never used; passwords with special characters broke the connection string; `sslmode` was hard-coded to `disable`.
- `docker compose up` could start the app before Postgres was ready, and it was not restarted.
- The dashboard returned 404 when the binary was started outside the repository root.
- The listener did not notice a dead connection and silently lost entries after reconnecting; it now pings and sends `resync`.
- A row with a NULL timestamp broke the whole list query.
- The CLI printed entries twice when they arrived during startup, accepted unknown filter values silently, and passed terminal escape sequences from messages through.

### Added
- `DATABASE_URL`, `DB_SSLMODE`, `CORS_ALLOWED_ORIGINS`, `GOLOG_AUTO_MIGRATE` settings.
- Index `logs_timestamp_id_idx` for the list query, built concurrently.
- `event: resync` and `: ping` heartbeat on the SSE stream; live/reconnecting status in the dashboard.
- Request body limit (64 KiB, `413`), generic `500` responses, security headers, HTTP server timeouts.
- Multi-stage Docker image (static binary, distroless, non-root).
- Integration tests against PostgreSQL (`GOLOG_TEST_DSN`, `make test-db`), run in CI with govulncheck and a Docker build.

### Changed
- `GET /api/logs` orders by `timestamp DESC, id DESC` so pages are stable.
- `github.com/go-chi/chi/v5` 5.2.1 -> 5.2.2 (GO-2025-3770). Docker build uses Go 1.27.4 (GO-2026-6859).

## [1.1.0] - 2026-04-21

### Added
- Input validation for `POST /api/logs` (level, type, message length)
- Query-parameter validation for `GET /api/logs` (level, type)
- `Log.Validate()` method with constants for valid levels and types in `models` package
- `CONTRIBUTING.md`, `SECURITY.md`, and issue/PR templates
- GitHub Actions CI pipeline (`go build`, `go vet`, `go test -race`)

### Fixed
- `GetLogsHandler` now returns an empty JSON array instead of `null` when no logs match
- `ListenForLogs` reuses the connection string from `Connect()` instead of re-reading environment variables
- Encode errors in HTTP handlers are now logged instead of silently discarded

### Changed
- `interface{}` replaced with `any` in database query args (Go 1.18+ idiom)
- Removed duplicate `getEnv` helper from `database` package

## [1.0.1] - 2025-03-09

### Fixed
- Handler fixes and dependency updates.

## [1.0.0] - 2025-03-09

### Added
- Real-time log monitoring via PostgreSQL LISTEN/NOTIFY
- Web interface with SSE streaming and filter support
- CLI tool with `-level` and `-type` flags
- REST API: `GET /api/logs`, `POST /api/logs`, `GET /api/logs/stream`
- Docker Compose setup
- Unit and mock-based tests
