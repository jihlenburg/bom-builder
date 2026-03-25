# Docs

Project documentation should live under `docs/` going forward.

Current release: `1.0.0.0`

Guides:

- [Secret Management](./guides/secrets.md)
- [Interactive Resolution](./guides/interactive-resolution.md)
- [Digi-Key Account Setup](./guides/digikey-account-setup.md)

Operational probes:

- `python scripts/digikey_probe.py --product-number P5555-ND --quantity 100`
- `python scripts/capture_live_fixtures.py`

Runtime UX notes:

- Live CLI output is intentionally buyer-facing: selected source, order plan,
  unit price, and line total first, with short note lines only when extra
  context is needed.
- AI reranking failures should degrade into one-shot fallback notices instead
  of repeated raw warning lines in the middle of a run.
- Purchase-plan selection now includes a small manufacturing-friendly bias so
  reel-heavy plans can win when they are materially better for the line and
  still within the configured cost delta.
- Cross-supplier selection is now also surplus-aware, so a supplier that
  forces large extra overbuy can lose to a slightly more expensive competitor.
- The persistent cache is now shared across Mouser, Digi-Key, and TI response
  payloads, including Mouser product-page and manufacturer-page packaging
  fallback results.
- NXP direct pricing is now available for `NXP` / `Freescale` parts through a
  browser-backed store parser that fails closed when the page structure becomes
  uncertain.
- `python main.py --flush` clears the shared distributor cache DB plus orphaned
  temp files, while `python main.py --flush-resolutions` also wipes the saved
  manual-resolution store for a true clean slate.
- `--mouser-delay` now keys off paced Mouser distributor traffic instead of
  every auxiliary fallback fetch, so manufacturer-page enrichment no longer
  slows unrelated lines.
- The CLI uses explicit Mouser-prefixed flags like `--mouser-api-key` and
  `--mouser-delay`; `python main.py --help` shows the full runtime surface, and
  `python main.py --version` prints the current release.

Generated API documentation:

- Install docs tooling: `pip install -r docs/requirements.txt`
- Regenerate the API module index: `python docs/generate_api_index.py`
- Build HTML docs: `make -C docs html`
- The HTML build refreshes `docs/api/index.rst` and clears stale generated API pages automatically
- Output location: `docs/_build/html/index.html`
- The generated API index should include `digikey`, `digikey_auth`, `fx`,
  `manufacturer_packaging`, `nxp`, `optimizer`, and `ti` alongside the
  original core modules
