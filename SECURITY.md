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

- GoLog is designed for internal/trusted network use. The API has no authentication layer by default. Do not expose it directly to the public internet.
- The `CORS` policy is currently set to `*`. Restrict `AllowedOrigins` in production.
- Database credentials are read from environment variables. Never commit a `.env` file with real credentials.
