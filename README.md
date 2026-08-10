# BOM Builder

BOM Builder is a native Go CLI for electronic BOM sourcing and pricing across
Mouser, Digi-Key, TI, and NXP.

Current Go milestone: `3.0.0-dev`

## What works today

- Single self-contained executable with no runtime or interpreter dependency
- Machine-readable capability and schema discovery
- Safe provider configuration discovery without credential disclosure
- Strict design JSON validation from files or stdin
- Live Mouser and Digi-Key health checks, lookup, and quantity pricing
- Live TI Store health checks, direct inventory, lifecycle, order-limit, and
  price-break sourcing
- Live NXP Store health checks and direct inventory/pricing through a private,
  headless Chrome/Edge session
- Multi-provider comparison with every normalized offer retained in JSON
- Exact six-place decimal prices and stock-aware purchase plans
- Digi-Key OAuth token reuse, locale/account pricing, and composite SKU plans
- TI OAuth token reuse through the native Go HTTP stack—no system `curl`
  dependency
- Datasheet and product links from normalized Mouser and Digi-Key offers
- HTTPS-only PDF downloads with size limits, signature checks, no-overwrite
  output, and SHA-256 artifact metadata
- Stock-aware resistor, capacitor, and inductor candidate evaluation with a
  field-by-field compatibility matrix
- Versioned SQLite lookup cache with WAL, TTL policies, payload checksums,
  credential-free cache-only/offline operation, and safe inspection/pruning
- Durable, audited persistence of human-approved part resolutions with
  preview/apply revocation
- Stable JSON stdout and process exit codes
- Verified Windows cross-compilation in the check pipeline (the NXP
  browser adapter is not yet available on Windows and reports that
  explicitly)

Reports are being ported in focused slices.
`bom-builder capabilities --full` is the authoritative way for a coding agent
to discover what the installed build can currently do.

## Build

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

Direct dependencies are deliberate and few: `modernc.org/sqlite v1.55.0`
(pure-Go SQLite for the lookup cache and resolutions store), the
`charmbracelet` Bubble Tea stack (`bubbletea`, `bubbles`, `lipgloss` — the
standard pure-Go terminal-interface toolkit powering the interactive mode,
cgo-free and Windows-capable), and `mattn/go-isatty` (terminal detection so
interactive mode can refuse pipelines). Use, for example,
`make GO_TOOLCHAIN=go1.26.5 check` only when deliberately testing another
compiler.

The resulting executable does not require a Go installation, an interpreter, or
any other runtime on the target machine.

Windows is a supported compilation target: `make check` cross-compiles and
vets every package for `windows/amd64` (`make windows` runs that gate alone).
The pure-Go SQLite driver works unchanged on Windows, and Chrome/Edge
discovery knows the Windows installation paths. One adapter is excluded so
far: the NXP browser transport rides on POSIX file-descriptor inheritance and
reports `unsupported_platform` on Windows instead of failing mid-run.

## Agent discovery

```bash
./bin/bom-builder capabilities --full --pretty
./bin/bom-builder providers list --pretty
./bin/bom-builder providers check --providers mouser,digikey,ti,nxp --live --pretty
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
  --providers mouser,digikey,ti,nxp \
  --pretty
```

Prices are JSON strings such as `"0.005000"` so calling agents never receive
binary floating-point approximations. An offer becomes `selected_plan` only
when the MPN is exact and reported stock covers the purchase quantity.
Non-exact candidates remain review-required. `--providers auto` uses every
configured native provider. Explicit provider selection never requires
credentials for providers outside that selection.

TI direct-store lookups are automatically skipped for non-TI manufacturers.
Known non-active lifecycle states and generic-to-orderable part resolutions
remain review-required; stock and TI Store order limits must both cover the
purchase plan before it can be selected.

