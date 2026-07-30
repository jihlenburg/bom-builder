# BOM Builder Roadmap

This roadmap tracks the CLI modernization work. The goal is to make BOM Builder
equally dependable for people, coding agents, CI jobs, and other automation.
Items are grouped by outcome rather than implementation file.

## CLI Foundation

- [x] Ship an installable `bom-builder` command through `pyproject.toml`
- [x] Replace the flat argument surface with focused subcommands:
  - [x] `bom-builder price`
  - [x] `bom-builder lookup`
  - [x] `bom-builder validate`
  - [x] `bom-builder providers list|check`
  - [x] `bom-builder cache status|purge`
  - [x] `bom-builder resolutions list|remove|purge`
  - [x] `bom-builder schema input|output`
- [x] Keep command help concise, task-oriented, and rich in copyable examples
- [ ] Make non-interactive operation the default and require an explicit flag
      for prompts or the full-screen resolver

## Machine Interface

- [x] Add a first-class machine mode with JSON as the only stdout content
- [x] Send diagnostics, warnings, progress, and verbose logs to stderr
- [x] Support `--output -` for stdout and avoid creating default files in
      machine mode
- [x] Accept design JSON from stdin with the `-` design source
- [x] Version the input/output contract with a stable `schema_version`
- [ ] Return one structured result envelope containing:
  - [x] overall status and exit status
  - [x] run ID, version, timing, and invocation metadata
  - [x] selected provider configuration and per-provider status
  - [x] BOM summary and normalized priced parts
  - [x] warnings and errors with stable machine-readable codes
  - [ ] cache hits, live request counts, latency, and source provenance
- [ ] Add optional newline-delimited JSON progress events for long runs
- [x] Add `--no-progress`
- [ ] Add `--log-format text|json`

## Exit and Completion Policy

- [x] Define and document stable process exit codes
- [x] Add `--fail-on never|error|review`
- [x] Add `--allow-partial` for useful output when one provider degrades
- [ ] Distinguish input errors, provider authentication errors, quota/rate
      limits, unresolved parts, review-required matches, and internal failures
- [x] Ensure partial runs still emit a valid structured result envelope

## Provider Control and Health

- [x] Make Mouser, Digi-Key, TI, and NXP independently selectable
- [x] Add `--providers` and `--exclude-provider`
- [x] Remove the requirement for Mouser credentials when another selected
      provider can perform the lookup
- [x] Add `bom-builder providers list` for capability discovery
- [x] Add `bom-builder providers check` with configuration-only and live modes
- [x] Report credential presence without ever revealing credential values
- [x] Report authentication, endpoint health, locale/currency, latency, request
      count, quota/rate-limit metadata, and degraded adapter state
- [x] Include optional service checks for ECB FX and OpenAI reranking
- [ ] Add configurable provider retry, timeout, and circuit-breaker policies

## Stock-Aware Substitution Analysis

- [ ] Add `bom-builder alternatives` for explicit alternative-part searches
- [ ] Add `price --find-alternatives never|shortage|always`
- [ ] Define shortage policy using required quantity, available quantity,
      lead time, lifecycle state, and configurable safety margin
- [ ] Separate substitution confidence into explicit tiers:
  - [ ] alternate distributor/orderable packaging for the same MPN
  - [ ] same-manufacturer orderable or package variant
  - [ ] manufacturer-published cross-reference
  - [ ] parametric equivalent requiring engineering review
- [ ] Start with constrained passive-component substitutions before enabling
      broader semiconductor recommendations
- [ ] Build component-family constraint schemas for resistors, capacitors,
      inductors, diodes, transistors, regulators, interfaces, sensors, and
      other common classes
- [ ] Enforce hard compatibility filters where applicable:
  - [ ] footprint, package dimensions, pin count, and pin mapping
  - [ ] value, tolerance, voltage/current/power ratings, and temperature range
  - [ ] polarity, dielectric/material/technology, and frequency behavior
  - [ ] qualification level such as AEC-Q100/AEC-Q200
  - [ ] lifecycle, moisture sensitivity, RoHS/REACH, and manufacturing needs
- [ ] Treat missing critical specifications as unknown rather than compatible
- [ ] Gather candidates from distributor search, manufacturer parametric data,
      published cross-reference data, and approved project-local mappings
- [x] Preserve datasheet and product-document links exposed by Mouser and
      Digi-Key in JSON/CSV/Excel output
- [x] Preserve TI and NXP direct-store product links in normalized offers
- [ ] Discover manufacturer-direct datasheet links beyond distributor metadata
- [ ] Add `bom-builder documents list|fetch` for explicit datasheet discovery
      and download
- [ ] Prefer manufacturer-hosted datasheets over distributor mirrors while
      retaining distributor URLs as fallbacks
- [ ] Cache retrieved documents with source URL, retrieval time, content hash,
      detected revision/date, MIME type, and provider provenance
