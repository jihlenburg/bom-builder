# BOM Builder

BOM Builder sources and prices electronic BOMs across Mouser, Digi-Key, and
the TI Store. It is a native Go CLI with a machine-first JSON contract, plus
two human interfaces over the same engine: a full-screen terminal mode and a
local web UI. Every automated result stays honest about what still needs
engineering review, and human approvals are recorded durably with a full
audit trail.

Latest release: `v3.0.1`

## What works today

- Single self-contained executable; no runtime or interpreter required, and
  the full test suite runs on Linux and Windows in CI
- Machine-readable capability and schema discovery; stable JSON stdout and
  process exit codes
- Live Mouser, Digi-Key, and TI Store sourcing: lookup, quantity pricing,
  stock-aware purchase plans, health checks, and multi-provider comparison
  with every normalized offer retained in JSON
- Exact six-place decimal prices; totals in a chosen currency via dated ECB
  reference quotes
- Credential-free Microchip factory evidence (availability and lifecycle,
  no pricing, always review-required)
- Datasheet and product links, with verified HTTPS-only PDF downloads
- Stock-aware resistor, capacitor, and inductor alternatives with a
  field-by-field compatibility matrix
- Versioned SQLite lookup cache with safe inspection and preview/apply
  pruning; cache-only and offline runs need no credentials
- Durable, audited human-approved resolutions that `lookup` and `price`
  consume automatically
- Interactive terminal mode and a local web UI for browsing, resolving, and
  approving
- Eurocircuits assembly-BOM CSV export with formula-injection protection

Buyer-oriented report files are still on the roadmap (`TODO.md`).
`bom-builder capabilities --full` is the authoritative way for a coding agent
to discover what the installed build can do.

## Install

