# Tasks

## Development record

- [x] Establish a clean Go module at repository root
- [x] Pin the toolchain to Go 1.25.12 and current compatible dependencies
- [x] Add typed public contracts and stable exit codes
- [x] Add safe `.env` loading without shell evaluation
- [x] Add `capabilities --full`, provider discovery, and schema commands
- [x] Add strict file/stdin design validation
- [x] Add a reproducible native build helper
- [x] Add unit and command-contract tests
- [x] Port the money model using exact fixed-point decimal arithmetic
- [x] Port the purchase-plan optimizer and its behavioral test cases
- [ ] Add serialized cross-language optimizer golden fixtures
- [x] Implement and live-verify the Mouser adapter
- [x] Add native `lookup`, `price`, and `providers check` commands
- [x] Implement and live-verify the Digi-Key adapter
- [x] Compare safe Mouser and Digi-Key plans with per-provider provenance
- [x] Implement and live-verify the pure-Go TI Store adapter
- [x] Add safe datasheet discovery and verified PDF downloads
- [x] Add stock-aware resistor, capacitor, and inductor alternative evaluation
- [x] Add the versioned native SQLite lookup cache and safe management commands
- [x] Implement and live-verify the credential-free Microchip Product API
      availability/lifecycle evidence provider (no pricing, never a
      selected plan)
- [x] Declare GPL-3.0-or-later: document the license in `README.md` and carry
      the SPDX identifier in every Go source file
- [x] Add the concurrent-safe, audited approved-resolution store and the
      `resolutions approve|list|history|revoke` commands
- [x] Make Windows a verified compilation target: `windows/amd64` cross-build
      and vet in `make check`, portable SQLite DSNs, Windows browser
      discovery, and an explicit `unsupported_platform` state for the NXP
      pipe transport
- [x] Start the interactive terminal mode: a Bubble Tea resolutions manager
      (`bom-builder interactive`) with a TTY gate and a fully testable
      model layer
- [x] Add the interactive resolver flow: in-interface lookup, candidate
      review, and approval seeding with per-lookup provider runtimes
- [x] Add the local web UI (`bom-builder serve`): loopback-only, token-
      gated, embedded stdlib-only frontend mirroring the TUI, verified
      end to end in a headless browser
- [x] Remove the NXP Store adapter and all of its wiring (provider
      package, discovery, health, selection, cache identity, restricted
      env keys, schemas, docs); keep historical values readable in stored
      resolutions and cache listings
- [x] Add CI (gofmt + full `make check` on every push and pull request)
      and the eC-BOM CSV formula-injection guard
- [x] Consume approved resolutions in `lookup`/`price` with narrow
      SKU-pinned review lifting
- [x] Add the FX layer: dated ECB quotes and `--currency` converted totals
- [x] Add the provider registry (single definition point) and
      `--providers` exclude syntax
- [x] Polish all documentation: accuracy pass against current behavior,
      corrected `.env.example` (restricted keys and stale variables),
      updated `CLAUDE.md` architecture
- [x] Make the project third-party ready: Windows runtime tests in CI,
      the tag-triggered release workflow (six-platform archives plus
      SHA256SUMS), install and API-key documentation, interface-stability
      policy, CONTRIBUTING.md, SECURITY.md, and the v3.0.0 release
