# Contributing to BOM Builder

Thanks for considering a contribution. This page covers the practical
rules; `CLAUDE.md` carries the same engineering rules in a form aimed at
coding agents, and the two must stay consistent.

## Building and testing

```bash
make build    # reproducible native build into bin/
make check    # unit tests, race tests, vet, windows/amd64 gate, build
```

Any Go 1.25 toolchain with automatic toolchain downloads works; the
Makefile pins the exact compiler. `make check` must pass and `gofmt` must
be clean before a change is ready. No test may contact a live provider
endpoint: provider tests use recorded fixtures or local HTTP transports,
so the whole suite runs without credentials.

## Ground rules for changes

- Stdout is a protocol channel. Machine commands emit exactly one JSON
  document; diagnostics go to stderr.
- Money is exact fixed-point decimal. Binary floating point never touches
  the pricing core.
- Insufficient stock, failed currency normalization, unknown critical
  compatibility fields, and loose part-number matches are explicit states,
  never silent defaults.
- Automated results may rank evidence but never clear engineering review.
  Only a named person can approve a resolution.
- Provider HTTP stays inside that provider's adapter package. Adding a
  provider starts at the checklist in `internal/provider/registry.go`.
- Never cache credentials, tokens, errors, or raw provider responses.
  Cache format changes must invalidate old keys explicitly.
- Keep output ordering deterministic and issue codes stable.
- Windows is a supported target: use `filepath`, avoid POSIX-only process
  tricks, and keep the `windows/amd64` gate green.
- Every behavior change comes with tests, and substantial work updates
  `TODO.md`, `TASKS.md`, `LOGBOOK.md`, and the user documentation.

## Licensing

BOM Builder is GPL-3.0-or-later. By submitting a contribution you agree to
license it under the same terms (inbound = outbound). Start every new Go
file with the copyright line and the `GPL-3.0-or-later` SPDX identifier,
followed by a blank line so the package doc comment stays attached.

## Reporting problems

Use the GitHub issue tracker for bugs and feature discussion. For
security-sensitive reports, follow `SECURITY.md` instead of opening a
public issue.