Prebuilt binaries for Linux, macOS, and Windows (amd64 and arm64) are
attached to each [GitHub release](https://github.com/jihlenburg/bom-builder/releases),
together with a `SHA256SUMS` file. Download the archive for your platform,
verify the checksum, and put the binary on your `PATH`; it has no runtime
dependencies.

With a Go toolchain installed, you can also build and install directly:

```bash
go install github.com/jihlenburg/bom-builder/cmd/bom-builder@latest
```

## Build from source

BOM Builder uses the Go 1.25 language and module baseline. Reproducible
development commands pin the latest supported Go 1.25 patch, `go1.25.12`.
Install any Go toolchain with automatic toolchain downloads enabled, then run:

```bash
make build
./bin/bom-builder --version
```

Or use Go directly:

```bash
go build -trimpath -o bin/bom-builder ./cmd/bom-builder
```

Direct dependencies are few and each has a stated reason:
`modernc.org/sqlite` (pure-Go SQLite for the lookup cache and resolutions
store), the charmbracelet Bubble Tea stack (`bubbletea`, `bubbles`,
`lipgloss` for the terminal interface; cgo-free and Windows-capable), and
`mattn/go-isatty` (terminal detection so interactive mode can refuse
pipelines). Use, for example, `make GO_TOOLCHAIN=go1.26.5 check` only when
deliberately testing another compiler.

The resulting executable does not require a Go installation, an interpreter, or
any other runtime on the target machine.

Windows is a supported target, verified twice over: `make check`
cross-compiles and vets every package for `windows/amd64` (`make windows`
runs that gate alone), and CI additionally runs the full unit-test suite
and a native build on a real Windows runner for every push. Release
archives include Windows binaries for amd64 and arm64.

Continuous integration runs gofmt and the complete `make check` gate on
every push and pull request; tagged releases are built and published by a
separate workflow after passing the same gate.

## Agent discovery

```bash
./bin/bom-builder capabilities --full --pretty
./bin/bom-builder providers list --pretty
./bin/bom-builder providers check --providers mouser,digikey,ti --live --pretty
./bin/bom-builder schema input --pretty
./bin/bom-builder help documents
```

`capabilities --full` returns the implemented command manifest, planned
commands, provider readiness, runtime platform, and all public JSON Schemas in
one JSON document.

## Validate a design

```bash
./bin/bom-builder validate examples/example-power-supply.json --pretty
generate-design | ./bin/bom-builder validate -
```

Validation is strict: unknown fields, empty identifiers, invalid quantities,
trailing JSON values, and oversized input documents are rejected.

## Lookup and price

```bash
./bin/bom-builder lookup RC0402FR-0710KL \
  --manufacturer Yageo \
  --quantity 950 \
  --providers auto \
  --pretty

./bin/bom-builder price examples/example-power-supply.json \
  --units 100 \
  --attrition 0.02 \
  --providers mouser,digikey,ti \
  --pretty
```

Prices are JSON strings such as `"0.005000"` so calling agents never receive
binary floating-point approximations. An offer becomes `selected_plan` only
when the MPN is exact and reported stock covers the purchase quantity.
Non-exact candidates remain review-required.

`--providers auto` uses every configured native provider. Exclusions narrow
that automatic set: `--providers -ti` (or `auto,-ti`) means "auto without
TI". With an explicit list (`--providers mouser,digikey`), simply leave the
unwanted provider out; exclusions cannot be mixed with explicit names.
Explicit selection never requires credentials for providers outside that
selection.

Every provider is declared once in an in-code registry
(`internal/provider/registry.go`). Construction, discovery, health checks,
selection, and capability lists all derive from it, so adding the next
distributor is one adapter package plus one registry entry.

TI direct-store lookups are automatically skipped for non-TI manufacturers.
Known non-active lifecycle states and generic-to-orderable part resolutions
remain review-required; stock and TI Store order limits must both cover the
purchase plan before it can be selected.

When more than one provider is selected, each normalized candidate is returned
in `parts[].offers`, while `parts[].offer` is the chosen safe plan. Plans in
different currencies are never compared for selection, and without an explicit
conversion target the summary total exists only when every selected plan
already shares one currency.

## Currency conversion

```bash
bom-builder price design.json --units 100 --currency EUR --pretty
```

`--currency <ISO>` on `lookup` and `price` converts the summary totals using
the European Central Bank's dated daily reference quotes, which need no
credentials. The summary reports `quote_source` and `quote_date` next to the
converted `total_cost`, and per-part plans keep their native currency.
Conversion math is exact: integer micro-units with a single half-to-even
rounding at the sixth decimal place, never binary floating point. Failures
are explicit. Unreachable quotes fail the run before any provider is
contacted (`FX_QUOTES_UNAVAILABLE`), and a selected plan in a currency the
quotes do not cover omits the whole total with `FX_CONVERSION_FAILED`
rather than summing a convertible subset.

## Microchip factory evidence (no pricing)

```bash
./bin/bom-builder lookup DSPIC33AK512MPS506-E/PT \
  --manufacturer Microchip \
  --quantity 10 \
  --providers microchip \
  --pretty
```

The `microchip` provider queries Microchip's public, credential-free
Product API for factory-direct availability, lead time, lifecycle status
(REL/EOL), MOQ/order-multiple, and datasheet links. The catalog carries no
pricing, so results are always review-required evidence with exit code `3`
and can never become a selected purchase plan; the offer's product URL
points at microchipDIRECT for the human order decision. The provider
applies only to Microchip/Atmel parts, is selected explicitly (never by
`--providers auto`), and participates in the lookup cache like every other
provider.

## Persistent lookup cache

`lookup`, `price`, `documents list`, and `alternatives` cache successful
normalized provider results for 24 hours by default. The cache stores no API
keys, tokens, or raw provider responses. Keys bind the provider adapter version,
part, manufacturer, quantity, package constraints, and a hash of the applicable
market/account context.

```bash
# Default: use a fresh hit, otherwise contact the provider and refresh it.
./bin/bom-builder lookup RC0402FR-0710KL --manufacturer Yageo \
  --cache-policy prefer --pretty

# Never initialize provider clients; accept only fresh entries.
./bin/bom-builder price design.json --units 100 \
  --cache-policy only --pretty

# Never initialize provider clients; expired entries are allowed and counted.
./bin/bom-builder price design.json --units 100 \
  --cache-policy offline --pretty
```

The policies are `prefer`, `refresh`, `only`, `offline`, and `off`. Configure
them with `--cache-policy`, `--cache-db`, and `--cache-ttl`, or the equivalent
`BOM_BUILDER_CACHE_POLICY`, `BOM_BUILDER_CACHE_DB`, and
`BOM_BUILDER_CACHE_TTL` environment variables. Run metadata reports hits,
stale hits, misses, refreshes, writes, errors, and the number of original
provider requests represented by reused entries.

Inspect and verify the database without exposing cached payloads:

```bash
./bin/bom-builder cache status --pretty
./bin/bom-builder cache list --provider mouser --include-stale --pretty
./bin/bom-builder cache verify --pretty
```

Pruning is preview/apply rather than an immediate destructive command:

```bash
./bin/bom-builder cache prune --all --pretty
./bin/bom-builder cache prune --all --apply 'sha256:<token>' --pretty
```

The token is valid only for the exact provider/key/payload set shown by the
current preview. Without `--all`, only expired entries are selected.

## Datasheets and evidence

Discover provider-supplied evidence links:

```bash
./bin/bom-builder documents list RC0402FR-0710KL \
  --manufacturer Yageo \
  --providers mouser,digikey \
  --pretty
```

The result distinguishes datasheets from product pages and marks the preferred
downloadable datasheet. Download it in a separate, explicit operation:

```bash
./bin/bom-builder documents fetch 'https://example.com/part.pdf' \
  --output ./part.pdf \
  --pretty
```

Fetching accepts only HTTPS URLs on public networks, follows at most five safe
redirects, defaults to a 25 MiB limit, verifies a PDF signature, and never
overwrites an existing file. The JSON result records the source/final URLs,
absolute output path, byte count, retrieval time, and SHA-256 digest.

## Eurocircuits assembly BOM export

```bash
./bin/bom-builder export ec-bom design.json --output ec_bom.csv --pretty
```

Renders one validated design as an upload-ready Eurocircuits assembly BOM:
semicolon-separated, CRLF line endings, columns
`Item;Quantity;Designators;Manufacturer;MPN;Description;Value;Package;Mounted;Comment`.
The optional part fields `designators`, `value`, `mounted`, and `comment`
fill the matching columns; parts without a `mounted` flag export as
mounted. The command needs no provider credentials, refuses to overwrite
an existing output file, and reports the absolute path, byte count,
SHA-256, and line count in its JSON envelope.

## Stock-aware alternatives

Alternative-part evaluation takes a strict JSON request so an LLM or a human
can supply candidate specifications extracted from known evidence:

```bash
./bin/bom-builder alternatives examples/alternatives-resistor.json \
  --providers mouser,digikey \
  --only-if-shortage \
  --pretty
```

The embedded request schema is available with:

```bash
./bin/bom-builder schema alternatives --pretty
```

The first implementation deliberately covers only resistors, capacitors, and
inductors. It checks package, dimensions when supplied, temperature range,
qualifications, and family-specific electrical ratings. Every field is
reported as `equal`, `better`, `worse`, or `unknown`; a missing critical
candidate value can never pass as compatible. Incompatible candidates are not
sent to providers. Viable candidates receive the same exact, stock-aware,
common-currency sourcing used by `lookup`.

`recommended_for_review` means “best stocked candidate supported by the
supplied values,” not “automatically approved.” The command intentionally exits
with code `3` and keeps `engineering_review_required: true`. Add fetched
document URL/hash pairs to `source_documents` to preserve evidence identity,
and verify every authored value before approval.

## Approved resolutions

Once a human has cleared engineering review, record the decision so later
runs can find it. BOM Builder never approves anything itself: every stored
resolution names the person who approved it.

```bash
bom-builder schema resolutions --pretty        # the strict request contract
bom-builder resolutions approve approval.json --pretty
bom-builder resolutions list --manufacturer "Texas Instruments" --pretty
bom-builder resolutions history --part TMP421-Q1 --pretty
```

An approval maps one original manufacturer/part to one approved replacement,
optionally pinned to a provider SKU, with optional https evidence documents
identified by URL and SHA-256. Approving a demand that already has an active
resolution supersedes the old record; nothing is deleted, and every change
lands in an append-only audit history.

Revocation follows the same preview/apply pattern as cache pruning:

```bash
bom-builder resolutions revoke --id <resolution-id> \
  --revoked-by "J. Ihlenburg" --reason "design change" --pretty
bom-builder resolutions revoke --id <resolution-id> \
  --revoked-by "J. Ihlenburg" --apply 'sha256:<token>' --pretty
```

The token is valid only while the previewed record is unchanged; a concurrent
approval or revocation invalidates it. The store is SQLite with WAL (the same
concurrency-safe machinery as the lookup cache) and lives in the per-user
configuration directory by default. Override the location with
`--resolutions-db` or the `BOM_BUILDER_RESOLUTIONS_DB` process environment
variable; like every trusted-path override, the variable is refused from
`.env`.

`lookup` and `price` consume the store: a demand with an active resolution is
sourced as its approved replacement, the BOM line keeps its original
identity, and the result carries the approval's provenance in
`parts[].resolution` (resolution id, approver, timestamp, replacement).
`--ignore-resolutions` skips the store for one run; a missing database is a
silent no-op, and read paths never create it.

Review lifting is narrow on purpose. A review-required offer becomes a
selected plan only when the human approval pinned the exact provider SKU
that came back review-required and its stock-verified plan covers the
demand; the stored approval is the completed engineering review of that
SKU. Anything looser (a different SKU or provider, unverified stock, no
pin) keeps the normal review-required outcome, and the envelope reports
`review_lifted` explicitly either way.

## Interactive mode

```bash
bom-builder interactive
```

A full-screen terminal interface for the resolutions store: browse active
and inactive records, inspect details and the audit history, approve new
resolutions through a form, and revoke active ones. The rules are identical
to the JSON commands. Every decision names the acting person, and
revocation uses the same content-bound preview under the hood.

The resolver flow (key `l`) connects sourcing to decisions. It sources one
part with the same provider, cache, and selection semantics as `lookup`,
lists every normalized candidate with match method, stock, price, and
review status, then seeds the approve form with the chosen candidate:
original part, replacement identity, provider SKU, and an evidence note.
The approver still reviews every field and must type their own name;
choosing a row never clears engineering review by itself. Provider clients
are constructed per lookup and torn down afterwards, so a resolution made
hours into a session sees current configuration and holds no token state
between lookups.

Interactive mode is the one exception to the JSON stdout protocol. It
renders for a human and refuses to start when stdin or stdout is not a
terminal, so a script or agent that reaches it by mistake receives a
machine-readable `INTERACTIVE_TTY_REQUIRED` error instead of a hanging
screen. Interactive views of live sourcing runs are a planned later slice.

## Local web UI

```bash
bom-builder serve
```

The same resolutions manager and resolver flow as the terminal interface,
rendered in your browser. `serve` prints one JSON startup document with a
tokenized URL, then serves until Ctrl+C:

```json
{"command": "serve", "url": "http://127.0.0.1:38099/?token=…", "...": "…"}
```

Open the URL to browse records and audit history, approve and revoke (with
the same content-bound preview/confirm handshake), and resolve parts through
the same provider pipeline as `lookup`. Approving from a candidate never
prefills the approver's name.

The interface is local-only, enforced in depth: the listener must be a
loopback address (non-loopback `--listen` values are refused), every API
request needs the per-session bearer token from the printed URL, the Host
header must be a loopback name (DNS-rebinding defense), state-changing
browser requests must come from a loopback origin, and the page ships a
strict Content-Security-Policy that permits no external requests. The
frontend is hand-written HTML/CSS/JS embedded in the binary, with no Node
toolchain, npm dependency tree, or build step. Its JSON API is an internal
contract between the binary and its own frontend; the public machine
interface remains the CLI.

## Configuration

Copy `.env.example` to `.env` and fill in provider credentials. Existing
process environment variables take precedence, and `.env` parsing never
evaluates shell syntax.

### Getting provider API keys

Each provider account is yours; BOM Builder only uses the credentials you
configure, and providers without credentials are simply skipped by
`--providers auto`.

- Mouser: request a Search API key through the API hub on mouser.com
  (free registration). `MOUSER_API_KEYS` accepts several keys and rotates
  through them.
- Digi-Key: register an organization and a production app at
  developer.digikey.com with access to the Product Information V4 API.
  You need the client ID, client secret, and your account ID
  (`DIGIKEY_ACCOUNT_ID`); set the locale variables to the site, currency,
  and ship-to country your account actually uses.
- TI Store: request TI store API access through your myTI account; the
  key and secret map to `TI_STORE_API_KEY` and `TI_STORE_API_SECRET`.
- Microchip evidence and ECB currency quotes need no credentials.

Prices and stock depend on your own accounts, locales, and the moment of
the request, so two users can legitimately see different numbers for the
same part. Every result envelope reports the market/account context it
was priced under.

Trusted-path and endpoint overrides (`BOM_BUILDER_CACHE_DB`,
`BOM_BUILDER_RESOLUTIONS_DB`, `BOM_BUILDER_ECB_URL`, and the per-provider
`*_URL` overrides) are refused from `.env` and must be exported in the
process environment: a checkout-local file must not be able to redirect
authenticated traffic or relocate trusted state.

Run `./bin/bom-builder help` for concise examples, or
`./bin/bom-builder capabilities --full` for the authoritative machine-readable
contract.

## Interface stability

The machine interface is the CLI: its JSON envelopes, the embedded JSON
Schemas, the stable issue codes, and the process exit codes. The rules:

- `schema_version` (currently `2.0`) changes only for breaking changes to
  published envelopes or schemas, announced in the release notes.
- New optional JSON fields may appear in any release without a version
  bump; consumers must ignore unknown fields.
- Exit codes and documented issue codes are stable identifiers.
- The web UI's JSON API is an internal contract with its own embedded
  frontend and may change at any time; do not build against it.

## Contributing and security

See [CONTRIBUTING.md](./CONTRIBUTING.md) for the development workflow and
ground rules, and [SECURITY.md](./SECURITY.md) for private vulnerability
reporting.

## Exit codes

| Code | Meaning |
|---:|---|
| `0` | Command completed successfully |
| `2` | Invalid command or design input |
| `3` | Valid result is incomplete, short, unresolved, or review-required |
| `4` | Provider, authentication, quota, or safe-total failure |
| `5` | Internal failure |

## Repository layout

```text
cmd/bom-builder/       executable entry point
internal/cli/          command parsing and JSON protocol
internal/contract/     public typed contracts
internal/config/       safe .env loading
internal/design/       design loading and validation
internal/bom/          deterministic demand aggregation and eC-BOM export
internal/money/        exact fixed-point decimal arithmetic
internal/fx/           dated ECB quotes and exact currency conversion
internal/procurement/  normalized offers and purchase-plan optimization
internal/provider/     provider registry, discovery, health, and adapters
internal/lookupcache/  versioned SQLite normalized-result persistence
internal/resolutions/  audited persistence of human-approved resolutions
internal/sourcing/     provider-independent orchestration and safe totals
internal/tui/          interactive terminal mode (resolutions manager)
internal/webui/        local web interface (loopback HTTP, embedded frontend)
internal/alternatives/ candidate loading and compatibility evaluation
internal/documents/    evidence links and safe PDF retrieval
internal/app/          build and version metadata
examples/              runnable example input documents
schemas/               public JSON Schemas and embedded access
scripts/               reproducible build helpers
.github/workflows/     CI (tests on Linux and Windows) and releases
```

## License

BOM Builder is free software licensed under the
[GNU General Public License v3.0 or later](./LICENSE).

```text
Copyright (C) 2026 Joern Ihlenburg

This program is free software: you can redistribute it and/or modify it under
the terms of the GNU General Public License as published by the Free Software
Foundation, either version 3 of the License, or (at your option) any later
version.

This program is distributed in the hope that it will be useful, but WITHOUT ANY
WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A
PARTICULAR PURPOSE. See the GNU General Public License for more details.

You should have received a copy of the GNU General Public License along with
this program. If not, see <https://www.gnu.org/licenses/>.
```

Every Go source file carries the `GPL-3.0-or-later` SPDX identifier.
