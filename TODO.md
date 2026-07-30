# Go Rewrite Roadmap

The Go implementation intentionally starts with a small, truthful capability
surface. Commands are advertised only after their contracts and tests exist.
This list carries forward every unfinished CLI, substitution, document, safety,
and quality item from the Python roadmap.

## CLI and machine interface

- [x] Native Go executable with no Python runtime dependency
- [x] Versioned JSON output and stable process exit codes
- [x] Strict design loading from files and stdin
- [x] Embedded input, output, and provider JSON Schemas
- [x] `capabilities --full` and safe provider configuration discovery
- [x] Add focused `price`, `lookup`, and `providers check` commands
- [x] Add focused `cache status|list|verify|prune` commands
- [ ] Add focused `resolutions` commands
- [x] Keep all operational commands non-interactive by default
- [x] Add concise command help with copyable human and agent examples
- [ ] Complete provider selection; `--providers auto`, every explicit native
      provider, and combined selection work without unrelated credentials,
      while exclude syntax remains
- [ ] Add `--output -`, `--allow-partial`, `--fail-on`, and idempotent artifact
      output policies
- [ ] Complete run envelopes; run IDs, timing, provider request counts, cache
      counts, warnings, and part provenance are implemented, while full
      invocation metadata remains
- [ ] Distinguish authentication, quota, rate-limit, provider, unresolved,
      review-required, deadline, and internal failures with stable codes
- [ ] Add optional NDJSON progress and structured stderr logging
- [ ] Add schema compatibility and deprecation policy

## Core sourcing engine

- [x] Deterministic BOM aggregation with provenance-preserving references
- [ ] Complete the money/FX layer; exact six-place fixed-point money is
      implemented, while dated cross-currency conversion remains
- [ ] Complete optimizer verification; the native purchase-plan optimizer and
      legacy behavioral cases are ported, while serialized cross-language
      golden fixtures remain
- [x] Define canonical ordering for aggregated parts and Mouser price breaks
- [x] SQLite normalized-response cache with migrations, WAL, expiry, checksums,
      adapter/context identity, and source-request provenance
- [ ] Concurrent-safe approved-resolution store with locking, atomic writes,
      audit history, and exact destructive-operation previews
- [ ] Complete execution policies; run-wide deadline, cancellation, bounded
      Mouser retry, and key rotation are implemented, while per-provider
      policies, circuit breakers, and API budgets remain
- [x] Add explicit refresh, cache-only, offline, prefer-cache, and bypass modes
- [ ] Add bounded parallelism that preserves provider rate limits
- [ ] Make interrupted runs resumable from cached normalized offers

## Provider adapters

- [x] Audit the historical Digi-Key fixes against the Go adapter: OAuth token
      reuse and refresh safety, normalized locale/account headers, MyPricing
      plus standard/composite and cheaper-overbuy plans, manufacturer/MPN
      safeguards, datasheet/product links, normalized cache reuse, and the
      ProductDetails per-variation stock correction all have native regression
      coverage. The obsolete `X-DIGIKEY-Customer-Id: 0` fallback was
      deliberately not ported: Digi-Key removed that header in November 2025
      and requires `X-DIGIKEY-Account-Id` for Product Information V4 with
      two-legged OAuth.
- [x] BUG FIXED 2026-07-30 (root cause + live verification in LOGBOOK):
      Digi-Key reported `available_quantity: 0` for well-stocked parts.
      Cause: the quantity-pricing endpoint's `QuantityAvailable` is not
      populated (returns 0 regardless of stock). Stock now comes from
      ProductDetails per-variation quantities (`ProductInformation`,
      `applyStock`); unknown variations report `stock_unknown`, never
      zero. Cache adapter version bumped to `digikey-normalized-v2`.
      Reference part TCAN1473CDRQ1 now returns priced / 2,940 available.
- [x] Mouser key rotation, exact lookup, bounded candidate search, stock,
      pricing, datasheet links, and product links
- [x] Digi-Key OAuth, locale/account pricing, stock, composite SKU plans, and
      document links
- [x] TI OAuth/store pricing without exposing secrets to process arguments or
      requiring an external `curl` executable
- [x] NXP direct sourcing through a documented browser/CDP boundary
- [ ] EVALUATED AND NOT PURSUED (2026-07-30): a `microchipdirect` provider
      on the NXP headless-CDP pattern. Feasibility spike failed decisively:
      microchipdirect.com resets plain TLS clients at connection level
      (curl: code 000 in 0.06s), microchip.com edge-403s them, and
      headless Chrome (`--headless=new --dump-dom`) receives an error page
      instead of the site — Akamai-class bot management, unlike NXP's
      store which tolerates a private headless session. Building this
      would mean maintaining bot-management evasion, which is out of
      scope for this tool. Use instead: an interactive browser session
      for one-off checks, or Eurocircuits' upload-time sourcing (Microchip
      is an EC preferred DIRECT supplier).
