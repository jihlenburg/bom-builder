# BOM Builder

A Python CLI tool for building and pricing electrical Bills of Materials
(eBOMs) across Mouser, Digi-Key, and TI direct for TI parts, with automatic selection of the cheapest
confident distributor offer per BOM line.

## Features

- Read part lists from simple JSON design files
- Aggregate parts across multiple designs and scale by production quantity
- Multi-pass fuzzy part number resolution on Mouser (exact, begins-with, stripped qualifier search)
- Automatic manufacturer name matching with configurable aliases (`manufacturers.yaml`)
- Automotive (`-Q1`), lead-free (`/NOPB`), and other qualifier-aware scoring
- Automatic package and pin count extraction from Mouser data
- Output to CSV, Excel (`.xlsx`), or JSON
- Statistical summary with top parts by cost and quantity, manufacturer breakdown, and package overview
- Persistent distributor response cache with 24-hour retention by default
- Verbose diagnostic mode (`-v`) for full debug/trace output on stdout
- Interactive ambiguity resolver (`--interactive`) with saved manual selections
- Optional OpenAI reranker (`--ai-resolve`) for the remaining ambiguous candidates
- Optional Digi-Key Product Information V4 pricing with automatic cheapest-offer selection
- Optional TI Store Inventory and Pricing API pricing for Texas Instruments parts
- Price-break-aware overbuy selection that can choose full reels or larger packs when they reduce actual spend
- Mixed-packaging optimization for Mouser, including reel-plus-remainder plans when they reduce total spend
- Central distributor-agnostic purchase-plan optimizer shared by Mouser, Digi-Key, and TI offer selection
- Small manufacturing-biased plan preference so reel-heavy buys can beat all-cut-tape plans when line cost stays within a configurable delta
- CSV/Excel output includes buyer-facing order-plan columns such as batch size, batch count, shortage, and reel plan
- Optional per-run trace transcript that captures exactly what this process wrote to stdout/stderr
- Compact live console output with buyer-facing order plans, per-line pricing, and graceful one-shot AI fallback notices

## Setup

```bash
pip install -r requirements.txt
```

Set your API keys in `.env` or normal environment variables. The runtime app resolves secrets in this order:

1. CLI flag override
2. Inherited environment variable
3. `.env`

Example `.env`:

```bash
MOUSER_API_KEY=your-mouser-api-key
MOUSER_API_KEYS=your-primary-mouser-api-key,your-backup-mouser-api-key
OPENAI_API_KEY=your-openai-api-key
DIGIKEY_CLIENT_ID=your-digikey-client-id
DIGIKEY_CLIENT_SECRET=your-digikey-client-secret
DIGIKEY_ACCOUNT_ID=your-digikey-account-id
DIGIKEY_LOCALE_SITE=DE
DIGIKEY_LOCALE_LANGUAGE=en
DIGIKEY_LOCALE_CURRENCY=EUR
DIGIKEY_LOCALE_SHIP_TO_COUNTRY=de
BOM_BUILDER_TARGET_CURRENCY=EUR
BOM_BUILDER_FX_OVERRIDES=
BOM_BUILDER_MANUFACTURING_PREFERENCE_PCT=0.5
TI_STORE_API_KEY=your-ti-store-api-key
TI_STORE_API_SECRET=your-ti-store-api-secret
TI_STORE_PRICE_CURRENCY=USD
BOM_BUILDER_CACHE_DB=/absolute/path/to/distributor_cache.sqlite3
BOM_BUILDER_RESOLUTIONS_FILE=/absolute/path/to/resolutions.json
BOM_BUILDER_TRACE_FILE=/absolute/path/to/latest-run.log
BOM_BUILDER_TRACE_DIR=/absolute/path/to/run-traces
```

If `MOUSER_API_KEYS` is set, BOM Builder uses the listed Mouser keys in order
and automatically falls back to the next key when the current one hits a
Mouser rate limit or daily quota error. `MOUSER_API_KEY` remains supported as
the single-key fallback.

