# Tasks

## Current Go Rewrite Cycle

- [x] Preserve the Python implementation under `legacy/`
- [x] Establish a clean Go module at repository root
- [x] Pin the rewrite to Go 1.25.12 and current compatible dependencies
- [x] Add typed public contracts and stable exit codes
- [x] Add safe `.env` loading without shell evaluation
- [x] Add `capabilities --full`, provider discovery, and schema commands
- [x] Add strict file/stdin design validation
- [x] Add a reproducible native build helper
- [x] Add unit and command-contract tests
- [x] Port the money model using exact fixed-point decimal arithmetic
- [x] Port the purchase-plan optimizer and legacy behavioral cases
- [ ] Add serialized cross-language optimizer golden fixtures
- [x] Implement and live-verify the Mouser adapter
- [x] Add native `lookup`, `price`, and `providers check` commands
- [x] Implement and live-verify the Digi-Key adapter
- [x] Compare safe Mouser and Digi-Key plans with per-provider provenance
- [x] Implement and live-verify the pure-Go TI Store adapter
- [x] Implement and live-verify the browser-backed native Go NXP adapter
- [x] Add safe datasheet discovery and verified PDF downloads
- [x] Add stock-aware resistor, capacitor, and inductor alternative evaluation
- [x] Add the versioned native SQLite lookup cache and safe management commands
