# Automation CLI

`bom-builder` is a command-line protocol for coding agents, CI jobs, shell
pipelines, and people who want deterministic structured results.

## Installation

From the repository root:

```bash
python -m pip install -e .
bom-builder --version
```

The editable installation keeps the command connected to the current checkout.
Provider credentials are read from inherited environment variables or `.env`;
credentials are not accepted as command-line flags.

### Standalone executable

Build a host-native executable when the target should not need Python or a
package installation:

```bash
python -m pip install -e '.[standalone]'
scripts/build-standalone.sh
./dist/bom-builder --version
```

PyInstaller embeds the Python runtime, application dependencies, Playwright
adapter, and YAML resolver data into `dist/bom-builder`. The executable is
specific to the operating system and CPU architecture on which it was built;
build separately on each target platform. Locally built macOS binaries are not
Apple-notarized.

Two provider adapters still rely on operating-system facilities: TI uses the
system `curl` executable because its production edge rejects the equivalent
Python transport, and NXP requires an installed Chrome or Edge browser. Mouser,
Digi-Key, ECB, and OpenAI do not need those external runtimes.

## Command Model

The canonical commands are:

```text
bom-builder price
bom-builder lookup
bom-builder validate
bom-builder providers list
bom-builder providers check
bom-builder cache status|purge
bom-builder resolutions list|remove|purge
bom-builder schema input|output|providers
bom-builder capabilities
```

Run `bom-builder COMMAND --help` for task-specific options.

## Stdout and Stderr Contract

Stdout contains exactly one JSON document for every successfully parsed
command, including expected runtime failures. Human progress and verbose
diagnostics go to stderr. An agent can therefore parse stdout without removing
Rich formatting, progress lines, or warning prose.

Pricing output uses schema version `1.0` and contains:

- overall `status` and `exit_code`
- run ID, timestamps, duration, version, and selected providers
- normalized BOM summary and priced parts
- one run summary for each selected provider
- stable warning and error objects

Use `--pretty` for indented JSON. Compact JSON is the default because it uses
less agent context.

## Pricing and Lookup

```bash
bom-builder price designs/example_power_supply.json --units 1000

bom-builder lookup TMP421AQDCNRQ1 \
  --manufacturer "Texas Instruments" \
  --quantity 1000
```

`price` interprets each design part's `quantity` as quantity per finished unit.
`lookup --quantity` is already the total required quantity.

The JSON result defaults to stdout. `--format csv|excel --output PATH` writes a
buyer-facing artifact while the full result envelope remains on stdout:

```bash
bom-builder price designs/board.json \
  --units 1000 \
  --format excel \
  --output artifacts/bom.xlsx
```

Use `--include-documents` when the result should include available datasheet
and provider product-page URLs. Mouser exposes these in normal search results;
Digi-Key may make one additional cache-backed product-details request:

```bash
bom-builder lookup ECA-1VHG102 \
  --manufacturer "Panasonic Industry" \
  --quantity 100 \
  --providers digikey \
  --include-documents
```

## Provider Selection

`--providers auto` is the default and selects every locally configured
distributor. Providers are independent; Digi-Key, TI, or NXP can run without a
Mouser credential:

```bash
bom-builder lookup ECA-1VHG102 \
  --manufacturer "Panasonic Industry" \
  --quantity 100 \
  --providers digikey
```

Select or exclude providers explicitly:

```text
--providers mouser,digikey,ti,nxp
--exclude-provider nxp
```

TI and NXP direct stores only apply to their own manufacturer lines. Their
provider summaries use `not_applicable` for other manufacturers.

## Provider Health

Configuration discovery does not contact the network and never reveals
credential values:

```bash
bom-builder providers list --pretty
bom-builder providers check --providers mouser,digikey
```

Add `--live` to make one bounded representative request per configured
provider:

```bash
bom-builder providers check --providers all --live --pretty
```

The health command supports Mouser, Digi-Key, TI, NXP, ECB FX, and the OpenAI
reranker. Results include safe configuration metadata, latency, request count,
and provider-specific details such as locale or remaining Digi-Key quota.

## Strict Completion and Exit Codes

`--fail-on` controls whether an otherwise useful result exits nonzero:

```text
--fail-on never   Always return 0 after producing a pricing result
--fail-on error   Return 3 for unpriced/error lines (default)
--fail-on review  Return 3 for errors or engineering-review lines
```

`--allow-partial` is an alias for `--fail-on never`.

Stable process codes:

| Code | Meaning |
|------|---------|
| `0` | Command completed under the selected strictness policy |
| `2` | Invalid command input or design data |
| `3` | Pricing completed but violated `--fail-on` |
| `4` | Required provider configuration or live health failure |
| `5` | Unexpected internal failure |

The same code appears in the emitted JSON envelope.

## Stdin and Schema Discovery

Read one design, a list of designs, or `{"designs": [...]}` from stdin:

```bash
generate-design | bom-builder validate -
generate-design | bom-builder price - --units 1000
```

Retrieve JSON Schema for integration generation and validation:

```bash
bom-builder schema input > design.schema.json
bom-builder schema output > pricing.schema.json
bom-builder schema providers > providers.schema.json
```

Discover supported commands, providers, formats, strictness policies, and
feature flags:

```bash
bom-builder capabilities --pretty
```

Retrieve the feature manifest, safe provider configuration, and every public
JSON Schema in one process:

```bash
bom-builder capabilities --full
```

The executable helper is convenient for coding agents and automatically
prefers `dist/bom-builder` over an installed command:

```bash
scripts/bom-builder-agent-context.sh > bom-builder-context.json
```

Set `BOM_BUILDER_BIN=/path/to/bom-builder` to select a different installed or
standalone executable.

## Cache and Resolution Maintenance

Maintenance commands preview their exact filesystem targets unless `--yes` is
present:

```bash
bom-builder cache status --pretty
bom-builder cache purge
bom-builder cache purge --yes

bom-builder resolutions list --pretty
bom-builder resolutions remove TMP421-Q1 --manufacturer TI
bom-builder resolutions remove TMP421-Q1 --manufacturer TI --yes
bom-builder resolutions purge
bom-builder resolutions purge --yes
```

Preview commands do not delete anything. Confirmed purge/remove operations
report the exact targets and affected count in their JSON result.

## Agent Example

A coding agent can perform a fresh, Digi-Key-only lookup with no progress
output:

```bash
bom-builder lookup ECA-1VHG102 \
  --manufacturer "Panasonic Industry" \
  --quantity 100 \
  --providers digikey \
  --fresh \
  --no-progress
```

Recommended interpretation:

1. Check the process exit code.
2. Parse stdout as one JSON document regardless of the code.
3. Inspect `status`, then `errors` and `warnings`.
4. Treat `review_required` as an engineering decision, not an automatic match.
5. Use `providers` to distinguish provider degradation from part-level failure.
