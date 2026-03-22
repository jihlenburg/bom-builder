# BOM Builder

A Python CLI tool for building and pricing electrical Bills of Materials (eBOM) using the [Mouser Search API](https://api.mouser.com/api/docs/ui/index).

## Features

- Read part lists from simple JSON design files
- Aggregate parts across multiple designs and scale by production quantity
- Multi-pass fuzzy part number resolution on Mouser (exact, begins-with, stripped qualifier search)
- Automatic manufacturer name matching with configurable aliases (`manufacturers.yaml`)
- Automotive (`-Q1`), lead-free (`/NOPB`), and other qualifier-aware scoring
- Automatic package and pin count extraction from Mouser data
- Output to CSV, Excel (`.xlsx`), or JSON
- Statistical summary with top parts by cost and quantity, manufacturer breakdown, and package overview
- Persistent Mouser search cache with 24-hour retention by default
- Verbose diagnostic mode (`-v`) for full debug/trace output on stdout
- Interactive ambiguity resolver (`--interactive`) with saved manual selections
- Optional OpenAI reranker (`--ai-resolve`) for the remaining ambiguous candidates

## Setup

```bash
pip install -r requirements.txt
```

Set your API keys in `.env` or normal environment variables. The runtime app resolves secrets in this order:

1. CLI flag override
2. `.env`
3. Inherited environment variable

Example `.env`:

```bash
MOUSER_API_KEY=your-mouser-api-key
MOUSER_API_KEYS=your-primary-mouser-api-key,your-backup-mouser-api-key
OPENAI_API_KEY=your-openai-api-key
BOM_BUILDER_CACHE_DB=/absolute/path/to/mouser_cache.sqlite3
BOM_BUILDER_RESOLUTIONS_FILE=/absolute/path/to/resolutions.json
```

If `MOUSER_API_KEYS` is set, BOM Builder uses the listed Mouser keys in order
and automatically falls back to the next key when the current one hits a
Mouser rate limit or daily quota error. `MOUSER_API_KEY` remains supported as
the single-key fallback.

`BOM_BUILDER_CACHE_DB` and `BOM_BUILDER_RESOLUTIONS_FILE` are optional file
path overrides. If either entry is missing or blank, BOM Builder falls back to
its platform default path for that file.

Default locations:

- macOS cache DB: `~/Library/Caches/bom-builder/mouser_cache.sqlite3`
- Linux cache DB: `$XDG_CACHE_HOME/bom-builder/mouser_cache.sqlite3` or `~/.cache/bom-builder/mouser_cache.sqlite3`
- Windows cache DB: `%LOCALAPPDATA%\\bom-builder\\mouser_cache.sqlite3`
- macOS/Linux resolutions: `~/.config/bom-builder/resolutions.json` unless `XDG_CONFIG_HOME` is set
- Windows resolutions: `%APPDATA%\\bom-builder\\resolutions.json`

Documentation now lives under [`docs/`](./docs). See [`docs/guides/secrets.md`](./docs/guides/secrets.md) for `.env` and environment-variable setup details.
See [`docs/guides/interactive-resolution.md`](./docs/guides/interactive-resolution.md) for the interactive resolver flow.
Generated API documentation can be built with Sphinx from the docstrings:

```bash
pip install -r docs/requirements.txt
make -C docs html
```

The generated site is written to `docs/_build/html/index.html`.

The local `.env` file is loaded with override enabled so unrelated parent-shell
environment variables do not silently override the project’s configured API
keys.

## Usage

```bash
# Basic usage — single design, 1000 units, CSV output
python main.py -d designs/power_supply.json -u 1000

# Multiple designs, Excel output
python main.py -d designs/*.json -u 500 -f excel -o bom.xlsx

# JSON output with 2% attrition factor
python main.py -d designs/board.json -u 1000 -a 0.02 -f json

# Verbose mode for debugging Mouser lookups
python main.py -d designs/board.json -u 1000 -v | tee diag.log

# Directly look up one part without creating a design JSON file
python main.py --part-number ADS7138-Q1 --manufacturer TI -u 1 --verbose

# Custom API delay (for rate limiting)
python main.py -d designs/board.json -u 1000 --delay 2.0

# Force fresh Mouser queries for one run
python main.py -d designs/board.json -u 1000 --no-cache

# Resolve ambiguous parts interactively and save your choices
python main.py -d LUPA_48VGen_BOM.json -u 1000 --interactive

# Let OpenAI rerank ambiguous candidates before falling back to prompts
python main.py -d LUPA_48VGen_BOM.json -u 1000 --ai-resolve --interactive
```

### CLI Options

| Flag | Description |
|------|-------------|
| `-d`, `--design` | Path(s) to design JSON file(s) (required) |
| `--part-number` | Directly look up one manufacturer part number without a design file |
| `--manufacturer` | Manufacturer hint required with `--part-number` |
| `--quantity-per-unit` | Quantity per finished unit in `--part-number` mode |
| `--description` | Optional description hint in `--part-number` mode |
| `--package` | Optional package hint in `--part-number` mode |
| `--pins` | Optional pin-count hint in `--part-number` mode |
| `-u`, `--units` | Number of units to build (required) |
| `-f`, `--format` | Output format: `csv`, `excel`, `json` |
| `-o`, `--output` | Output file path (format auto-detected from extension) |
| `-a`, `--attrition` | Attrition/waste factor, e.g. `0.02` for 2% |
| `--api-key` | Mouser API key (overrides secure store / env var) |
| `--delay` | Delay between API requests in seconds (default: 1.0) |
| `--cache-ttl-hours` | Mouser search cache retention in hours (default: `24`) |
| `--no-cache` | Disable the persistent Mouser search cache |
| `--interactive` | Prompt for manual candidate selection on ambiguous parts and save the choice |
| `--ai-resolve` | Use OpenAI to rerank still-ambiguous candidates before prompting |
| `--ai-model` | OpenAI model for `--ai-resolve` (default: `gpt-5.4-mini`) |
| `--ai-confidence-threshold` | Minimum AI confidence required to auto-accept a reranked candidate |
| `-v`, `--verbose` | Write full diagnostic trace output to stdout |

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
├── manufacturers.yaml    # Manufacturer name aliases
├── secret_store.py       # Environment and `.env`-backed secret loading
├── resolution_store.py   # Saved manual resolution mappings for future runs
├── requirements.txt
├── .env                  # Local API keys and developer overrides
└── docs/                 # Project documentation
    └── guides/
```
