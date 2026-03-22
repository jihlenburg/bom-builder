# CLAUDE.md — Coding Agent Instructions for bom-builder

## Project Overview

BOM Builder is a Python CLI tool for building and pricing electrical Bills of Materials (eBOM) via the Mouser Search API. It reads design JSON files, aggregates parts, performs multi-pass fuzzy part number lookups, and generates priced BOMs in CSV/Excel/JSON.

## Tech Stack

- Python 3.12+
- pydantic for data models and validation
- httpx for HTTP requests (with connection pooling via `MouserClient`)
- openpyxl for Excel output
- pyyaml for config files (manufacturers.yaml, packages.yaml)
- python-dotenv for environment management
- pytest for testing

## Architecture Decisions

### Why Pydantic models over plain dicts
All structured data flows through typed Pydantic models (`Part`, `Design`, `AggregatedPart`, `PricedPart`, `BomSummary`). This was a deliberate choice to catch validation errors early (e.g. negative quantities, missing required fields) and to make the data flow between modules explicit and type-safe. The `AggregatedPart` → `PricedPart` conversion uses `PricedPart.from_aggregated()` to keep the boundary clean.

### Why multi-pass fuzzy lookup
Electronic component part numbers are notoriously inconsistent between BOMs and distributors. A designer might write "TMP423-Q1" but Mouser only knows "TMP423AQDCNRQ1". The 3-pass strategy (Exact → BeginsWith → Stripped+BeginsWith) with manufacturer-aware scoring was designed to handle this gracefully while always flagging uncertain matches for human review.

### Why YAML for config data
Manufacturer aliases, package patterns, and vendor codes change frequently and are domain knowledge, not application logic. Keeping them in YAML files makes them editable by engineers who don't touch Python code, and avoids code changes for data updates.

### Why `logging` over custom `_diag()`
The project uses Python's standard `logging` module instead of a custom diagnostic function. This gives us proper log levels (DEBUG for verbose pass-by-pass output, WARNING for parse failures, etc.), configurable output, and compatibility with standard tooling. In verbose mode, diagnostic output is intentionally routed to stdout so users can capture a full trace with `tee`.

### Why `MouserClient` as a class
The Mouser API client is a class with `__enter__`/`__exit__` for proper connection pooling and cleanup. This avoids creating a new HTTP connection per request and makes the client injectable for testing.

### Why `BomSummary` is computed once
Summary statistics (total cost, error counts, etc.) are computed once via `BomSummary.from_parts()` and passed to both the report writer and the console summary. This eliminates the DRY violation where every output format was independently computing the same totals.

## Code of Conduct for Coding Agents

### General Rules

- Do not commit or push to git automatically. Always ask the user for permission first.
- Do not commit secrets to the repository.
- Secrets are loaded through `secret_store.py` via CLI override, environment variable, or `.env`.
- Do not expose API keys in code, logs, or output.
- Do not add unnecessary dependencies. Prefer the standard library where possible.
- Keep changes minimal and focused. Do not refactor surrounding code unless explicitly asked.
- Do not create new files unless strictly necessary. Prefer editing existing files.

### Development Cycle

Every substantial change should follow this full cycle:

1. Inspect the relevant code, tests, and runtime behavior before changing anything.
2. Create or update task entries in `TASKS.md` for the work you are about to do.
3. Implement the smallest coherent slice that materially improves the project.
4. Add or update tests for new logic and edge cases.
5. Run verification locally (`pytest`, targeted CLI runs, or both).
6. Mark verified tasks as complete in `TASKS.md`.
7. Record meaningful accomplishments and behavior changes in `LOGBOOK.md`.
8. Update user-facing documentation under `docs/` and lightweight pointers in `README.md` when behavior or setup changes.
9. Only then present results to the user with remaining risks or follow-up items.

### Task Tracking

- `TASKS.md` is the working checklist for the current and near-future implementation cycle.
- Tasks should be added before coding and checked off only after implementation and verification.
- Leave unfinished tasks unchecked rather than deleting them.

### Logbook

- `LOGBOOK.md` is the durable engineering record of what was actually accomplished.
- Log completed work, important design decisions, and notable verification outcomes.
- Keep entries concise, dated, and factual.

