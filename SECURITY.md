# Security Policy

## Supported Versions

| Version | Supported |
| ------- | --------- |
| latest  | yes       |

## Reporting a Vulnerability

If you discover a security vulnerability in Subtitler, please report it responsibly:

1. Do not open a public issue for security vulnerabilities.
2. Email the maintainers directly or use GitHub private vulnerability reporting.
3. Include a detailed description, reproduction steps, and potential impact.

## What to Expect

- We will acknowledge reports within 48 hours.
- We will provide an estimated timeline for a fix.
- We will notify you when the vulnerability is fixed.
- We will credit you in release notes unless you prefer to remain anonymous.

## Security Best Practices for Users

When deploying Subtitler:

- Keep your instance updated.
- Keep OpenAI, Sonarr, and Radarr API keys private.
- Run the container as the user that owns your media files.
- Start with `dry_run: true` and `cleanup.external_subtitles: keep`.
- Avoid exposing Subtitler directly to the public internet.
- Back up media-adjacent sidecar subtitle files before enabling cleanup modes.

## Scope

This policy covers Subtitler itself. Third-party integrations such as OpenAI, Sonarr, Radarr, Jellyfin, Docker, and Sentry have their own security policies.
