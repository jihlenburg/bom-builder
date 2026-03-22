# Secret Management

`bom-builder` now uses a simple `.env`-based workflow.

Runtime secret resolution order:

1. CLI flag override
2. `.env`
3. Inherited environment variable

Current supported keys:

- `MOUSER_API_KEY`
- `MOUSER_API_KEYS`
- `OPENAI_API_KEY`
- `BOM_BUILDER_CACHE_DB`
- `BOM_BUILDER_RESOLUTIONS_FILE`

Example `.env`:

```bash
MOUSER_API_KEY=your-mouser-api-key
MOUSER_API_KEYS=your-primary-mouser-api-key,your-backup-mouser-api-key
OPENAI_API_KEY=your-openai-api-key
BOM_BUILDER_CACHE_DB=/absolute/path/to/mouser_cache.sqlite3
BOM_BUILDER_RESOLUTIONS_FILE=/absolute/path/to/resolutions.json
```

`MOUSER_API_KEYS` is the preferred format when you have multiple Mouser API
keys. List them in priority order, separated by commas. The runtime will use
the first key by default and automatically fall back to the next configured key
when Mouser returns a quota or rate-limit response. `MOUSER_API_KEY` remains
available as the single-key compatibility fallback.

`BOM_BUILDER_CACHE_DB` and `BOM_BUILDER_RESOLUTIONS_FILE` are optional file
path overrides, not directory-only settings. If they are omitted or set to an
empty value, the application uses its built-in platform defaults instead.

Default locations:

- macOS cache DB: `~/Library/Caches/bom-builder/mouser_cache.sqlite3`
- Linux cache DB: `$XDG_CACHE_HOME/bom-builder/mouser_cache.sqlite3` or `~/.cache/bom-builder/mouser_cache.sqlite3`
- Windows cache DB: `%LOCALAPPDATA%\\bom-builder\\mouser_cache.sqlite3`
- macOS/Linux resolutions: `~/.config/bom-builder/resolutions.json` unless `XDG_CONFIG_HOME` is set
- Windows resolutions: `%APPDATA%\\bom-builder\\resolutions.json`

## Local Development

Run the CLI directly:

```bash
python main.py -d designs/board.json -u 1000
```

`python-dotenv` loads `.env` automatically at startup, so no wrapper script is needed.
The loader uses override mode so a stale parent-shell variable does not
silently win over the project-local `.env` file.

## CI and Containers

Use normal environment variables when you intentionally want to run without a
local `.env` file:

```bash
export MOUSER_API_KEY=your-mouser-api-key
export OPENAI_API_KEY=your-openai-api-key
```

## Adding Future API Keys

Future runtime secrets should be added in [`secret_store.py`](/Users/jihlenburg/bom-builder/secret_store.py) by extending `SECRET_SPECS`.

## Notes

- `.env` should stay untracked and machine-local.
- CLI flags still override `.env` when you need a one-off value.
- The application no longer depends on OS keychains or launcher scripts.
