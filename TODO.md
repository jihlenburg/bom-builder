# Go Rewrite Roadmap

The Go implementation intentionally starts with a small, truthful capability
surface. Commands are advertised only after their contracts and tests exist.
This list carries forward every unfinished CLI, substitution, document, safety,
and quality item from the Python roadmap.

## CLI and machine interface

- [x] Native Go executable with no interpreter or runtime dependency
- [x] Versioned JSON output and stable process exit codes
- [x] Strict design loading from files and stdin
- [x] Embedded input, output, and provider JSON Schemas
- [x] `capabilities --full` and safe provider configuration discovery
- [x] Add focused `price`, `lookup`, and `providers check` commands
- [x] Add focused `cache status|list|verify|prune` commands
- [x] Add focused `resolutions approve|list|history|revoke` commands
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

## Interactive mode

- [x] Start the interactive terminal mode: `bom-builder interactive` manages
      the resolutions store (browse, detail, audit history, approve form,
      preview-bound revoke) on the pure-Go Bubble Tea stack, with a
      non-TTY refusal (`INTERACTIVE_TTY_REQUIRED`) and a terminal-free
      model test suite
- [ ] Interactive live pricing runs: stream `price`-style sourcing progress
      into the interface (the archived Python TUI's parts table, cost
      panel, and status bar)
- [x] Interactive resolver flow: source one part from inside the interface,
      present every normalized candidate (match method, stock, price,
      review status), and seed the approve form with the chosen candidate —
      the successor of the Python resolver modal. The approver is never
      prefilled; choosing a candidate cannot clear engineering review
- [ ] Interactive alternatives review: browse the compatibility matrix and
      hand a chosen candidate to the approve form

## Core sourcing engine

- [x] Deterministic BOM aggregation with provenance-preserving references
- [ ] Complete the money/FX layer; exact six-place fixed-point money is
      implemented, while dated cross-currency conversion remains
- [ ] Complete optimizer verification; the native purchase-plan optimizer and
      the Python-era behavioral cases are ported, while serialized
      cross-language golden fixtures remain. The `legacy/` tree has been
      removed, so generating fresh cross-language fixtures now requires
      checking the Python implementation out of Git history
      (`git show 9f54a22:legacy/optimizer.py`)
- [x] Define canonical ordering for aggregated parts and Mouser price breaks
- [x] SQLite normalized-response cache with migrations, WAL, expiry, checksums,
      adapter/context identity, and source-request provenance
- [x] Concurrent-safe approved-resolution store with locking, atomic writes,
      audit history, and exact destructive-operation previews (SQLite/WAL;
      one active resolution per demand, supersede on re-approval,
      preview/apply revocation)
- [ ] Consume active resolutions during `lookup`/`price`/`alternatives` so an
      approved replacement is sourced automatically and reported with its
      resolution identity
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
- [x] `microchip` availability provider via the PUBLIC Product API — IMPLEMENTED + LIVE-VERIFIED 2026-07-31 (see LOGBOOK) —
      VERIFIED LIVE 2026-07-31 (JI session): documented under Partner Data
      Exchange (ProductAPI-user-guide.html), endpoint
      `GET https://www.microchip.com/designresources/product-catalog/api/productInfo`
      `?part=<prefix,min 3 chars>&pagesize&pagenumber`, NO credentials,
      plain curl + browser UA returns 200 in <1 s. Response: paginated
      records with `part_number`, `instock_quantity`, `lead_time_weeks`,
      `lifecycle_status` (REL/EOL), `minimum_order_quantity`,
      `order_multiple`, `packaging_type`, `datasheet_url`, MSL, export/
      compliance data — NO pricing. Live probe on DSPIC33AK512MPS506
      returned all 15 variants and matched the manual MicrochipDirect
      check exactly (-E/PT 960 in stock TRAY mult 160; -I/PT 0; /5L EOL).
      Adapter design: availability/lifecycle EVIDENCE provider (no
      purchase plans — priceless offers stay review-level evidence),
      prefix query on the base part + exact-match filter, cache-first,
      gentle request budget (no documented rate limits — assume small).
      The credentialed Purchasing API (business account, adds
      pricing/ordering) remains the later upgrade path if needed.
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
- [ ] Complete decision persistence; recommended-for-review and rejected
      reasons are in JSON, and the `resolutions` store now records approvals
      with an append-only audit history, while report generation and the
      alternatives-to-approval hand-off remain
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
      (the `windows/amd64` build+vet gate already runs in `make check`;
      release packaging remains)
