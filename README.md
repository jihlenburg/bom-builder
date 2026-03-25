# BOM Builder

A Python CLI tool for building and pricing electrical Bills of Materials (eBOMs).
Queries Mouser, Digi-Key, TI Store, and NXP direct, then selects the best
distributor offer per BOM line after cross-supplier price, surplus, and
packaging-plan comparison.

Current release: `1.0.1.0`

## Features

**Sourcing and pricing**

- Multi-pass fuzzy part number resolution on Mouser (exact, begins-with, stripped qualifier)
- Optional Digi-Key, TI Store, and NXP direct pricing with automatic cheapest-offer selection
- Price-break-aware overbuy and mixed-packaging optimization (reel + cut-tape plans)
- Manufacturing-biased plan preference within a configurable cost delta
- Surplus-aware cross-supplier scoring
- FX normalization for cross-currency comparison via ECB daily rates

**Resolution and review**

- Qualifier-aware scoring (`-Q1`, `/NOPB`, `-EP`, `-TR`) with configurable manufacturer aliases
- Interactive terminal resolver (`--interactive`) with saved manual selections
- Optional OpenAI reranker (`--ai-resolve`) for remaining ambiguous candidates
- Persistent distributor response cache with configurable TTL

**Input and output**

- JSON design files with multi-design aggregation and production-quantity scaling
- CSV, Excel, and JSON output with buyer-facing order-plan columns
- Automatic package and pin count extraction from distributor data
- Per-run trace transcripts for debugging

## Quick Start

```bash
pip install -r requirements.txt
```

Create a `.env` file with at minimum your Mouser API key:

```bash
MOUSER_API_KEY=your-mouser-api-key
```

Run a BOM:

```bash
python main.py -d designs/board.json -u 1000
```

For the full list of environment variables, distributor credentials, locale
settings, cache paths, and platform defaults, see the
[Secret Management](./docs/guides/secrets.md) guide.

## Usage

```bash
# Single design, 1000 units, CSV output
python main.py -d designs/power_supply.json -u 1000

# Multiple designs, Excel output
python main.py -d designs/*.json -u 500 -f excel -o bom.xlsx

# JSON output with 2% attrition factor
python main.py -d designs/board.json -u 1000 -a 0.02 -f json

# Verbose diagnostic output
python main.py -d designs/board.json -u 1000 -v | tee diag.log

# Capture a run transcript
python main.py -d designs/board.json -u 1000 --trace-file run.log

# Look up a single part directly
python main.py --part-number ADS7138-Q1 --manufacturer TI -u 1 --verbose

# Resolve ambiguous parts interactively
python main.py -d designs/board.json -u 1000 --interactive

# AI reranking before interactive fallback
python main.py -d designs/board.json -u 1000 --ai-resolve --interactive

# Flush the distributor cache
python main.py --flush

# Flush cache and saved resolutions for a clean slate
python main.py --flush-resolutions

# Force fresh distributor queries for one run
python main.py -d designs/board.json -u 1000 --no-cache
```

### CLI Options

| Flag | Description |
|------|-------------|
| `-d`, `--design` | Design JSON file(s); required unless using a standalone flush action |
| `--part-number` | Look up one manufacturer part number without a design file |
| `--manufacturer` | Manufacturer hint (required with `--part-number`) |
| `--quantity-per-unit` | Quantity per finished unit in single-part mode |
| `--description` | Description hint in single-part mode |
| `--package` | Package hint in single-part mode |
| `--pins` | Pin-count hint in single-part mode |
| `-u`, `--units` | Number of units to build |
| `-f`, `--format` | Output format: `csv`, `excel`, `json` |
| `-o`, `--output` | Output file path |
| `-a`, `--attrition` | Waste factor, e.g. `0.02` for 2% |
| `--mouser-api-key` | Mouser API key (overrides `.env`) |
| `--mouser-delay` | Delay between live Mouser requests in seconds (default: `1.0`) |
| `--flush` | Remove distributor cache and temp files; may be used standalone |
| `--flush-resolutions` | Also remove saved manual resolutions; may be used standalone |
| `--cache-ttl-hours` | Cache retention in hours (default: `24`) |
| `--no-cache` | Disable the persistent cache for this run |
| `--interactive` | Prompt for manual candidate selection on ambiguous parts |
| `--ai-resolve` | Use OpenAI to rerank ambiguous candidates before prompting |
| `--ai-model` | OpenAI model for `--ai-resolve` (default: `gpt-5.4-mini`) |
| `--ai-confidence-threshold` | Minimum AI confidence to auto-accept a candidate |
| `-v`, `--verbose` | Full diagnostic trace on stdout |
| `--trace-file` | Mirror stdout/stderr into a file |
| `--version` | Show version and exit |
| `-h`, `--help` | Show CLI help and exit |

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

