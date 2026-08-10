# Logbook

## 2026-08-10 (local web UI)

- Added `bom-builder serve` (`internal/webui`): the resolutions manager and
  resolver flow in a browser, parallel to the terminal interface. The
  dependency budget for the entire feature is the Go standard library —
  the frontend is hand-written HTML/CSS/JS embedded with `go:embed`, no
  Node toolchain, no npm tree, no build step, no external requests. This
  was the deciding argument against starting with a Wails-style desktop
  shell: the same UI can be wrapped natively later, but today nothing
  about the build, the `windows/amd64` gate, or `CGO_ENABLED=0` changes.
- Security posture (defense in depth for a single-human localhost tool):
  loopback-only listener enforced at the CLI (`LISTEN_NOT_LOOPBACK` for
  anything else); a 256-bit per-session bearer token in the printed URL,
  required on every API request (constant-time compare) so a malicious
  website cannot drive the API cross-origin; loopback Host-header
  enforcement against DNS rebinding; loopback-origin enforcement for
  state-changing browser requests; strict CSP (`default-src 'none'`, no
  inline script); `no-store` responses. The frontend keeps the token in
  memory only and strips it from the address bar and history immediately.
  The web JSON API is an internal contract with the embedded frontend —
  the public machine interface remains the CLI.
- Protocol note: `serve` still emits exactly one JSON document on stdout —
  the startup envelope with the tokenized URL and database path — then
  serves until Ctrl+C with diagnostics on stderr, so agents can launch it
  and hand the URL to a human.
- Approve/revoke semantics are identical to the CLI and TUI: named person
  required, supersede on re-approval, revocation via the content-bound
  preview/apply handshake (the frontend's two-click "Preview revoke" →
  "Confirm revoke" carries the real apply token; a concurrent change
  surfaces as a 409). The resolver endpoint reuses the same injected
  lookup runner as the TUI, per-lookup runtimes included.
- Verified beyond unit tests: the Go test suite covers the API lifecycle,
  auth, rebinding/origin rejection, and validation with httptest, and a
  headless-Chromium run drove the real page end to end (approve via form,
  table render, detail, revoke preview/confirm, inactive toggle) with a
  zero-console-error assertion. That browser run caught two real issues
  the Go tests could not: success messages appeared before the table
  refresh (now the message is the completion signal, shown after refresh)
  and the favicon request 404-ed against the strict CSP (now answered
  with 204). The browser harness lives in the session scratchpad, not the
  repository; a committed Playwright suite would need the npm toolchain
  the feature deliberately avoids.

## 2026-08-10 (interactive resolver flow)

- Added the resolver flow to interactive mode — the successor of the Python
  TUI's resolver modal, now wired to the audited resolutions store instead
  of an in-run choice. Pressing "l" opens a lookup form (part, manufacturer,
  quantity, providers with `auto` default); the lookup runs asynchronously
  through the injected `tui.LookupRunner` while the interface stays
  responsive, and a sequence counter discards results that arrive after the
  user abandoned the lookup. The candidates view lists every normalized
  offer with provider, safe/review marker, MPN, SKU, match method, stock,
  and unit price; choosing one seeds the approve form with the original
  demand, the replacement identity, the provider SKU, and an evidence note
  (provider, match method, stock, unit price, review flag). The
  `approved_by` field is NEVER prefilled and gets the focus — choosing a
  candidate cannot clear engineering review; a person still has to sign it.
- The CLI injects the runner so `internal/tui` never constructs provider
  clients (and the cli→tui import direction stays acyclic). The runner
  reuses the exact `lookup` semantics: env-driven cache policy, the same
  provider-selection rules, `newProviderRuntimes`, multi-resolver sourcing.
  Runtimes are built per lookup and torn down afterwards — no idle browser
  processes or token state between resolutions, and configuration changes
  are picked up mid-session. Without a runner (nil), the interface hides
  the resolver key entirely, which the tests assert.
- Model tests cover the full happy path (lookup → candidates → prefilled
  approve → stored record with the chosen SKU), validation, runner errors
  returning to the form visibly, the late-result guard, and the
  never-prefill-approver invariant, all driven through Update/View with a
  fake runner — still no terminal required.

## 2026-08-10 (interactive mode, first slice)

