# Secret Management

`bom-builder` now uses a simple `.env`-based workflow.

Runtime secret resolution order:

1. CLI flag override
2. Inherited environment variable
3. `.env`

Current supported keys:

- `MOUSER_API_KEY`
- `MOUSER_API_KEYS`
- `OPENAI_API_KEY`
- `DIGIKEY_CLIENT_ID`
- `DIGIKEY_CLIENT_SECRET`
- `DIGIKEY_ACCOUNT_ID`
- `DIGIKEY_LOCALE_SITE`
- `DIGIKEY_LOCALE_LANGUAGE`
- `DIGIKEY_LOCALE_CURRENCY`
- `DIGIKEY_LOCALE_SHIP_TO_COUNTRY`
- `BOM_BUILDER_TARGET_CURRENCY`
- `BOM_BUILDER_FX_OVERRIDES`
- `BOM_BUILDER_MANUFACTURING_PREFERENCE_PCT`
- `TI_STORE_API_KEY`
- `TI_STORE_API_SECRET`
- `TI_STORE_PRICE_CURRENCY`
- `BOM_BUILDER_CACHE_DB`
- `BOM_BUILDER_RESOLUTIONS_FILE`
- `BOM_BUILDER_TRACE_FILE`
- `BOM_BUILDER_TRACE_DIR`

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

`DIGIKEY_ACCOUNT_ID` is only needed for Digi-Key account-aware product and
pricing calls. If you do not know it yet, use the one-time lookup helper
documented in {doc}`Digi-Key Account Setup <./digikey-account-setup>`.

The Digi-Key locale variables control which market and currency Digi-Key uses
for pricing. The defaults shown above target Germany/EUR so Product
Information V4 pricing comes back in EUR for an EU shipping destination.

`BOM_BUILDER_TARGET_CURRENCY` controls the run-wide comparison and reporting
currency. When a distributor returns prices in another currency, the runtime
converts those prices into the target currency using the ECB daily euro
foreign exchange reference rates. `BOM_BUILDER_FX_OVERRIDES` accepts manual
overrides such as `USD:EUR=0.92` when you want deterministic tests or you need
to run without a live FX lookup.

`BOM_BUILDER_MANUFACTURING_PREFERENCE_PCT` controls the optimizer's willingness
to prefer more manufacturing-friendly packaging plans over the absolute
cheapest line. The default runtime value is `0.5`, which lets reel-heavy or
other line-friendly plans win when they stay within `0.5%` of the cheapest
valid line cost.

`TI_STORE_API_KEY` and `TI_STORE_API_SECRET` enable TI direct pricing for
Texas Instruments parts through the TI Store Inventory and Pricing API.
`TI_STORE_PRICE_CURRENCY` controls the currency requested from TI, and the
runtime defaults that request to `USD` when no override is set. Legacy
`TI_PRODUCT_API_*` variable names remain accepted as a compatibility fallback.

`MOUSER_API_KEYS` is the preferred format when you have multiple Mouser API
keys. List them in priority order, separated by commas. The runtime will use
the first key by default and automatically fall back to the next configured key
when Mouser returns a quota or rate-limit response. `MOUSER_API_KEY` remains
available as the single-key compatibility fallback.

`BOM_BUILDER_CACHE_DB` and `BOM_BUILDER_RESOLUTIONS_FILE` are optional file
path overrides, not directory-only settings. If they are omitted or set to an
empty value, the application uses its built-in platform defaults instead. The
cache database is shared across Mouser, Digi-Key, and TI responses, even
though the historical default filename still uses `mouser_cache.sqlite3` for
backward compatibility.

`BOM_BUILDER_TRACE_FILE` and `BOM_BUILDER_TRACE_DIR` are optional tracing
paths. Set `BOM_BUILDER_TRACE_FILE` when you want one exact transcript file for
the next run, or `BOM_BUILDER_TRACE_DIR` when you want BOM Builder to create a
new timestamped transcript for each run. The transcript mirrors this process's
stdout and stderr, which makes future prompt-source debugging much easier.

Default locations:

- macOS cache DB: `~/Library/Caches/bom-builder/mouser_cache.sqlite3`
- Linux cache DB: `$XDG_CACHE_HOME/bom-builder/mouser_cache.sqlite3` or `~/.cache/bom-builder/mouser_cache.sqlite3`
- Windows cache DB: `%LOCALAPPDATA%\\bom-builder\\mouser_cache.sqlite3`
- macOS/Linux resolutions: `~/.config/bom-builder/resolutions.json` unless `XDG_CONFIG_HOME` is set
- Windows resolutions: `%APPDATA%\\bom-builder\\resolutions.json`

If you want saved manual resolutions to be project-local instead of shared
across all BOM Builder runs on the machine, point `BOM_BUILDER_RESOLUTIONS_FILE`
at a repo-local path in `.env`, for example:

```bash
BOM_BUILDER_RESOLUTIONS_FILE=.bom-builder/resolutions.json
```

CLI maintenance helpers:

- `python main.py --flush` removes the shared distributor cache DB, SQLite
  sidecars, and orphaned temp files, but keeps saved manual resolutions.
- `python main.py --flush-resolutions` also deletes the saved manual-resolution
  file for the configured `BOM_BUILDER_RESOLUTIONS_FILE` path.

## Local Development

Run the CLI directly:

```bash
python main.py -d designs/board.json -u 1000
```

`python-dotenv` loads `.env` automatically at startup, so no wrapper script is
needed. Existing process environment variables are not overwritten, which
keeps one-shot shell overrides working for cache paths, locale settings, and
temporary credential swaps.

## CI and Containers

Use normal environment variables when you intentionally want to run without a
local `.env` file:

```bash
export MOUSER_API_KEY=your-mouser-api-key
export OPENAI_API_KEY=your-openai-api-key
```

## Adding Future API Keys

Future runtime secrets should be added in
{doc}`the generated secret_store API reference <../api/generated/secret_store>`
by extending `SECRET_SPECS`.

## Notes

- `.env` should stay untracked and machine-local.
- CLI flags still override `.env` when you need a one-off value.
- Shell-prefixed environment variables also override `.env`, which is useful
  for true cold-cache or alternate-locale runs.
- The application no longer depends on OS keychains or launcher scripts.