The tool uses a multi-pass lookup strategy to handle incomplete or shorthand
part numbers:

1. **Exact match** on the full part number
2. **BeginsWith** on the full part number (catches longer orderable MPNs)
3. **Fuzzy** -- strips known qualifier suffixes and searches with BeginsWith, then scores candidates by manufacturer, qualifier match, and availability

Fuzzy matches are flagged for manual review. When `--interactive` is enabled,
the terminal UI shows ranked candidates with package, price, and availability
data. When `--ai-resolve` is also enabled, an OpenAI reranking step runs before
the interactive prompt.

After Mouser resolution, configured distributors (Digi-Key, TI Store, NXP
direct) are queried in parallel. Each offer is normalized and the cheapest
confident result is selected per BOM line. Manufacturer-direct stores (TI, NXP)
are treated as authoritative for their own parts.

See the [Interactive Resolution](./docs/guides/interactive-resolution.md) guide
for details on the terminal chooser and saved resolutions.

## Documentation

Detailed guides live under [`docs/`](./docs):

- [Secret Management](./docs/guides/secrets.md) -- environment variables, `.env` setup, platform defaults
- [Interactive Resolution](./docs/guides/interactive-resolution.md) -- terminal chooser and saved resolutions
- [Digi-Key Account Setup](./docs/guides/digikey-account-setup.md) -- one-time OAuth account lookup

Generated API documentation can be built with Sphinx:

```bash
pip install -r docs/requirements.txt
make -C docs html
open docs/_build/html/index.html
```

Edit `manufacturers.yaml` to add or update manufacturer name aliases (e.g. "TI"
to "Texas Instruments").

## Project Structure

```
bom-builder/
├── main.py                    # CLI entry point and orchestration
├── models.py                  # Pydantic data models (Part, PricedPart, BomSummary, ...)
├── bom.py                     # Design loading and part aggregation
├── mouser.py                  # Mouser API client, multi-pass lookup, and pricing pipeline
├── mouser_scoring.py          # Candidate matching, scoring, and qualification rules
├── mouser_packaging.py        # Packaging detail extraction from search and product pages
├── package.py                 # Package/pin extraction logic
├── report.py                  # CSV, Excel, and JSON output
├── config.py                  # Configuration and env loading
├── ai_resolver.py             # Optional OpenAI candidate reranker
├── digikey.py                 # Digi-Key V4 client with locale-aware pricing
├── digikey_auth.py            # Digi-Key OAuth and account-discovery helpers
├── ti.py                      # TI Store Inventory and Pricing API client
├── nxp.py                     # NXP direct-store client with fail-closed parsing
├── fx.py                      # FX rate provider for cross-currency normalization
├── optimizer.py               # Distributor-agnostic purchase-plan optimization
├── manufacturer_packaging.py  # Manufacturer fallback packaging parsers and shared utilities
├── lookup_cache.py            # SQLite-backed distributor response cache
├── secret_store.py            # Environment and .env-backed secret loading
├── resolution_store.py        # Saved manual resolution mappings
├── manufacturers.yaml         # Manufacturer name aliases
├── packages.yaml              # Package pattern definitions
├── requirements.txt
├── .env                       # Local API keys and developer overrides (gitignored)
├── scripts/                   # Operational helpers (fixture capture, Digi-Key setup)
├── tests/                     # pytest test suite
└── docs/                      # Project documentation and Sphinx API docs
```