`BOM_BUILDER_CACHE_DB` and `BOM_BUILDER_RESOLUTIONS_FILE` are optional file
path overrides. If either entry is missing or blank, BOM Builder falls back to
its platform default path for that file. The cache database is now shared
across Mouser, Digi-Key, and TI responses, even though the historical default
filename still ends in `mouser_cache.sqlite3` for backward compatibility.

`BOM_BUILDER_TRACE_FILE` writes one exact stdout/stderr transcript for the
current run. `BOM_BUILDER_TRACE_DIR` instead creates one timestamped transcript
per run under the chosen directory. These traces are useful when you need to
prove whether a prompt or warning really came from `python main.py` or from
another process sharing the same terminal.

For Digi-Key, locale controls the market and currency used for pricing. The
default example above uses a Germany/EUR shipping context (`DE` / `EUR` / `de`)
so Digi-Key Product Information V4 pricing comes back in EUR for an EU
destination.

`BOM_BUILDER_TARGET_CURRENCY` controls the currency used for cross-distributor
comparison, summaries, and selected-offer reporting. By default it falls back
to `DIGIKEY_LOCALE_CURRENCY`, then `EUR`. When offers arrive in another
currency, BOM Builder converts them into the target currency using the ECB
daily euro foreign exchange reference rates. `BOM_BUILDER_FX_OVERRIDES`
supports manual overrides such as `USD:EUR=0.92` for deterministic testing or
offline runs.

`BOM_BUILDER_MANUFACTURING_PREFERENCE_PCT` controls how far the optimizer may
deviate from the absolute cheapest line cost when a more manufacturing-friendly
plan is available. The default runtime value is `0.5`, which means BOM Builder
may prefer reel-heavy or otherwise line-friendly packaging when the plan stays
within `0.5%` of the cheapest valid option for that BOM line.

For TI direct pricing, `TI_STORE_API_KEY` and `TI_STORE_API_SECRET` enable the
TI Store Inventory and Pricing API integration for BOM lines whose
manufacturer is Texas Instruments / `TI`. The TI store API returns
currency-tagged price-break schedules, and `TI_STORE_PRICE_CURRENCY` controls
which currency is requested from TI. The runtime defaults that request
currency to `USD` when no override is set. Legacy `TI_PRODUCT_API_*` variable
names are still accepted as a compatibility fallback.

Default locations:

- macOS cache DB: `~/Library/Caches/bom-builder/mouser_cache.sqlite3`
- Linux cache DB: `$XDG_CACHE_HOME/bom-builder/mouser_cache.sqlite3` or `~/.cache/bom-builder/mouser_cache.sqlite3`
- Windows cache DB: `%LOCALAPPDATA%\\bom-builder\\mouser_cache.sqlite3`
- macOS/Linux resolutions: `~/.config/bom-builder/resolutions.json` unless `XDG_CONFIG_HOME` is set
- Windows resolutions: `%APPDATA%\\bom-builder\\resolutions.json`

Documentation now lives under [`docs/`](./docs). See [`docs/guides/secrets.md`](./docs/guides/secrets.md) for `.env` and environment-variable setup details.
See [`docs/guides/interactive-resolution.md`](./docs/guides/interactive-resolution.md) for the interactive resolver flow.
See [`docs/guides/digikey-account-setup.md`](./docs/guides/digikey-account-setup.md) for the one-time Digi-Key Account ID lookup flow.
Use `python scripts/digikey_probe.py --product-number P5555-ND --quantity 100` to verify Digi-Key locale and quantity pricing with your configured credentials.
When Digi-Key and/or TI credentials are configured, BOM Builder automatically
queries those additional sources alongside Mouser and chooses the cheapest
priced offer that does not require manual review.
Generated API documentation can be built with Sphinx from the docstrings:

```bash
pip install -r docs/requirements.txt
python docs/generate_api_index.py
make -C docs html
```

The generated site is written to `docs/_build/html/index.html`.
The docs build also regenerates `docs/api/index.rst` from the current top-level
Python modules and clears stale autosummary pages under `docs/api/generated/`.

Regression fixtures for Mouser and manufacturer packaging parsers can be
refreshed with:

```bash
python scripts/capture_live_fixtures.py
```

The fixture capture prefers live responses when available and falls back to the
local Mouser cache when quota is exhausted, so parser regressions remain
testable without depending on fresh API quota every time.

