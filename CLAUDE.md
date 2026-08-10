# CLAUDE.md — Coding Agent Instructions for BOM Builder

## Project

BOM Builder is a native Go CLI for electronic BOM sourcing, pricing, provider
health, and stock-aware substitution analysis. The Go implementation is the
only implementation; the archived Python predecessor has been removed and lives
on only in Git history.

## Current architecture

- `cmd/bom-builder/` owns the executable entry point.
- `internal/cli/` owns command parsing, dispatch, exit codes, and JSON output.
- `internal/contract/` owns public machine-interface and design types.
- `internal/design/` owns strict design JSON loading and validation.
- `internal/provider/` owns safe provider discovery and future adapters.
- `internal/lookupcache/` owns versioned SQLite normalized-result persistence.
- `internal/config/` owns non-evaluating `.env` loading.
- `schemas/` contains and embeds the versioned public JSON contracts.
- `examples/` holds runnable example input documents used by the README.

## Engineering rules

- Do not commit or push automatically.
- Do not expose credentials in arguments, logs, errors, fixtures, or JSON.
- Stdout is a protocol channel: emit exactly one JSON document for machine
  commands. Send diagnostics to stderr.
- Use Go standard library packages where they are a good fit. Add dependencies
  deliberately and document why they are needed.
- Use integer minor currency units or a decimal type for money; do not use
  binary floating point in the new pricing core.
- Treat insufficient stock, failed currency normalization, unknown critical
  compatibility fields, and loose part-number matches as explicit states.
- An AI result may rank evidence but may never independently clear engineering
  review.
- Keep provider HTTP code inside its provider adapter.
- Never cache provider errors, credentials, tokens, or raw provider responses.
  Cache format/adapter changes must invalidate old lookup keys explicitly.
- Preserve deterministic output ordering and stable issue codes.
- Add tests with every behavior change. Provider tests must use recorded
  fixtures or local HTTP transports, not live endpoints.
- Update `TASKS.md`, `TODO.md`, `LOGBOOK.md`, and user documentation for
  substantial work.
- The project is licensed under GPL-3.0-or-later. Start every new Go file with
  the copyright line and the `GPL-3.0-or-later` SPDX identifier, followed by a
  blank line so the package doc comment stays intact. Do not modify `LICENSE`;
  the GPL text must remain verbatim.

## Verification

Run before handoff:

```bash
make check
```

`make check` pins Go 1.25.12 and runs unit tests, race tests, vet, and the native
build. Use `gofmt` on every changed Go file.
