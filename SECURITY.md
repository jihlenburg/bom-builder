# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately, either through GitHub's
"Report a vulnerability" flow on this repository (Security tab) or by
email to <ihlenburg@ihlems.de>. Do not open a public issue for a security
report. You can expect an acknowledgement within a few days; please allow
a reasonable window for a fix before any public disclosure.

## Scope

BOM Builder is a local tool. The areas with security relevance:

- Credential handling: provider API keys come from the environment or a
  local `.env`; they must never appear in arguments, logs, errors, JSON
  output, or cache entries. `.env` parsing never evaluates shell syntax,
  and trusted-path/endpoint overrides are refused from `.env`.
- The local web UI (`bom-builder serve`): loopback-only listener,
  per-session bearer token, loopback Host and Origin enforcement, and a
  strict Content-Security-Policy. Anything that lets a remote origin read
  or drive the API is a vulnerability.
- Artifact handling: PDF downloads are HTTPS-only with size and signature
  checks; CSV exports neutralize spreadsheet formula injection; output
  files are never overwritten.
- The SQLite stores (lookup cache, resolutions): integrity checksums and
  the refusal to load records that fail validation.

## Supported versions

The latest release and the current `main` branch receive fixes.
