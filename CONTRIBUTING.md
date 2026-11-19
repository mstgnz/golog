# Contributing to GoLog

Thank you for your interest in contributing! This document explains how to get started.

## Development setup

**Prerequisites:** Go 1.22+, PostgreSQL 16+, Docker (optional)

```bash
git clone https://github.com/mstgnz/golog.git
cd golog
cp .env.example .env   # edit as needed
```

Run unit tests (no database required):

```bash
make test
```

Run everything, including integration tests against a throwaway Postgres in Docker:

```bash
make test-db
make test-integration
make test-db-stop
```

Integration tests need `GOLOG_TEST_DSN` (set by the Makefile). Each test creates and drops its own schema. Packages run one at a time (`-p 1`) because they share the NOTIFY channel.

Run with Docker Compose for a full local environment:

```bash
docker compose up --build
```

## Workflow

1. Fork the repository and create a branch from `main`.
2. Make your changes and add tests for any new behaviour. A bug fix comes with a test that fails without it.
3. Run `make lint` and `make test-integration` -- both must pass.
4. Open a pull request using the provided template.

## Schema changes

Add a new file to `database/migrations/` named `NNNN_description.sql` with the next number. Never edit a migration that has been released. Start the file with a short header: what it does and how to roll it back. A statement that cannot run in a transaction (`CREATE INDEX CONCURRENTLY`) goes in its own file whose first line is `-- golog:no-transaction`.

## Code style

- Follow standard Go conventions (`gofmt`, `go vet`).
- Keep functions small and focused.
- Add a one-line comment only when the *why* is non-obvious.
- Do not add comments that merely restate what the code does.

## Commit messages

Use the imperative mood and keep the subject line under 72 characters.

```
add pagination support for GetLogs
fix nil slice encoding in GetLogsHandler
```

## Log levels and types

Valid log levels: `INFO`, `WARNING`, `ERROR`, `DEBUG`

Valid log types: `SYSTEM`, `AUTH`, `DATABASE`, `USER`, `API`

These are defined as constants in `models/log.go` and enforced by `Log.Validate()`.

## Reporting issues

Use the GitHub issue templates for bug reports and feature requests.
