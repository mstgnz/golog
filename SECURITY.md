# Security Policy

## Supported versions

| Version | Supported |
|---------|-----------|
| latest (`main`) | Yes |

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Instead, send an email to the maintainer with:

- A description of the vulnerability
- Steps to reproduce
- Potential impact
- Any suggested mitigations

You can find the maintainer's contact on their [GitHub profile](https://github.com/mstgnz).

We will acknowledge your report within 48 hours and aim to release a fix within 14 days for confirmed vulnerabilities.

## Security considerations

- GoLog is designed for internal/trusted network use. The API has no authentication layer yet: anyone who can reach it can read and write logs. Do not expose it directly to the public internet; put it behind an authenticating reverse proxy if it must be reachable.
- CORS is off by default. `CORS_ALLOWED_ORIGINS` enables it for the listed origins only, never with credentials.
- Log messages are untrusted input. The dashboard renders them as text and the CLI strips terminal control characters.
- Responses carry `Content-Security-Policy: default-src 'self'`, `X-Frame-Options: DENY` and `X-Content-Type-Options: nosniff`. Request bodies are capped at 64 KiB.
- Build with a Go toolchain that has current security fixes; at least Go 1.26.11 or 1.27.4 (GO-2026-6859 affects the request body limit). The Docker image pins one.
- Database credentials are read from environment variables. Never commit a `.env` file with real credentials. Use `DB_SSLMODE=verify-full` (or a `DATABASE_URL` with it) when the database is reached over a network you do not control.