- Started the interactive terminal mode: `bom-builder interactive`, a
  full-screen manager for the approved-resolutions store (list with
  active/inactive toggle, record detail, per-demand audit history, an
  approve form, and revocation that runs the content-bound preview/apply
  handshake under the hood — a concurrent change between preview and apply
  surfaces as a stale-preview error, exactly like the CLI). The rules match
  the JSON commands: every decision requires the acting person's name.
- Framework: the charmbracelet Bubble Tea stack (`bubbletea` v1.3.10,
  `bubbles`, `lipgloss`) — deliberate new dependencies, documented in the
  README. Chosen because it is pure Go (no cgo, so the `windows/amd64`
  check gate still passes and `CGO_ENABLED=0` release builds are
  unaffected), supports Windows terminals, and keeps Update/View as pure
  functions. The whole interface is unit-tested without a terminal by
  driving the model with key messages and asserting on rendered views and
  store side effects. A GUI toolkit (Fyne class) was not chosen: it would
  drag in cgo/OpenGL and break the cross-compilation gates; the archived
  Python implementation's interactive mode was a TUI as well.
- Protocol boundary kept explicit: `interactive` is the one command exempt
  from the machine-first JSON stdout contract. It refuses to start when
  stdin or stdout is not a terminal and emits a machine-readable
  `INTERACTIVE_TTY_REQUIRED` error instead — scripts and agents can never
  hang on an invisible screen. `mattn/go-isatty` (already in the tree as an
  indirect dependency) is now direct for that gate.
- Test-clock lesson recorded: the model tests initially froze `now`, and an
  identical re-approval collided on the content+time-derived resolution ID —
  which is the store behaving correctly. The tests now use a deterministic
  advancing clock.
- Later slices tracked in TODO.md: live pricing runs in the interface, the
  resolver flow for review-required lookups (successor of the Python
  resolver modal), and interactive alternatives review.

## 2026-08-10 (resolutions store, Windows target)

- Implemented the approved-resolution store and the `resolutions
  approve|list|history|revoke` commands — the roadmap's decision-persistence
  keystone. A resolution records that a NAMED PERSON cleared engineering
  review for one replacement of one original demand; the tool never clears
  review itself (`approved_by` is mandatory, and so is `--revoked-by`).
  Design: SQLite/WAL (`internal/resolutions`, schema v1) rather than the
  legacy JSON-plus-rename approach, because SQLite provides the locking and
  atomicity the TODO item demanded on every platform including Windows,
  where the Python store needed an `os.name != "nt"` special case. One
  active resolution per demand key (case-insensitive manufacturer+part),
  enforced by a partial unique index; a new approval supersedes the old
  record in the same transaction, revocation retires it, and nothing is
  ever deleted — every change appends to an audit-event table. Records
  carry a SHA-256 checksum and are round-trip-validated on write exactly
  like lookup-cache entries. `resolutions revoke` reuses the cache-prune
  preview/apply-token pattern: the token binds the previewed record's
  content hash, so any concurrent change invalidates it. Strict JSON
  approval requests (embedded `resolutions` schema, `schema resolutions`,
  bundle + capabilities updated; `planned_commands` is now empty), optional
  provider-SKU pinning, and https+SHA-256 evidence documents. The database
  lives under `os.UserConfigDir()` — durable decisions, not reclaimable
  cache — overridable via `--resolutions-db`/`BOM_BUILDER_RESOLUTIONS_DB`,
  with the variable added to the restricted `.env` key list like
  `BOM_BUILDER_CACHE_DB`. Read-only commands never create the database.
  Consuming active resolutions during `lookup`/`price`/`alternatives` is
  the next slice (tracked in TODO.md).