### Documentation and Comments Strategy

- **Python API documentation is mandatory**: Treat Python docstrings as first-class API documentation, not optional comments. From now on, every Python change should either preserve or improve the documentation quality of the touched code.
- **Preferred style**: Use Sphinx-compatible Python docstrings by default. For non-trivial modules, classes, functions, methods, properties, and classmethods, document intent plus structured sections such as `Parameters`, `Returns`, `Raises`, `Notes`, or `Attributes` when they add value.
- **Module docstrings**: Every Python file MUST have a module-level docstring explaining what the module does, its role in the architecture, key design decisions, and how it fits into the BOM-builder pipeline.
- **Function/method docstrings**: Every public function, class, classmethod, property, and any meaningful internal helper MUST have a docstring explaining what it does, its parameters, return values, and important side effects or edge cases. Use plain English, not just parameter type restating.
- **Model documentation**: Data models should document what phase of the pipeline they belong to, what invariants they enforce, and which fields are optional hints versus required data.
- **Inline comments**: Add comments for any non-obvious logic — especially scoring weights, regex patterns, domain-specific knowledge (e.g. "TI uses -Q1 suffix for automotive qualification"), and algorithmic choices. When in doubt, over-comment rather than under-comment.
- **Architecture comments**: Use section dividers (`# ---------------------------------------------------------------------------`) to group related functions within a module. Add a brief comment explaining the purpose of each section.
- **Design rationale**: When making a choice between alternatives (e.g. "why BeginsWith instead of Contains"), document the reasoning in a comment near the code.
- **Be verbose**: Comments and docstrings should help someone unfamiliar with electronic component naming conventions understand the domain logic. Explain WHY, not just WHAT.
- **No undocumented new code**: Do not introduce new Python functions, classes, or modules without adding or updating the corresponding API documentation in the same change.

### Code Style

- Use type hints on all function signatures, including return types.
- Use pydantic models for structured data, not plain dicts.
- Keep functions short and single-purpose.
- Use f-strings for formatting.
- Use `MatchMethod` enum instead of raw strings for match methods.
- Follow existing patterns in the codebase — look at neighboring code before writing new code.

### Documentation Placement

- Long-form guides and project documentation belong under `docs/`.
- `README.md` should stay high-level and point to the deeper guides in `docs/`.
- Operational project artifacts such as `TASKS.md` and `LOGBOOK.md` may live at the repository root.
- Security-sensitive launch helpers should live under `scripts/`.

### Architecture Rules

- Configuration data (manufacturer aliases, package mappings) belongs in YAML files, not hardcoded in Python.
- API interaction logic stays in `mouser.py`. Do not scatter HTTP calls across modules.
- Data models stay in `models.py`. All model-to-model conversions use class methods.
- Report/output logic stays in `report.py`. Summary stats come from `BomSummary`.
- CLI argument parsing and orchestration stays in `main.py`.
- Package extraction logic stays in `package.py`.
- Diagnostic/debug output uses `logging`. In verbose mode it is intentionally routed to stdout for easy capture with `tee`; otherwise normal warnings/errors go to stderr.

### Mouser API

- Always respect rate limits. Default delay between requests is 1.0s, plus 0.3s between passes within a single part lookup.
- Retry transient failures and Mouser throttling with bounded exponential backoff before failing.
- Never log or print the API key.
- Use the `MouserClient` context manager for connection pooling.
- Filter out EVMs, dev kits, and evaluation boards from search results.
- The API base URL is `https://api.mouser.com/api/v2/search/partnumber`.

### Testing

- All pure logic functions (scoring, matching, parsing, aggregation) MUST have unit tests in `tests/`.
- Tests should not require API calls — mock or avoid network access.
- Run `pytest tests/ -v` before submitting changes.
- When testing API-dependent code interactively, prefer `--dry-run` or small targeted scripts.
- Use `--verbose` / `-v` flag to enable diagnostic output for debugging.

### File Conventions

- Design input files go in `designs/`.
- Output files are generated in the working directory.
- `.env` is gitignored and may contain local developer API keys and overrides.
- YAML config files (`manufacturers.yaml`, `packages.yaml`) live in the project root alongside the Python modules.
