# BOM Builder Documentation

This directory contains the project guides and the Sphinx-generated API
reference. The root `README.md` covers quick-start setup and CLI usage.

## Guides

- [Secret Management](./guides/secrets.md) -- environment variables, `.env`
  configuration, distributor credentials, locale settings, cache paths, and
  platform defaults.
- [Interactive Resolution](./guides/interactive-resolution.md) -- terminal
  chooser for ambiguous parts, saved resolutions, and the full resolver
  pipeline order.
- [Digi-Key Account Setup](./guides/digikey-account-setup.md) -- one-time
  3-legged OAuth flow to discover your Digi-Key Account ID.

## Operational Scripts

| Script | Purpose |
|--------|---------|
| `scripts/capture_live_fixtures.py` | Refresh cached Mouser/manufacturer packaging regression fixtures |
| `scripts/digikey_account_lookup.py` | One-time Digi-Key account discovery (see guide above) |
| `scripts/digikey_probe.py` | Verify Digi-Key locale and quantity pricing |

## Building the API Docs

```bash
pip install -r docs/requirements.txt
make -C docs html
open docs/_build/html/index.html
```

The build regenerates `docs/api/index.rst` from the current top-level Python
modules and clears stale autosummary pages automatically. The generated index
currently covers 20 modules.