NXP direct-store lookups are automatically skipped for non-NXP/Freescale
manufacturers. The adapter starts an isolated headless Chrome or Edge process
and communicates with it directly over the Chrome DevTools Protocol; it does
not require Playwright or a browser extension. The temporary browser
profile is deleted when the command ends. Exact orderable MPNs, confirmed MOQ
and package multiples, and sufficient reported stock are all required for a
selected plan. Base-part to packaging-suffix matches remain review-required.

When more than one provider is selected, each normalized candidate is returned
in `parts[].offers`, while `parts[].offer` is the chosen safe plan. Plans in
different currencies are never compared; the command fails closed until an FX
layer can prove a common currency.

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
(REL/EOL), MOQ/order-multiple, and datasheet links. The catalog carries
no pricing, so results are always review-required evidence with exit
code `3` and can never become a selected purchase plan — the offer's
product URL points at microchipDIRECT for the human order decision.
The provider applies only to Microchip/Atmel parts, is selected
explicitly (never by `--providers auto`), and participates in the
lookup cache like every other provider.

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
approval or revocation invalidates it. The store is SQLite with WAL — the same
concurrency-safe machinery as the lookup cache — and lives in the per-user
configuration directory by default. Override the location with
`--resolutions-db` or the `BOM_BUILDER_RESOLUTIONS_DB` process environment
variable; like every trusted-path override, the variable is refused from
`.env`.

## Interactive mode

```bash
bom-builder interactive
```

A full-screen terminal interface for the same resolutions store: browse
active and inactive records, inspect details and the audit history, approve
new resolutions through a form, and revoke active ones. The rules are
identical to the JSON commands — every decision names the acting person, and
revocation uses the same content-bound preview under the hood.

The resolver flow (`l`) closes the loop between sourcing and decisions: it
sources one part with the same provider, cache, and selection semantics as
`lookup`, lists every normalized candidate with match method, stock, price,
and review status, and seeds the approve form with the chosen candidate —
original part, replacement identity, provider SKU, and an evidence note.
The approver still reviews every field and must type their own name;
choosing a row never clears engineering review by itself. Provider clients
are constructed per lookup and torn down afterwards, so a resolution made
hours into a session sees current configuration and no idle browser or
token state lingers between lookups.

Interactive mode is the one deliberate exception to the JSON stdout
protocol. It renders for a human, and it refuses to start when stdin or
stdout is not a terminal, so a script or agent that reaches it by mistake
receives a machine-readable `INTERACTIVE_TTY_REQUIRED` error instead of a
hanging screen. Interactive views of live sourcing runs (the pricing
pipeline the archived Python TUI offered) are a planned later slice.

## Configuration

Copy `.env.example` to `.env` and add provider credentials. Existing process
environment variables take precedence. `.env` parsing never evaluates shell
syntax.

NXP needs no credentials, but requires an installed Google Chrome, Chromium, or
Microsoft Edge executable. BOM Builder discovers common locations
automatically; set `BOM_BUILDER_NXP_BROWSER` when the executable is elsewhere.
Set `NXP_STORE_CURRENCY` explicitly for the store context you intend to use.

Run `./bin/bom-builder help` for concise examples, or
`./bin/bom-builder capabilities --full` for the authoritative machine-readable
contract.

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
cmd/bom-builder/      executable entry point
internal/cli/         command parsing and JSON protocol
internal/contract/    public typed contracts
internal/config/      safe .env loading
internal/design/      design loading and validation
internal/bom/         deterministic demand aggregation
internal/money/       exact fixed-point decimal arithmetic
internal/procurement/ normalized offers and purchase-plan optimization
internal/provider/    provider discovery, health, and adapters
internal/lookupcache/ versioned SQLite normalized-result persistence
internal/resolutions/ audited persistence of human-approved resolutions
internal/sourcing/    provider-independent orchestration and safe totals
internal/tui/         interactive terminal mode (resolutions manager)
internal/alternatives/ candidate loading and compatibility evaluation
internal/documents/   evidence links and safe PDF retrieval
internal/app/         build and version metadata
examples/             runnable example input documents
schemas/              public JSON Schemas and embedded access
scripts/              reproducible build helpers
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
