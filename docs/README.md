# Docs

Project documentation should live under `docs/` going forward.

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

Generated API documentation:

- Install docs tooling: `pip install -r docs/requirements.txt`
- Regenerate the API module index: `python docs/generate_api_index.py`
- Build HTML docs: `make -C docs html`
- The HTML build refreshes `docs/api/index.rst` and clears stale generated API pages automatically
- Output location: `docs/_build/html/index.html`
- The generated API index should include `digikey`, `digikey_auth`, `fx`,
  `manufacturer_packaging`, `optimizer`, and `ti` alongside the original core modules