- Made Windows a verified compilation target. `make check` now includes a
  `windows` gate (`GOOS=windows GOARCH=amd64` build + vet of every
  package). The audit found and fixed two real Windows defects: (1) both
  SQLite stores built their DSN with `url.URL{Path: absolute}`, which
  turns `C:\...` into `file://C:%5C...` — the drive letter lands in the
  URI authority and SQLite rejects it; a shared `sqliteDSN` helper now
  emits canonical `file:///C:/...` on Windows and byte-identical DSNs on
  POSIX. (2) NXP browser discovery knew only macOS and PATH lookup; it now
  probes the Windows Program Files / LocalAppData install paths for
  Chrome/Edge/Chromium and uses PATHEXT-aware `chrome`/`msedge` names.
  One honest limitation gated instead of papered over: the NXP adapter's
  DevTools connection rides on inherited file descriptors 3/4
  (`--remote-debugging-pipe` via `exec.Cmd.ExtraFiles`), which os/exec
  does not support on Windows. `nxp.New` fails fast with a configuration
  error there, and provider discovery reports the new
  `unsupported_platform` status (providers schema enum extended) rather
  than claiming readiness and dying mid-run. CRLF `.env` files were
  already handled (bufio.ScanLines strips `\r`); `Process.Kill`,
  `filepath` usage, and 0o600 chmods are portable as-is.

- Declared the project license explicitly. The repository already carried the
  verbatim GPL-3.0 text in `LICENSE` from its first commit, but nothing named
  it: no copyright holder, no README section, no source-file notices. Kept
  GPL-3.0 and wired it in properly — a `## License` section in `README.md` with
  the standard GPLv3 notice under `Copyright (C) 2026 Joern Ihlenburg`, and a
  two-line `Copyright` + `SPDX-License-Identifier: GPL-3.0-or-later` header on
  all 77 Go files. The headers sit above a blank line so every package doc
  comment stays attached to its `package` clause. `LICENSE` itself is untouched:
  the GPL text must remain verbatim, so the copyright holder is named in the
  README and the source headers instead. Chose `-or-later` per the FSF's own
  "How to Apply These Terms" appendix, which is the wording already shipped at
  the end of `LICENSE`.
- Retired the archived Python implementation. `legacy/` (107 files, 3.0 MB —
  the Textual TUI, the provider modules, the pytest suite, and its recorded
  fixtures) is deleted. Nothing in the build depended on it: no Go file,
  `Makefile` target, or `scripts/build.sh` line referenced the directory, so
  removal is behavior-neutral for the CLI. Only documentation and `.gitignore`
  pointed at it. The one asset worth keeping was rescued rather than dropped:
  `legacy/designs/example_power_supply.json` is now
  `examples/example-power-supply.json`, since the README's `validate` and
  `price` examples ran against it and would otherwise have been broken
  copy-paste. `.gitignore` lost its whole "Archived Python implementation
  artifacts" block, and the README repository-layout tree was corrected while
  it was being edited — it had drifted, omitting `internal/alternatives/`,
  `internal/documents/`, `internal/app/`, `examples/`, and `scripts/`.
- Noted the one real cost of the deletion in `TODO.md`: the open
  "serialized cross-language optimizer golden fixtures" item was to be
  generated by running the Python optimizer against the Go one. That is still
  possible, but the source must now come out of Git history
  (`git show 9f54a22:legacy/optimizer.py`) rather than the working tree.
- Removed `CODE_REVIEW_2026-07-31.md` from the working tree. The review it
  reported was fully remediated in `9f54a22`, so the file was a spent artifact
  rather than live documentation. The report is unchanged in Git history
  (`git show 1647847:CODE_REVIEW_2026-07-31.md`); the earlier 2026-07-31 entry
  below still refers to it by name, and the pointer in `TODO.md` now names the
  history command instead of the deleted path.

## 2026-07-31

- Added the `microchip` provider: a credential-free availability/lifecycle
  EVIDENCE adapter over Microchip's public Product API (Partner Data
  Exchange). Offers carry factory-direct stock, lead time, lifecycle
  status, MOQ/order-multiple, datasheet link, and a microchipDIRECT
  product URL — but no pricing, so they are always review-required and
  can never become a selected plan (statuses: review with
  MANUFACTURER_EVIDENCE_ONLY or LIFECYCLE_WARNING, shortage,
  stock_unknown, not_found; non-Microchip/Atmel demands are
  not_applicable). Unparseable stringly-typed quantities stay unknown,
  never zero (the Digi-Key lesson applied at design time). Base-part
  fallback query with strict exact-match filtering refuses to silently
  pick a variant. Wired end to end: provider kind "manufacturer"
  (schema enum extended, capabilities gained a `manufacturers` list),
  explicit selection only (never `--providers auto`), lookup-cache
  adapter version `microchip-normalized-v1` with a products-URL context
  hash, endpoint override restricted from `.env` like every other
  endpoint key, live health check, and README/help documentation.
  Fixture-based unit, resolver, and CLI-contract tests; `make check`
  passes. Live-verified: health ok (606 ms, 15 catalog records) and
  `lookup DSPIC33AK512MPS506-E/PT --providers microchip` returns
  review/MANUFACTURER_EVIDENCE_ONLY with 960 in stock, REL, 6-week
  lead, MOQ 1, multiple 160 — matching the manual microchipDIRECT
  check and the prior raw-API probe exactly.

