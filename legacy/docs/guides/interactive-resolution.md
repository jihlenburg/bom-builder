# Interactive Resolution

BOM Builder can pause on genuinely ambiguous parts and let you choose the
correct candidate interactively.

## Modes

### CLI mode (default)

The text-based resolver prints candidates to the terminal and reads your
selection from stdin. It works in any terminal and requires no additional
dependencies.

```bash
python main.py -d designs/board.json -u 1000 --interactive
```

### TUI mode (full-screen)

When `--interactive` is used on a TTY, BOM Builder launches a full-screen
Textual terminal UI.  The TUI shows a live-updating parts table, running
cost totals, and a modal dialog for candidate resolution.

The TUI requires the `textual` package:

```bash
pip install textual
```

### Combined with AI reranking

Enable the AI reranker first and fall back to interactive review only when
the model abstains:

```bash
python main.py -d designs/board.json -u 1000 --ai-resolve --interactive
```

The interactive resolver only prompts when the preceding stages still require
review. Confident exact, begins-with, fuzzy-resolved, saved, and AI-reranked
matches continue automatically.

## CLI Terminal Commands

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

## TUI Keyboard Shortcuts

The full-screen TUI modal supports:

| Key | Action |
|-----|--------|
| `Enter` | Select the highlighted candidate row |
| `a` | Accept the current suggested match |
| `s` / `Escape` | Skip this part |
| `q` | Quit the entire run |
| Arrow keys | Navigate the candidate table |

The TUI also shows clickable action buttons at the bottom of the modal.

## Saved Resolutions

When you select a candidate (in either CLI or TUI mode), the choice is saved
and reused automatically on future runs.

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
5. **Interactive selection** -- terminal chooser or TUI modal when `--interactive` is enabled
6. **Cross-distributor pricing** -- Digi-Key, TI Store, and NXP direct are queried for resolved parts
7. **Offer selection** -- cheapest confident offer wins after surplus and packaging-plan comparison

## TUI Architecture

The TUI is built with [Textual](https://textual.textualize.io) and lives in
the `tui/` package.  It separates the synchronous pricing pipeline from the
async UI event loop with a clean threading model:

- **Worker thread** runs the same `_price_single_part()` function used by the
  CLI, posting progress via `app.post_message()` (thread-safe).
- **Resolver rendezvous** bridges the two threads for interactive resolution.
  A `concurrent.futures.Future` allows the worker to block on
  `rendezvous.wait()` and the modal to wake it instantly with
  `rendezvous.resolve()`.  On app shutdown, `rendezvous.cancel()` raises
  `CancelledError` in the worker so it never hangs.
- **Shutdown event** (`threading.Event`) handles iteration boundaries where no
  Future exists yet.

```
Worker Thread                          UI Thread (Textual event loop)
─────────────                          ───────────────────────────────

for each part:
  check shutdown_event ──────────────→ shutdown_event.set() on quit
  post_message(Started) ────────────→ on_part_pricing_started()
  _price_single_part()

  [if ambiguous]
    create ResolverRendezvous(Future)
    post_message(Request) ──────────→ on_resolver_request()
    rendezvous.wait()  ← blocks        push ResolverModal
                                         user picks candidate
                ← rendezvous.resolve() ← modal._resolve_with_candidate()
    returns resolved lookup

  post_message(Completed) ──────────→ on_part_pricing_completed()

                                       [on quit]
                                         shutdown_event.set()
                ← rendezvous.cancel()  ← action_quit() → CancelledError
                                         app.exit()
```