The local `.env` file is loaded without overriding existing process
environment variables, so one-shot shell overrides for cache paths, locale, or
credentials still work during ad-hoc runs.

## Usage

```bash
# Basic usage — single design, 1000 units, CSV output
python main.py -d designs/power_supply.json -u 1000

# Multiple designs, Excel output
python main.py -d designs/*.json -u 500 -f excel -o bom.xlsx

# JSON output with 2% attrition factor
python main.py -d designs/board.json -u 1000 -a 0.02 -f json

# Verbose mode for debugging distributor lookups
python main.py -d designs/board.json -u 1000 -v | tee diag.log

# Capture a transcript for one run without changing normal console behavior
python main.py -d designs/board.json -u 1000 --trace-file run.log

# Directly look up one part without creating a design JSON file
python main.py --part-number ADS7138-Q1 --manufacturer TI -u 1 --verbose

# Custom Mouser pacing delay after live Mouser requests
python main.py -d designs/board.json -u 1000 --mouser-delay 2.0

# Show the full CLI reference
python main.py --help

# Flush the shared distributor cache and orphaned temp files
python main.py --flush

# Flush caches first, then continue into a normal BOM run
python main.py --flush -d designs/board.json -u 1000

# Also delete saved manual resolutions when you want a true clean slate
python main.py --flush-resolutions

# Force fresh distributor queries for one run
python main.py -d designs/board.json -u 1000 --no-cache

# Resolve ambiguous parts interactively and save your choices
python main.py -d LUPA_48VGen_BOM.json -u 1000 --interactive

# Let OpenAI rerank ambiguous candidates before falling back to prompts
python main.py -d LUPA_48VGen_BOM.json -u 1000 --ai-resolve --interactive

# If Digi-Key credentials are configured, both distributors are queried and
# the cheapest confident offer is selected automatically
python main.py -d LUPA_48VGen_BOM.json -u 1000 -f excel -o bom.xlsx

# If TI Store API credentials are configured, TI parts are also checked
# against TI direct pricing
python main.py --part-number TMP421AQDCNRQ1 --manufacturer TI -u 100

# Refresh cached live parser fixtures used by regression tests
python scripts/capture_live_fixtures.py
```

### CLI Options

| Flag | Description |
|------|-------------|
| `-d`, `--design` | Path(s) to design JSON file(s) (required unless a flush action is used standalone) |
| `--part-number` | Directly look up one manufacturer part number without a design file |
| `--manufacturer` | Manufacturer hint required with `--part-number` |
| `--quantity-per-unit` | Quantity per finished unit in `--part-number` mode |
| `--description` | Optional description hint in `--part-number` mode |
| `--package` | Optional package hint in `--part-number` mode |
| `--pins` | Optional pin-count hint in `--part-number` mode |
| `-u`, `--units` | Number of units to build (required for actual BOM lookups) |
| `-f`, `--format` | Output format: `csv`, `excel`, `json` |
| `-o`, `--output` | Output file path (format auto-detected from extension) |
| `-a`, `--attrition` | Attrition/waste factor, e.g. `0.02` for 2% |
| `--mouser-api-key` | Mouser API key (overrides `.env` / environment variables) |
| `--mouser-delay` | Delay after live Mouser requests in seconds (default: `1.0`) |
| `--flush` | Remove the shared distributor cache DB, SQLite sidecars, and orphaned temp files before running; may be used standalone |
| `--flush-resolutions` | Also remove the saved manual-resolution store; may be used standalone |
| `--cache-ttl-hours` | Shared distributor-response cache retention in hours, including Mouser page-fallback packaging data (default: `24`) |
| `--no-cache` | Disable the persistent distributor response cache |
| `--interactive` | Prompt for manual candidate selection on ambiguous parts and save the choice |
| `--ai-resolve` | Use OpenAI to rerank still-ambiguous candidates before prompting |
| `--ai-model` | OpenAI model for `--ai-resolve` (default: `gpt-5.4-mini`) |
| `--ai-confidence-threshold` | Minimum AI confidence required to auto-accept a reranked candidate |
| `-v`, `--verbose` | Write full diagnostic trace output to stdout |
| `--trace-file` | Mirror this run's stdout/stderr transcript into a file |
| `-h`, `--help` | Show the built-in CLI help and exit |