- Ran a full four-slice code review of the Go codebase at `a9e6b9d` (CLI +
  contract, provider adapters, pricing/sourcing core, cache + config), with
  all Critical findings independently re-verified against source. Baseline
  `make check` green. Results: three verified Critical defects (`money.Parse`
  fraction-overflow wrap to negative, digit-free input parsing as zero price,
  `export ec-bom` artifact failing the published output schema), ~20 Important
  findings concentrated in the NXP browser adapter's failure model and money
  text normalization, plus consistency and test-coverage gaps. Full ranked
  findings with file:line references and a suggested fix order recorded in
  `CODE_REVIEW_2026-07-31.md`.
- Remediated the review's fix-order head with strict TDD (failing test first
  for every fix): `money.Parse` no longer wraps past MaxInt64 into a negative
  Decimal, no longer accepts digit-free input as a zero price, and now rejects
  letters between digit runs ("1e5") and ambiguous lone-comma thousands
  ("$1,234") as explicit errors; `String()` renders broken-invariant negative
  values readably. The output schema gained an `exportArtifact` def (export
  envelopes now validate), the missing `demand`/`pricing_strategy`/`order_plan`
  property descriptions, and the input schema now describes all three accepted
  document shapes while the loader rejects empty array/wrapper documents. The
  NXP parser passes string prices to `money.Parse` verbatim (EU "1.234,56" was
  silently mispriced 1000x) and accepts "NXP USA Inc."/"N.V."/"B.V."
  spellings; the Mouser resolver no longer squashes shortage/stock_unknown/
  unavailable into a bare review status on loose matches; the lookup cache's
  `Put` now round-trips every candidate row through `decodeRow` and refuses
  entries the read path would reject, closing the self-poisoning path. A new
  schema meta-test enforces required⊆properties across all five contracts and
  a new `store_test.go` starts store-level coverage. `make check` (unit, race,
  vet, build) passes; remaining findings tracked in `TODO.md`.
- Completed the remainder of the review's Important tier, all TDD. NXP browser
  adapter failure model: transient timeouts now fail only their lookup (run-
  level disable is reserved for confirmed schema drift), a dead Chrome process
  is dropped and relaunched on the next lookup, the response body is fetched
  only after `Network.loadingFinished` (closing the "No data found for
  resource" race), and Search/PartDetail are serialized over the single-
  consumer CDP transport — all proven against a new scripted fake-browser
  harness driving the real transport code. Established the `.env` trust
  model: a checkout-local `.env` may supply credentials and preferences but
  may not introduce endpoint URLs, the NXP browser path, or the cache DB path
  (loud refusal naming the key; inert when the operator already exported the
  override); malformed `.env` files now exit 2 under the invoked command,
  lowercase keys parse, a UTF-8 BOM is stripped, and unbalanced quotes are
  explicit errors. The optimizer refuses cross-currency plan comparison; the
  sourcing summary counts unknown statuses as INTERNAL_CONTRACT_ERROR and the
  fallback path strips leaked SelectedPlans; aggregation emits
  AGGREGATION_METADATA_CONFLICT warnings and fills empty metadata; lookup,
  validate, documents list, and alternatives reject flag-shaped positionals;
  alternatives validation errors are deterministic; cache session pragmas ride
  in the DSN so replacement connections keep busy_timeout, and CacheStatus
  reports the database's actual user_version. Added provider retry/auth tests
  (Digi-Key 401-refresh, 429/5xx backoff, 8 MB body cap; Mouser 429 backoff;
  TI 401-refresh) and made sanitizer truncation UTF-8-safe in all three HTTP
  adapters. `make check` passes; the built binary was smoke-tested against
  the new flag guard and `.env` refusal.
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
