# Interactive Resolution

`bom-builder` can stop on genuinely ambiguous parts and let you choose the correct candidate from the terminal.

## Usage

```bash
python main.py -d LUPA_48VGen_BOM.json -u 1000 --interactive
```

Or enable the AI reranker first and fall back to interactive review only if the model abstains:

```bash
python main.py -d LUPA_48VGen_BOM.json -u 1000 --ai-resolve --interactive
```

The interactive resolver only prompts when the preceding stages still require review. Confident exact, begins-with, fuzzy-resolved, saved, and AI-reranked matches continue automatically.

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

When you select a candidate, the choice is saved and reused automatically on future runs.

Default path:

```text
~/.config/bom-builder/resolutions.json
```

Override with:

```bash
export BOM_BUILDER_RESOLUTIONS_FILE=/path/to/resolutions.json
```

## Resolver Order

The resolver now runs in this order:

1. deterministic Mouser lookup
2. manufacturer/package enrichment
3. saved manual resolution reuse
4. optional OpenAI reranking when `--ai-resolve` is enabled
5. interactive selection when `--interactive` is enabled

The AI stage only reranks candidates that already came from deterministic search. It does not invent new part numbers.
