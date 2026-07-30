# Logbook

## 2026-07-31

- Completed a historical Digi-Key parity audit before continuing the rewrite.
  The Go adapter preserves the useful Python-era fixes: OAuth token reuse with
  expiry safety, locale normalization, account-scoped pricing, both account
  and standard pricing groups, composite and cheaper-overbuy purchase plans,
  strict manufacturer handling, conservative MPN handling, document links,
  and normalized persistent caching. Added focused regression tests for
  locale/account configuration, removal of the obsolete Customer-Id header,
  cross-group cheaper-overbuy selection, early manufacturer mismatch
  rejection, and the cache version that excludes legacy false-zero stock.
- Kept the current Digi-Key account contract rather than restoring the old
  `X-DIGIKEY-Customer-Id: 0` fallback. Digi-Key's November 2025 ProductSearch
  changelog removed Customer-Id from the affected V4 endpoints, and the
  current ProductDetails/PricingByQuantity documentation requires
  `X-DIGIKEY-Account-Id` for two-legged OAuth.
- Re-ran the real adapter after the audit. `providers check` succeeded in
  documented `account_id` mode, and an uncached `TCAN1473CDRQ1` lookup used
  three requests (OAuth, pricing, ProductDetails), returned exact EUR pricing,
  the TI datasheet and Digi-Key product URL, and correctly verified 2,940 units
  of cut-tape stock.

## 2026-07-30

- Added `export ec-bom`: renders one validated design as a Eurocircuits
  assembly BOM (semicolon-separated, CRLF, upload-ready column set
  Item;Quantity;Designators;Manufacturer;MPN;Description;Value;Package;
  Mounted;Comment). Credential-free, exactly one design per file,
  never overwrites, JSON envelope reports absolute path, size, SHA-256,
  and line count. The design schema gained optional `designators`,
  `value`, `mounted`, and `comment` part fields (strict validation
  extended; empty designators rejected). Capabilities advertise
  `export ec-bom` and the `ec-bom-csv` artifact format; unit and
  command-contract tests cover rendering (quoting, CRLF, mounted
  default) and the overwrite refusal; `make check` passes. Verified
  end-to-end against a real 91-line design.

- Fixed the Digi-Key zero-stock defect (TODO provider-adapters entry): the
  quantity-pricing endpoint reports `QuantityAvailable` 0 even for
  well-stocked parts, so every lookup read false zero availability.
  Confirmed live against TCAN1473CDRQ1 — pricing said 0 while
  ProductDetails and digikey.com said 2,940 (cut tape, MOQ 1).
- Stock truth now comes from ProductDetails: `ProductDocuments` became
  `ProductInformation` (document links + product quantity + per-variation
  `QuantityAvailableforPackageType` map), one fetch per lookup serves both
  stock verification and document links. `applyStock` verifies every
  normalized plan per SKU with leg aggregation; a missing variation
  quantity is UNKNOWN (`stock_unknown`), never zero. The pricing
  endpoint's `QuantityAvailable` is no longer consulted.
- Fixtures now mirror the live endpoints (pricing always 0, details carry
  variations); added `applyStock` unit coverage (aggregated legs,
  shortage, unknown) and a resolver `stock_unknown` test. Bumped the
  cache adapter version to `digikey-normalized-v2` so stale v1 entries
  with wrong zero availability can never be served. `make check` passes;
  live verification returns `priced` / `2940 available` with a selected
  plan for the reference part.

## 2026-07-29

- Archived the complete Python implementation under `legacy/` while preserving
  the Git repository, root `.env`, and project license.
- Started the native Go rewrite at version `3.0.0-dev`.
- Added a standard-library-first CLI with JSON-only machine output,
  `capabilities --full`, provider configuration discovery, embedded schemas,
  and strict design validation from files or stdin.
- Added safe non-evaluating `.env` loading, typed contracts, tests, and a
  host-native build helper.
- Verified the foundation with `gofmt`, `shellcheck`, `go test ./...`,
  `go vet ./...`, schema checks, stdin/file validation, and error-path smoke
  tests.
- Built a stripped, self-contained 2.1 MB macOS arm64 executable at
  `bin/bom-builder`; it requires no Go or Python installation at runtime.

## 2026-07-30

- Raised the rewrite baseline to Go 1.25 and pinned build, test, race, and vet
  workflows to the latest Go 1.25 patch (`go1.25.12`). Upgraded the pure-Go
  SQLite driver from `modernc.org/sqlite v1.46.0` to the current stable
  `v1.55.0`, retained its upstream-certified transitive dependency set, and
  verified both the full application suite and an in-memory SQLite test with
  the exact pinned toolchain.
