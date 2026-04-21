# Changelog

All notable changes to GoLog are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

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

## [0.1.0] - 2024

### Added
- Real-time log monitoring via PostgreSQL LISTEN/NOTIFY
- Web interface with SSE streaming and filter support
- CLI tool with `-level` and `-type` flags
- REST API: `GET /api/logs`, `POST /api/logs`, `GET /api/logs/stream`
- Docker Compose setup
- Unit and mock-based tests
