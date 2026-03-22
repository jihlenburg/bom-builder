# Logbook

## 2026-03-22

- Refactored the core application flow into smaller helpers across `main.py`, `bom.py`, `report.py`, `package.py`, and `mouser.py`.
- Added regression coverage for CLI helpers, report output, config logging, and Mouser pricing edge cases.
- Introduced cross-platform secret resolution in `secret_store.py` with environment-variable and fallback file support.
- Moved secret-management documentation under `docs/` and scrubbed secrets from `.env`.
- Stored the current Mouser and OpenAI API keys in the macOS Keychain under project-specific service names.
- Tightened secret access by removing runtime keychain reads from the Python app and adding `scripts/with-project-secrets.sh` as an allowlisted launcher for macOS Keychain and Linux Secret Service.
- Hardened the launcher by removing `eval`, validating registry-driven names, and rejecting weak-permission or symlinked fallback secret files.
- Switched the secure launcher to unbuffered Python output so BOM runs report progress live instead of appearing hung under non-interactive execution.
- Added persistent Mouser search caching in `lookup_cache.py` with a default 24-hour TTL and CLI controls for disabling or retuning the cache window.
- Began strengthening the Mouser resolver to prefer buyable orderable parts, retry transient API failures, and reduce unnecessary manual-review flags.
- Fixed TI package extraction for `8-SOT-23` / reel-vs-tube temperature-sensor variants, which reduced the LUPA BOM manual-review set from 10 parts to 8 and corrected the console summary split between fuzzy-resolved and true review-required matches.
- Made the Mouser pacing logic cache-aware so cached runs no longer pay the per-part `--delay` or inter-pass lookup sleeps when no network request is needed.
- Added an interactive terminal resolver with persistent saved selections in `resolution_store.py`, and expanded TI package decoding so the chooser can present package-aware candidate lists such as `SOIC-8`, `WSON-2`, `X2SON-4`, `HSOIC-8`, and `VSSOP-19`.
- Added an optional OpenAI reranker in `ai_resolver.py` that runs before interactive review, uses the Responses API with structured JSON output, and falls back cleanly when the AI abstains or the API key is invalid.
- Reverted secret handling back to a simple `.env` plus environment-variable workflow, removed the dedicated launcher/keychain registry path, and simplified the secret-loading docs and tests to match.
- Updated the default AI model to `gpt-5.4-mini` based on the current OpenAI model docs, while keeping the reranker on the Responses API.
- Refactored `ai_resolver.py` into smaller helper functions for request headers, JSON schema construction, decision parsing, and decision validation so the GPT-5.4 mini path is easier to maintain.
- Removed orphaned generated outputs, Python cache directories, and the unused temporary design file, then re-verified the repository with a full passing test run.
- Tightened `CLAUDE.md` so future work must include proper Sphinx-style Python API documentation for modules, classes, functions, properties, and meaningful internal helpers.
- Expanded the source tree to full API-doc coverage with richer module, class, function, method, property, and helper docstrings across the runtime modules, then verified both docstring coverage and the full test suite.
- Added `.env.example` and documented the optional cache / resolution path overrides, including the exact platform defaults that apply when those entries are omitted or blank.
- Added a direct single-part CLI lookup mode built on a synthetic one-line design, and fixed the console/summary classification so Mouser HTTP failures such as `TooManyRequests` are reported as lookup failures instead of misleading `No match found` misses.
- Added support for multiple Mouser API keys via `MOUSER_API_KEYS`, stored the second key as a backup in `.env`, rotated automatically to the next key on Mouser quota/rate-limit responses, and reduced wasted requests further by consulting saved resolutions before the full multi-pass lookup and by skipping redundant exact passes for qualifier-style part numbers.
- Refreshed `.gitignore` so local secrets, Python caches, generated BOM outputs, generated docs, SQLite cache files, and macOS `.DS_Store` artifacts stay out of version control.