- Added a native versioned SQLite cache for normalized Mouser, Digi-Key, TI,
  and NXP results. It uses WAL, bounded TTLs, adapter and market/account context
  hashes, payload checksums, explicit corruption failures, owner-only database
  permissions, and source-request provenance without storing credentials or raw
  provider responses.
- Integrated `prefer`, `refresh`, `only`, `offline`, and `off` cache policies
  into lookup, BOM pricing, document discovery, and alternative sourcing.
  Cache-only and offline modes do not initialize provider clients, so a cached
  workflow needs neither live credentials nor an NXP browser.
- Added machine-readable `cache status|list|verify|prune` commands and a public
  cache schema. Pruning requires an exact preview token bound to the current
  provider/key/payload set; inspection never emits stored provider payloads.
- Added exact six-place fixed-point money; prices serialize as decimal strings
  and mixed-currency totals fail closed.
- Ported deterministic BOM aggregation and stock-aware single/mixed packaging
  purchase-plan optimization.
- Added the native Mouser v2 adapter with bounded retries, backup-key rotation,
  sanitized failures, strict manufacturer/MPN matching, and review-required
  non-exact candidates.
- Added native `lookup`, `price`, and `providers check` commands with deadlines,
  run metadata, request counts, safe partial results, and exit code `3`.
- Preserved Mouser datasheet/product URLs and verified the adapter against the
  configured live API: health succeeded, exact pricing succeeded, and the
  three-line example returned two safe prices plus one explicit unresolved
  line.
- Added the native Digi-Key Product Information V4 adapter with two-legged
  OAuth token reuse, required account and locale headers, bounded retries,
  quota metadata, exact fixed-point quantity pricing, composite SKU plans, and
  product/datasheet links.
- Generalized `lookup` and `price` to query explicitly selected or automatically
  discovered native providers. Every normalized offer is retained, and only
  exact, stock-verified, common-currency plans participate in selection.
- Extended the public run envelope and output schema with per-provider request
  counts and multi-offer provenance.
- Verified the configured live Mouser and Digi-Key APIs together: both health
  checks passed, Digi-Key authentication used the documented account-ID mode,
  a cross-provider lookup returned both offers, and an out-of-stock Digi-Key
  result correctly remained an unselected shortage with working document links.
- Added the native TI Store V2 adapter with client-credentials OAuth token
  reuse, exact fixed-point price breaks, stock and order-limit enforcement,
  lifecycle review, package metadata, product links, bounded retries, and
  provider-specific applicability.
- Removed the legacy TI runtime dependency on system `curl`: the production TI
  edge accepts the native Go HTTP transport.
- Live-verified TI health and an exact `TMP421AQDCNRQ1` lookup. Authentication,
  real-time inventory, requested-currency pricing, lifecycle data, and a
  stock-verified purchase plan all succeeded in two requests.
- Added the native NXP Store adapter using dependency-free Chrome DevTools
  Protocol transport over a private headless Chrome/Edge process. Structured
  store responses are schema-checked; orderable-part matches, stock, price
  breaks, MOQ, package multiples, and product links are normalized
  conservatively.
- Live-verified NXP discovery and health with `KW47B42ZB7AFTBT`. A base-device
  lookup resolved to the orderable tray SKU, reported 940 units of stock, MOQ
  1300, package multiple 260, and three USD price tiers; the optimizer correctly
  refused to select a plan because the available stock could not satisfy MOQ.
- Added native `documents list` and `documents fetch` commands. Link discovery
  retains provider provenance and prioritizes manufacturer-hosted PDFs.
  Downloads enforce HTTPS/public-network targets, safe redirects, a bounded
  size, PDF signature validation, exclusive output creation, and SHA-256
  artifact metadata.
- Live-fetched and independently verified the 420,264-byte Yageo resistor
  datasheet surfaced by Mouser; the CLI and system checksum both produced
  `082584f002a340558f9afcc0189ccad51ec3d3d746829e900dd5b555d2c19180`.
- Added built-in root and per-command help with copyable shell/agent examples;
  help and version output no longer depend on loading project configuration.
- Added the native `alternatives` command and an embedded strict request schema
  for resistors, capacitors, and inductors. The deterministic engine fails
  closed on missing critical fields and returns a field-by-field
  equal/better/worse/unknown matrix with explicit rejected reasons.
- Added stock-aware candidate sourcing and ranking. Electrically incompatible
  candidates consume no provider requests; viable candidates retain all offer,
  MOQ, packaging, stock, and exact-price provenance. Mixed currencies are not
  compared, and every recommendation remains engineering-review-required.
- Live-verified a Yageo 10 kOhm 0402 baseline against a proposed Vishay
  `CRCW040210K0FKED` candidate. Mouser and Digi-Key sourcing ran in seven total
  requests; Mouser reported over 6.3 million units and a safe EUR 1.40 plan for
  100 pieces. The command returned the candidate for review rather than
  automatically approving it.