- [ ] `microchipdirect` provider via Microchip's OFFICIAL Purchasing API
      (2026-07-30 follow-up): Microchip offers a credentialed Purchasing
      API for MicrochipDirect business accounts, "built to integrate with
      sourcing tools" — the same credentialed-adapter model as Mouser/
      Digi-Key/TI, no scraping involved. PREREQUISITE (account owner):
      request API access on the MicrochipDirect business account, obtain
      docs + credentials; THEN implement as a normal native adapter with
      recorded fixtures. Would cover the manufacturer-direct channel that
      distribution lacks (fresh temp/speed grades, e.g. -E/PT parts).
- [ ] Optional breadth: evaluate a Nexar (Octopart) aggregator provider —
      one credentialed GraphQL API adding Farnell/RS/TME coverage (all
      Eurocircuits preferred suppliers bom-builder lacks); check seller
      coverage for manufacturer-direct stores and commercial API terms
      before committing.
- [ ] ECB dated FX quotes with explicit failure propagation
- [ ] Optional OpenAI evidence ranking that never clears engineering review
- [x] Complete live health coverage for every implemented distributor;
      Mouser, Digi-Key, TI, and NXP checks report latency, currency/locale,
      request counts, and available provider-specific metadata

## Procurement safety

- [x] Select only offers that cover required stock, or explicitly return a
      shortage/partial-fill plan
- [ ] Define shortage using required quantity, safety margin, available stock,
      lead time, and lifecycle state
- [x] Never compare or total prices until every value has a proven common
      currency; fail closed when FX conversion fails
- [ ] Model lifecycle, lead time, MOQ, order multiples, pack quantities, and
      stock as typed fields rather than presentation strings
- [x] Treat substring/prefix MPN relationships as review-required unless a
      provider-specific orderable rule proves packaging-only equivalence
- [x] Preserve every SKU, quantity, and currency in composite purchase plans
- [ ] Keep AI-ranked fuzzy matches review-required until deterministic
      compatibility checks pass
- [ ] Reject non-finite numeric input
- [ ] Escape formula-leading content in CSV and Excel artifacts
- [x] Ensure secrets never appear in arguments, errors, output, or provider
      excerpts for every currently implemented provider, with redaction tests

## Stock-aware alternatives

- [ ] Complete alternative discovery and price integration; explicit proposed
      candidate evaluation and `--only-if-shortage` sourcing are implemented,
      while automatic candidate discovery and `price` command triggering remain
- [ ] Separate confidence tiers for same-MPN packaging, manufacturer variants,
      published cross-references, and engineering-review parametric matches
- [x] Start with constrained resistor, capacitor, and inductor substitutions
- [ ] Extend the component-family constraint schema beyond resistors,
      capacitors, and inductors to diodes, transistors, regulators, interfaces,
      sensors, and other common classes
- [ ] Complete compatibility constraints; passive package, dimensions,
      electrical ratings, temperature, technology, polarity/shielding, and
      qualification checks are implemented, while pin mapping, lifecycle,
      compliance, and manufacturing constraints remain
- [x] Treat missing critical specifications as unknown, never compatible
- [ ] Gather candidates from distributors, manufacturer parametric data,
      published cross-references, and approved project-local mappings
- [ ] Complete alternative ranking; compatibility, safe stock, common-currency
      price, MOQ, packaging plans, and surplus are implemented, while evidence
      quality, lead time, lifecycle, multi-source resilience, and landed cost
      remain
- [x] Return a field-by-field equal/better/worse/unknown compatibility matrix
- [ ] Complete decision persistence; recommended-for-review and rejected reasons
      are in JSON, while approvals, reports, and audit logs remain
- [x] Add deterministic fixtures for compatible, missing-rating, package,
      dielectric-alias, DCR, stock, and mixed-currency cases

## Datasheets and evidence

- [x] Preserve distributor and manufacturer product/document links in all
      currently implemented normalized offers
- [x] Add explicit `documents list|fetch` discovery and download commands
- [x] Prefer manufacturer-hosted datasheets while retaining distributor mirrors
- [ ] Cache source URL, retrieval time, content hash, revision/date, MIME type,
      and provider provenance
- [ ] Complete document validation; duplicate normalization, bounded redirects,
      private-network blocking, and non-PDF rejection are implemented, while
      stale-document and revision detection remain
- [ ] Extract page-level evidence for ratings, operating conditions, packages,
      ordering tables, and pin assignments
- [ ] Bind every extracted claim to an exact document hash and retrieval time
- [ ] Never let an LLM assertion alone establish electrical or pin compatibility

## Reports, distribution, and quality

- [x] Add `export ec-bom` — upload-ready Eurocircuits assembly BOM CSV from
      one validated design (2026-07-30; optional designators/value/mounted/
      comment part fields, immutable-on-write artifact with SHA-256)
- [ ] Add normalized JSON, buyer-oriented CSV, and Excel output
- [ ] Add subprocess contract tests, golden JSON tests, and schema validation
- [ ] Add redaction, cancellation, concurrency, and mocked provider-health tests
- [ ] Add benchmark fixtures for small, medium, and production-sized BOMs
- [ ] Add CI for tests, race detector, vet, static analysis, schema
      compatibility, cross-builds, and releases
- [ ] Cross-compile release binaries for macOS, Linux, and Windows
- [ ] Add checksums, SBOMs, signing, and macOS notarization
- [ ] Add shell completions and Claude/Codex/shell/CI automation guides
