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

- **Set both tokens before GoLog is reachable from a network you do not fully trust.** Without `GOLOG_READ_TOKEN` anyone who can reach the server reads every log; without `GOLOG_WRITE_TOKEN` anyone can write. The server warns at startup when either is missing.
- The write token can only write and the read token can only read. Give applications the write token and people the read token. Tokens must be at least 16 characters and different from each other; use random values (`openssl rand -hex 24`).
- Tokens are compared in constant time. The dashboard cookie is `HttpOnly`, `SameSite=Strict`, derived from the read token by HMAC (it is not the token) and `Secure` over TLS or with `GOLOG_SECURE_COOKIES=true`. Changing the read token ends every session. The write token is never accepted from a cookie.
- Dashboard sign-in is limited to 5 attempts per minute per client address. The address is the TCP peer: behind a reverse proxy every client shares the proxy's address, so rate-limit at the proxy as well.
- Serve GoLog over TLS (for example behind a reverse proxy); tokens and cookies travel in every request. The CLI warns when it would send the read token over plain http to another host.
- CORS is off by default. `CORS_ALLOWED_ORIGINS` enables it for the listed origins only, never with credentials.
- Log messages and attributes are untrusted input. The dashboard renders them as text and the CLI strips terminal control characters.
- Responses carry `Content-Security-Policy: default-src 'self'`, `X-Frame-Options: DENY` and `X-Content-Type-Options: nosniff`. Request bodies are capped (128 KiB per entry, 2 MiB per batch).
- `GOLOG_RETENTION_DAYS` deletes data permanently. It is off by default.
- Build with a Go toolchain that has current security fixes; at least Go 1.26.11 or 1.27.4 (GO-2026-6859 affects the request body limit). The Docker image pins one.
- Database credentials are read from environment variables. Never commit a `.env` file with real credentials. Use `DB_SSLMODE=verify-full` (or a `DATABASE_URL` with it) when the database is reached over a network you do not control.
