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
- `internal/bom/` owns deterministic demand aggregation and the eC-BOM CSV
  export (including the formula-injection guard).
- `internal/money/` owns exact fixed-point decimal arithmetic.
- `internal/fx/` owns dated ECB reference quotes and exact currency
  conversion (one half-to-even rounding, explicit failures).
- `internal/procurement/` owns normalized offer types and the purchase-plan
  optimizer.
- `internal/provider/` owns the provider registry, discovery, health checks,
  and one adapter package per provider. Every provider is declared once in
  `registry.go`; its doc comment carries the complete add-a-provider
  checklist. Keep provider HTTP inside the provider's own adapter package.
- `internal/sourcing/` owns provider-independent orchestration: the
  multi-provider resolver, resolution-aware redirection, and safe totals.
- `internal/lookupcache/` owns versioned SQLite normalized-result
  persistence, including per-provider adapter versions and context hashes.
- `internal/resolutions/` owns audited persistence of human-approved
  resolutions. Provider values in stored records may include removed
  providers; validation must keep accepting them so durable data stays
  readable.
- `internal/tui/` owns the interactive terminal mode (Bubble Tea); its
  model layer is a pure state machine and must stay testable without a
  terminal. `interactive` is the only command exempt from the JSON stdout
  protocol and must keep refusing non-TTY stdio.
- `internal/webui/` owns the local web interface: loopback-only listener,
  per-session bearer token, loopback Host/Origin enforcement, strict CSP,
  and a hand-written embedded frontend (stdlib only; no npm, no build
  step, no external requests). Its JSON API is an internal contract with
  its own frontend, not a public interface. `serve` emits exactly one
  JSON startup envelope on stdout and then serves until interrupted.
- `internal/documents/` owns evidence-link discovery and safe PDF retrieval.
- `internal/config/` owns non-evaluating `.env` loading, including the
  restricted-key list that keeps trusted-path overrides out of `.env`.
- `internal/app/` owns build and version metadata.
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

`make check` pins Go 1.25.12 and runs unit tests, race tests, vet, the
`windows/amd64` cross-compilation and vet gate, and the native build. Windows
is a supported target: keep new code portable (use `filepath`, no POSIX-only
process tricks outside explicitly gated adapters). Use `gofmt` on every
changed Go file.