## Design JSON Format

```json
{
  "design": "Power Supply Rev A",
  "version": "1.0",
  "parts": [
    {
      "part_number": "RC0402FR-0710KL",
      "manufacturer": "Yageo",
      "quantity": 4,
      "reference": "R1,R2,R3,R4",
      "description": "10kOhm 0402 1% resistor",
      "package": "0402",
      "pins": 2
    }
  ]
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `design` | yes | Design name |
| `version` | no | Revision string |
| `parts[].part_number` | yes | Manufacturer part number |
| `parts[].manufacturer` | yes | Manufacturer name |
| `parts[].quantity` | yes | Quantity per unit |
| `parts[].reference` | no | Reference designators |
| `parts[].description` | no | Human-readable description |
| `parts[].package` | no | Package type (auto-detected if omitted) |
| `parts[].pins` | no | Pin count (auto-detected if omitted) |

## Part Number Resolution

The tool uses a multi-pass lookup strategy to handle incomplete or shorthand part numbers:

1. **Exact match** on the full part number
2. **BeginsWith** on the full part number (catches longer orderable MPNs)
3. **Fuzzy** — strips known suffixes (`-Q1`, `/NOPB`, `-EP`, `-TR`) and searches with BeginsWith, then scores candidates by manufacturer, qualifier match, and availability

Fuzzy matches are flagged with "review!" in the output for manual verification.

When `--interactive` is enabled, the CLI stops only on those review-required parts and shows a paged terminal chooser with package, pin, price, and availability data. Selected candidates are saved and reused automatically on later runs.

When `--ai-resolve` is enabled, the resolver inserts an OpenAI reranking step before interactive review. The AI only chooses from the existing Mouser candidate shortlist and abstains when the BOM text is still underspecified.

When Digi-Key credentials are configured, BOM Builder reuses the best resolved
manufacturer part number from the Mouser workflow, asks Digi-Key for quantity
pricing on that same part, and then keeps the cheapest priced offer that does
not require manual review. If Digi-Key is unavailable or returns no valid
price, the Mouser result remains selected.

When TI Store API credentials are configured, BOM Builder also queries TI
direct pricing for TI-manufactured parts. TI direct pricing is compared the
same way as any other normalized offer, including MOQ, order limit, price
break, and full-reel metadata when TI exposes those constraints.

Direct single-part lookup mode is useful for debugging distributor behavior on
one MPN at a time. It runs through the same cache, resolver, AI, interactive,
and reporting pipeline as a normal design-file lookup, but builds a synthetic
one-line design from the CLI flags instead of reading JSON.

## Manufacturer Aliases

Edit `manufacturers.yaml` to add or update manufacturer name mappings. This handles cases where the BOM uses "TI" but Mouser returns "Texas Instruments".

## Project Structure

```
bom-builder/
├── main.py               # CLI entry point
├── models.py             # Pydantic data models
├── bom.py                # Design loading and part aggregation
├── mouser.py             # Mouser API client with fuzzy lookup
├── package.py            # Package/pin extraction logic
├── report.py             # CSV, Excel, and JSON output
├── config.py             # Configuration and env loading
├── digikey_auth.py       # Digi-Key OAuth and account-discovery helpers
├── digikey.py            # Digi-Key V4 client with locale-aware pricing helpers
├── fx.py                 # FX normalization for cross-distributor comparison
├── manufacturer_packaging.py  # Manufacturer fallback packaging parsers
├── optimizer.py          # Shared purchase-plan optimization logic
├── ti.py                 # TI Store Inventory and Pricing API client and pricing helpers
├── manufacturers.yaml    # Manufacturer name aliases
├── secret_store.py       # Environment and `.env`-backed secret loading
├── resolution_store.py   # Saved manual resolution mappings for future runs
├── requirements.txt
├── .env                  # Local API keys and developer overrides
├── scripts/              # Operational helpers such as fixture capture and Digi-Key setup
└── docs/                 # Project documentation
    └── guides/
```