- [ ] Add checksums, SBOMs, signing, and macOS notarization
- [ ] Add shell completions and Claude/Codex/shell/CI automation guides

## Code review remediation (2026-07-31)

The review is remediated and its report has been removed from the working
tree. Findings, file:line detail, and fix order remain available in Git
history (`git show 1647847:CODE_REVIEW_2026-07-31.md`).

- [x] C1 `money.Parse` fraction overflow wrapping to a negative Decimal
- [x] C2 digit-free separator input parsing as a zero price
- [x] C3 `export ec-bom` artifact failing the published output schema
      (new `exportArtifact` def, envelope `artifact` oneOf; bonus: missing
      `demand`, `pricing_strategy`, `order_plan` property descriptions)
- [x] Money: reject letters embedded between digit runs ("1e5", "2 for 1.50")
- [x] Money: reject ambiguous lone-comma thousands ("$1,234"); sign-safe
      `String()`; adversarial money test suite
- [x] Input schema describes all three accepted document shapes; loader
      rejects empty array/wrapper documents per document
- [x] NXP: string prices reach `money.Parse` verbatim (EU comma-decimal
      1000x fix); integer fields keep US-grouping tolerance
- [x] NXP: accept "NXP USA Inc.", "N.V.", "B.V." manufacturer spellings
- [x] Mouser: loose-match squash no longer masks shortage/stock_unknown/
      unavailable states
- [x] Cache: `Put` round-trips through `decodeRow`, refusing entries the
      read path would reject (self-poisoning footgun)
- [x] NXP failure model: transient errors fail one lookup only; schema drift
      still disables for the run; dead browser processes are dropped and
      relaunched; body fetch waits for `Network.loadingFinished`; operations
      serialized under an operation mutex (CDP single-consumer documented);
      scripted fake-browser test harness added
- [x] `.env` trust model: endpoint/browser/cache-path keys must come from the
      process environment (refused when a `.env` would introduce them);
      malformed `.env` now exits 2 under the invoked command; lowercase keys
      accepted; UTF-8 BOM stripped; unbalanced quotes are explicit errors
- [x] Optimizer: explicit refusal for cross-currency plan comparison
- [x] Sourcing: unknown statuses count as INTERNAL_CONTRACT_ERROR; fallback
      path strips leaked SelectedPlans
- [x] Aggregation: AGGREGATION_METADATA_CONFLICT warnings on conflicting
      description/package/pins; empty fields filled from later designs
- [x] CLI: uniform unknown-flag rejection (lookup, validate, documents list,
      alternatives)
- [x] Deterministic field order for alternatives validation errors
- [x] Cache: session pragmas ride in the DSN (per-connection busy_timeout);
      CacheStatus reports the database's actual `PRAGMA user_version`
- [x] Provider retry/auth-path tests (DigiKey 401-refresh, 429/5xx backoff,
      oversized-body cap; Mouser 429 backoff; TI 401-refresh); dotenv edge
      tests; sanitizer truncation now UTF-8-safe in all three adapters
- [ ] Remaining CLI error-path tests (provider-config errors, bounds,
      `--pretty` shape, empty-stderr assertion)
- [ ] Remaining Minor findings per review file (Retry-After handling, NXP
      PartDetail fixed sleep, Mouser short-MPN state, diacritic folding,
      providerutil dedup, help/typo polish, cache exit-code split, eC-BOM
      quantity/designator consistency and formula guard)
