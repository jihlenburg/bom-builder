# Legacy Python BOM Builder

> This implementation was archived on 2026-07-29 when active development moved
> to the native Go application at the repository root. It remains available as
> a behavioral reference and fixture source.

A Python CLI tool for building and pricing electrical Bills of Materials (eBOMs).
Queries Mouser, Digi-Key, TI Store, and NXP direct, then selects the best
distributor offer per BOM line after cross-supplier price, surplus, and
packaging-plan comparison.

Current release: `2.0.0.0`

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
- Interactive resolution (`--interactive`) with a full-screen Textual TUI on TTY terminals
- Text-based CLI resolver fallback when not on a TTY
- Optional OpenAI reranker (`--ai-resolve`) for remaining ambiguous candidates
- Persistent distributor response cache with configurable TTL

**Input and output**

- JSON design files with multi-design aggregation and production-quantity scaling
- CSV, Excel, and JSON output with buyer-facing order-plan columns
- Versioned machine-readable JSON envelopes on stdout
- Independent provider selection with safe configuration and live health checks
- JSON Schema discovery and stdin input for agent and CI workflows
- Automatic package and pin count extraction from distributor data
- Stable completion statuses and strict exit policies for automation

## Quick Start

```bash
python -m pip install -e .
```

Or build a host-native executable that does not require Python on the target:

```bash
python -m pip install -e '.[standalone]'
scripts/build-standalone.sh
./dist/bom-builder capabilities --full
```

Configure one or more providers in `.env`. For example:

```bash
MOUSER_API_KEY=your-mouser-api-key
```

Inspect configured providers without making network requests:

```bash
bom-builder providers list --pretty
```

Price a BOM. The default output is a versioned JSON document on stdout:

```bash
bom-builder price designs/example_power_supply.json --units 1000 --pretty
```

For the full list of environment variables, distributor credentials, locale
settings, cache paths, and platform defaults, see the
[Secret Management](./docs/guides/secrets.md) guide.

## Usage

```bash
# Look up one part through every configured distributor
bom-builder lookup ADS7138-Q1 --manufacturer TI --quantity 1000

# Restrict sourcing to Digi-Key and TI; Mouser is not required
bom-builder lookup TMP421AQDCNRQ1 \
  --manufacturer "Texas Instruments" \
  --quantity 1000 \
  --providers digikey,ti

# Multiple designs with 2% attrition
bom-builder price designs/board-a.json designs/board-b.json \
  --units 500 \
  --attrition 0.02

# Write an Excel buyer sheet while stdout remains machine-readable JSON
bom-builder price designs/board.json \
  --units 1000 \
  --format excel \
  --output bom.xlsx

# Read a design from stdin
generate-design | bom-builder price - --units 1000

# Fail when a selected match requires engineering review
bom-builder price designs/board.json --units 1000 --fail-on review

# Validate input without contacting providers
bom-builder validate designs/board.json

# Verify all configured provider and service endpoints
bom-builder providers check --providers all --live --pretty

# Discover public JSON contracts
bom-builder schema input
bom-builder schema output
bom-builder schema providers

# Discover installed capabilities
bom-builder capabilities --pretty

# Retrieve capabilities, provider configuration, and schemas in one call
bom-builder capabilities --full

# Preview exact cache files before deletion
bom-builder cache purge --pretty
```

The canonical CLI never accepts API keys as command-line arguments. This keeps
credentials out of shell history and process listings. Stdout contains exactly
one JSON document; progress and verbose diagnostics use stderr. See the
[Automation CLI](./docs/guides/automation-cli.md) guide for the output contract,
exit codes, provider policy, and Claude/Codex examples.

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

Fuzzy matches are flagged for manual review. When `--interactive` is enabled on
a TTY, BOM Builder launches a full-screen Textual TUI with a live parts table,
running cost totals, and a modal dialog for candidate selection. When `--ai-resolve`
is also enabled, an OpenAI reranking step runs before the interactive prompt.

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
├── bom_builder_cli.py         # Canonical machine-first command interface
├── cli_models.py              # Versioned JSON envelopes and issue contracts
├── provider_health.py         # Safe provider discovery and live checks
├── main.py                    # Shared pricing orchestration engine
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
├── console.py                 # Rich-powered shared console and theme
├── tui/                       # Full-screen Textual TUI (--interactive)
│   ├── app.py                 # BomBuilderApp — screen composition and lifecycle
│   ├── events.py              # Textual Messages and ResolverRendezvous (Future-based)
│   ├── worker.py              # Threading bridge: sync pricing ↔ async UI
│   ├── widgets.py             # PartsTable, CostPanel, StatusBar
│   └── resolver_modal.py      # ModalScreen for interactive candidate selection
├── manufacturers.yaml         # Manufacturer name aliases
├── packages.yaml              # Package pattern definitions
├── pyproject.toml             # Packaging metadata and bom-builder entry point
├── requirements.txt
├── .env                       # Local API keys and developer overrides (gitignored)
├── scripts/                   # Operational helpers (fixture capture, Digi-Key setup)
├── tests/                     # pytest test suite
└── docs/                      # Project documentation and Sphinx API docs
```
