# Contributing to GoLog

Thank you for your interest in contributing! This document explains how to get started.

## Development setup

**Prerequisites:** Go 1.22+, PostgreSQL 16+, Docker (optional)

```bash
git clone https://github.com/mstgnz/golog.git
cd golog
cp .env.example .env   # edit as needed
```

Run tests (no database required):

```bash
make test
```

Run with Docker Compose for a full local environment:

```bash
docker-compose up
```

## Workflow

1. Fork the repository and create a branch from `main`.
2. Make your changes and add tests for any new behaviour.
3. Run `make test` and `make vet` -- both must pass.
4. Open a pull request using the provided template.

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
