# Interactive Resolution

BOM Builder can pause on genuinely ambiguous parts and let you choose the
correct candidate from the terminal.

## Usage

```bash
python main.py -d designs/board.json -u 1000 --interactive
```

Or enable the AI reranker first and fall back to interactive review only when
the model abstains:

```bash
python main.py -d designs/board.json -u 1000 --ai-resolve --interactive
```

The interactive resolver only prompts when the preceding stages still require
review. Confident exact, begins-with, fuzzy-resolved, saved, and AI-reranked
matches continue automatically.

## Terminal Commands

For each ambiguous part, the terminal UI shows:

- ranked manufacturer part numbers
- detected package and pin count
- unit price at your requested quantity
- availability
- the resolver's current suggested choice

Available commands:

- `[number]` select a specific candidate
- `a` accept the suggested top candidate
- `n` / `p` move through pages when there are many candidates
- `s` skip and leave the part flagged for review
- `q` abort the run

## Saved Resolutions

When you select a candidate, the choice is saved and reused automatically on
future runs.

Default path:

```text
~/.config/bom-builder/resolutions.json
```

Override with:

```bash
export BOM_BUILDER_RESOLUTIONS_FILE=/path/to/resolutions.json
```

To clear all saved selections:

```bash
python main.py --flush-resolutions
```

## Resolver Pipeline

Each BOM line passes through these stages in order. A line exits the pipeline
as soon as a stage resolves it with confidence.

1. **Saved resolution reuse** -- previously saved manual or AI-confirmed selections
2. **Deterministic Mouser lookup** -- exact, begins-with, and fuzzy multi-pass search
3. **Manufacturer/package enrichment** -- product-page and manufacturer-page packaging details
4. **AI reranking** -- optional OpenAI reranker when `--ai-resolve` is enabled; abstains on underspecified input
5. **Interactive selection** -- terminal chooser when `--interactive` is enabled
6. **Cross-distributor pricing** -- Digi-Key, TI Store, and NXP direct are queried for resolved parts
7. **Offer selection** -- cheapest confident offer wins after surplus and packaging-plan comparison
