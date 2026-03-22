# Tasks

Active tasks should be recorded here while work is in progress and checked off only after implementation and verification.

## Current Cycle

- [x] Add cross-platform secret loading with environment and file fallback
- [x] Remove committed secrets from `.env`
- [x] Start documentation under `docs/` and add a secret-management guide
- [x] Add a project logbook and persistent task tracking
- [x] Update `CLAUDE.md` with the current development workflow and repository rules
- [x] Add a persistent Mouser lookup cache with 24-hour retention
- [x] Tighten secret access by removing runtime keychain reads and adding an allowlisted launcher
- [x] Harden the launcher and fallback secret file handling against broad access
- [x] Re-run the LUPA BOM and measure resolver improvements
- [x] Add an interactive resolver UI with saved manual selections
- [x] Add optional AI-assisted reranking only for still-ambiguous matches
- [x] Revert secret handling back to a simple `.env` workflow and remove launcher/keychain code
- [x] Align the OpenAI resolver default to `gpt-5.4-mini`
- [x] Refactor the OpenAI resolver into smaller request/parse/validation helpers
- [x] Remove orphaned generated files and stale launcher references after the `.env` rollback
- [x] Tighten `CLAUDE.md` to require proper Python API documentation for all future code
- [x] Add source-wide Sphinx-style API documentation across all Python modules
- [x] Document optional cache and resolution path overrides in `.env` and docs
- [x] Add direct single-part CLI lookup mode and separate API lookup failures from true no-match cases
- [x] Add Mouser multi-key fallback and reduce wasted requests on saved resolutions and exhausted daily quotas
- [x] Refresh `.gitignore` for local env files, caches, generated docs, and BOM outputs
- [ ] Add a generated Sphinx API documentation toolchain under `docs/`
- [ ] Tighten deterministic Mouser resolution and ambiguity handling