- [ ] Detect duplicate, stale, redirected, blocked, and non-PDF document links
- [ ] Extract substitution evidence from datasheets with page-level citations,
      including absolute-maximum ratings, recommended operating conditions,
      package drawings, ordering tables, and pin assignments
- [ ] Keep extracted claims linked to the exact document hash so a later
      datasheet revision cannot silently change an earlier compatibility result
- [ ] Rank compatible candidates by:
  - [ ] verified compatibility and evidence quality
  - [ ] aggregate stock across selected providers
  - [ ] lead time, lifecycle, and multi-source resilience
  - [ ] landed price, MOQ, packaging plan, and surplus
- [ ] Return a field-by-field compatibility matrix showing equal, better,
      worse, and unknown properties
- [ ] Attach source provenance and retrieval time to every compatibility claim
- [ ] Never let an LLM assertion alone establish electrical or pin-compatible
      equivalence; use AI only to extract or prioritize evidence for validation
- [ ] Add explicit approval and persistent project-local substitution mappings
- [ ] Include substitution choices and rejected-candidate reasons in machine
      JSON, reports, and audit logs
- [ ] Add regression fixtures for known-compatible and subtly incompatible
      parts, including package and pinout traps

## Bounded and Deterministic Execution

- [ ] Add a run-wide `--deadline`
- [ ] Add per-provider timeouts
- [ ] Add an API request budget such as `--max-api-calls`
- [x] Add explicit `--fresh` mode
- [ ] Add explicit `--cache-only` and `--offline` modes
- [ ] Make cache and saved-resolution use visible in machine output
- [x] Add deterministic ordering for warnings and errors
- [ ] Define and enforce canonical ordering for parts and offers
- [ ] Add a reproducible mode that pins FX inputs and avoids live enrichment

## Schema and Discovery

- [x] Expose JSON Schema for design input
- [x] Expose JSON Schema for machine output
- [x] Add a machine-readable capabilities document
- [x] Include supported providers, formats, policies, and feature flags in
      capability output
- [ ] Document schema compatibility and deprecation policy

## Secrets and Safety

- [x] Prefer environment or `.env` credentials and remove API-key CLI flags
- [ ] Ensure secrets never appear in process arguments, traces, exceptions, or
      structured output
- [ ] Add redaction tests for every provider
- [x] Add a safe provider configuration report
- [x] Make destructive cache/resolution operations explicit subcommands with
      exact target previews

## Performance and Operations

- [ ] Add safe bounded parallelism across independent parts/providers
- [ ] Preserve provider rate limits under concurrency
- [ ] Add cancellation handling and deadline-aware network operations
- [x] Add run IDs
- [ ] Add idempotent output behavior
- [ ] Make interrupted runs resumable from cached normalized offers
- [ ] Add benchmark fixtures for small, medium, and production-sized BOMs

## Packaging, Documentation, and Quality

- [x] Add packaging metadata and a console entry point in `pyproject.toml`
- [x] Add executable discovery helpers that expose capabilities and schemas
- [x] Add a host-native standalone build that embeds Python, dependencies, and
      resolver data
- [ ] Publish signed/notarized `bom-builder` binaries for macOS/Linux/Windows
- [ ] Add CLI contract tests that execute the installed command as a subprocess
- [ ] Add golden JSON output tests and JSON Schema validation
- [x] Add provider-health tests with mocked transports
- [x] Add stdin/stdout pipeline tests
- [x] Update README quick-start examples to use `bom-builder`
- [x] Add an agent/automation guide with Claude, Codex, shell, and CI examples
- [x] Document provider selection, strictness policies, exit codes, and
      troubleshooting
- [x] Record each completed CLI slice in `LOGBOOK.md`

## Code Review Remediation

- [ ] Make offer selection stock-aware and treat insufficient selected stock as
      a strict completion failure rather than a successful priced line
- [ ] Fail closed when FX conversion fails; never total or compare monetary
      values from different currencies under one currency label
- [ ] Keep AI-reranked fuzzy matches review-required until deterministic
      electrical/package compatibility checks independently confirm them
- [ ] Replace substring-based MPN equivalence in Digi-Key, TI, and NXP with
      provider-aware packaging/orderable rules; never label loose containment
      as an exact match
- [ ] Preserve every Digi-Key product/order leg when an API pricing option is a
      composite purchase instead of collapsing it to one distributor SKU
- [ ] Escape formula-leading user/provider text in CSV and Excel reports
- [ ] Remove secret-bearing legacy CLI arguments and redact command traces,
      exception text, and provider response excerpts
- [ ] Add inter-process locking and unique atomic temp files to the saved
      resolution store
- [ ] Reject non-finite CLI numbers such as `nan` and `inf` as input errors
- [ ] Establish enforced Ruff and mypy baselines, then clear the current static
      analysis findings without hiding provider contract mismatches
- [ ] Add automated TUI/event/worker coverage; the current full suite does not
      execute the Textual package
