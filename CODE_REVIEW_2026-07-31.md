# Full Code Review — 2026-07-31

- **Scope:** entire native Go codebase at commit `a9e6b9d` (~16.7k lines, 17 packages). `legacy/` excluded.
- **Method:** four parallel reviewer agents, one per slice (CLI + contract, provider adapters,
  pricing/sourcing core, cache + config), each reviewing against the engineering rules in
  `CLAUDE.md`. All Critical findings were independently re-verified against the source by the
  coordinating session before inclusion here.
- **Baseline:** `make check` fully green — unit tests, race tests, `go vet`, native build,
  pinned Go 1.25.12.
- **Overall verdict:** healthy codebase with a consistently applied safety architecture.
  Three verified Critical defects (two in `money.Parse`, one contract drift in the output
  schema), a cluster of Important robustness issues concentrated in the NXP browser adapter
  and money text normalization, and a set of consistency/test-coverage gaps.

> **Remediation status (2026-07-31, same session):** fixed with TDD and verified by
> `make check` — all three Criticals; Important 1-6 (money normalization, NXP failure
> model incl. loadingFinished race and browser-death recovery), Important 7-9
> (CDP serialization, NXP aliases, Mouser squash), Important 10-17 (sourcing
> fallthrough, optimizer currency gate, DSN pragmas, cache Put validation, `.env`
> trust model + failure mode, input schema shapes, unknown-flag rejection),
> Important 18-20 (aggregation conflicts, deterministic errors; request-count
> attribution remains documented-only), and test-gap Importants 21-23 largely closed
> (money adversarial suite, provider 401/429/oversize tests, dotenv edge tests,
> store-level tests). Also fixed from the Minor tier: money `String()` sign guard,
> schema `purchasePlan`/`demand` property gaps (the `demand` gap was found by the new
> required⊆properties schema test, beyond the original findings), sanitizer UTF-8
> truncation (3 adapters), sourcing fallback plan leak, `CacheStatus` pragma read,
> unbalanced dotenv quotes, dotenv BOM/lowercase handling. Remaining work is tracked
> in `TODO.md` under "Code review remediation (2026-07-31)".

---

## Critical

### C1. `money.Parse` overflow check ignores the fraction → silently negative Decimal *(verified)*
- `internal/money/decimal.go:53-62`
- The guard is `wholeValue > math.MaxInt64/Scale`, but `scaled = wholeValue*Scale + fractionValue`
  can still exceed `MaxInt64`. `Parse("9223372036854.999999")` wraps to
  `-9,223,372,036,854,551,617` and returns it with `err == nil`, breaking the type's
  non-negative invariant. `String()` then renders garbage (see M-money-1), and any downstream
  `Add` with the negative operand silently shrinks totals. `UnmarshalJSON` feeds `Parse`, so
  corrupt or crafted JSON/cache input is an injection path.
- **Fix:** check `wholeValue > (math.MaxInt64-fractionValue)/Scale` (and re-check before the
  `roundUp` increment), or build with `math/bits` carry detection.

### C2. Digit-free separator strings parse as zero price *(verified)*
- `internal/money/decimal.go:150-234` (`allDigits("")` returns `true` at 231-234)
- `Parse(".")`, `Parse(",")`, `Parse("€.")` all return `0.000000` with no error. A malformed
  provider price field becomes a €0.00 line item; the optimizer accepts a zero unit price
  (`internal/procurement/optimizer.go:197`), so the BOM total silently understates cost.
- **Fix:** after symbol filtering, reject values containing no digit.

### C3. `export ec-bom` output fails the project's own published output schema *(verified)*
- `schemas/output.schema.json:56` binds top-level `artifact` to `$defs/documentArtifact`,
  which requires `source_url`, `final_url`, `mime_type`, `fetched_at`. But
  `contract.ExportArtifact` (`internal/contract/contracts.go:253-259`, emitted at
  `internal/cli/export.go:128-134`) has none of those fields. Any agent validating
  `export ec-bom` stdout against `bom-builder schema output` gets a hard failure on a
  successful export.
- **Fix:** add an `exportArtifact` def and make `artifact` a `oneOf`, or key the export
  artifact under a distinct property.

---

## Important

### Money text normalization (`internal/money/decimal.go`)
1. **Letters silently stripped → wrong magnitudes.** `Parse("1e5")` = `15.000000`,
   `Parse("2 for 1.50")` = `21.500000` (`decimal.go:168`). Failed normalization must surface,
   not produce a plausible wrong number. Reject letter runs *between* digit runs.
2. **Lone-separator ambiguity resolves US thousands to 1000x smaller.** `Parse("$1,234")` =
   `1.234000` (`decimal.go:204-213`). Violates the "failed currency normalization is an
   explicit state" rule. Treat `<digits><sep>ddd` with no second separator as ambiguous, or
   thread a locale hint from adapters.

### NXP browser adapter (`internal/provider/nxp/`)
3. **Transient failures permanently disable the client.** Any `waitEvent` error — including
   one context timeout — calls `disable(...)`, never cleared (`client.go:221-224, 246-247,
   332-338`). One slow page kills direct NXP pricing for the rest of a 200-line BOM run.
   Reserve `disable` for confirmed schema drift.
4. **No recovery after browser death.** `ensureProcess` never resets a dead `process`
   (`client.go:309-321`); a Chrome crash bricks the client for its lifetime.
5. **CDP body fetched too early.** `Network.getResponseBody` is issued on
   `responseReceived` instead of after `loadingFinished` (`client.go:208-248`); slow responses
   yield "No data found for resource" — which then triggers issue 3. Wait for
   `loadingFinished` with matching requestId.
6. **Comma-stripping defeats locale-aware money parsing** *(verified)*.
   `parser.go:204-217` does `ReplaceAll(value, ",", "")` before `money.Parse` ever sees the
   string: EU `"1.234,56"` → `"1.23456"` — a silent 1000x price error. `money.Parse` handles
   the raw form correctly; pass it through. (`parser.go:249` is safe only because its regex
   pre-filters.)
7. **`cdpProcess` is not goroutine-safe** (`cdp.go:191, 219, 240-258, 276-278`) — unsynchronized
   `nextID`/`pending`/`messages`, unlike the mutex-protected HTTP adapters. Latent (CLI is
   sequential today); serialize or document the constraint.
8. **`supportsManufacturer` rejects "NXP USA Inc."** (`resolver.go:171-187`) — normalizes to
   `"nxp usa"` ≠ `"nxp"`, silently `not_applicable`. Mouser aliases this spelling
   (`mouser/resolver.go:439`); NXP's own adapter should too.

### Resolver/sourcing state discipline
9. **Mouser broad-pass squash masks explicit states.** When `reviewRequired`, the selected
   candidate's status/code are overwritten with `review`/`REVIEW_REQUIRED` even when it was
   `shortage`/`INSUFFICIENT_STOCK`, `stock_unknown`, or `unavailable`
   (`mouser/resolver.go:106-112` vs 159-188). A loose match with a shortage reports as merely
   "needs review". Keep the blocking status and add the review flag.
10. **Unrecognized status strings fall through the sourcing summary uncounted**
    (`internal/sourcing/source.go:65-106`). Add a `default:` case mapping to
    `INTERNAL_CONTRACT_ERROR` with an issue.
11. **Latent cross-currency plan comparison in the optimizer.**
    `selectBestPlan`/`comparePlanCost` compare `ExtendedPrice` micro-values across families
    with no currency check (`optimizer.go:329-345, 357-368`); per-family currency is enforced
    but cross-family is not. Latent (all adapters pass one family today) but it is the public
    multi-family API. Mirror `multi.go:110-119`'s explicit refusal.

### Cache & config (`internal/lookupcache/`, `internal/config/`)
12. **Session pragmas set on the pool, not per connection** (`store.go:179-187`). If
    `database/sql` recycles the single connection, the replacement has `busy_timeout = 0` →
    immediate `SQLITE_BUSY` under cross-process contention, silently absorbed as an errorCount.
    Encode pragmas in the DSN (`?_pragma=busy_timeout(5000)...`).
13. **`Put` does not validate what `decodeRow` will reject — and corrupt reads are fatal.**
    (`store.go:296-350` vs `store.go:663-676`; `resolver.go:150-153` deliberately has no
    network fallback on corrupt entries.) The first adapter that emits a new status value
    self-poisons the cache: every later lookup of that part hard-fails until expiry/prune.
    Make `Put` run the same whitelist (ideally a full `decodeRow` round-trip).
14. **CWD-relative `.env` can redirect authenticated provider traffic.**
    `LoadDotEnv(".env")` (`internal/cli/run.go:44`) won't override an inherited API key, but
    it happily *sets* `BOM_BUILDER_MOUSER_API_URL` etc., and clients accept any http/https
    host (`mouser/client.go:78-81`, similar in digikey/ti/nxp). Running in an untrusted
    checkout can send the real key from the shell environment to an attacker host. Honor
    endpoint overrides only from the real process environment, or require the `.env` to be
    user-owned 0600, or document the trust model.
15. **A malformed `.env` bricks every machine command with exit 5.** Lowercase keys
    (`foo=bar`, legal POSIX) abort `Run` before dispatch with `CONFIG_ERROR`, command
    `"startup"`, `ExitInternal` (`run.go:44-46`, `dotenv.go:77-89`). Even `capabilities` and
    `cache status` die. User-authored file problems should be exit 2 (or per-line warnings).

### Contract & CLI consistency
16. **Input schema under-describes accepted input.** The loader accepts a single design, a
    top-level array, and a `{"designs": [...]}` wrapper (`internal/design/load.go:76-102`);
    `schemas/input.schema.json` describes only the single object. Express all three via
    `oneOf` or drop the extra forms.
17. **Unknown `--flags` silently become positional values** in `lookup` (`run.go:305-324`),
    `documents list` (`documents.go:87-97`), `alternatives` (`alternatives.go:47-57`), and
    `validate` (`run.go:256-262`) — inconsistent with `price`/`export`, which guard
    (`run.go:441-445`, `export.go:66-70`). `lookup --stock-verify --manufacturer Yageo`
    queries providers for the literal MPN `--stock-verify`. Apply the same `--` rejection
    everywhere.
18. **Silent first-wins merge in aggregation drops conflicting compatibility metadata.**
    Duplicate part keys across designs take `Description`/`Package`/`Pins` from the first
    occurrence only (`internal/bom/aggregate.go:45-59`); a 0402-vs-0603 conflict is dropped
    with no issue. Emit a stable warning (e.g. `AGGREGATION_METADATA_CONFLICT`) and prefer
    non-empty over empty.
19. **Nondeterministic validation-error selection via map iteration** *(found independently
    by two reviewers)*. `for field, value := range numericFields(part)`
    (`internal/alternatives/load.go:131`) makes the `INVALID_INPUT` field named in the CLI
    envelope vary run-to-run. Iterate a sorted field list.

### Concurrency accounting
20. **Request-count attribution is not concurrency-safe and the constraint is undocumented.**
    `sourceRequests` before/after diff of a shared counter (`lookupcache/resolver.go:173-178`)
    is correct only because lookups are strictly sequential (`sourcing/multi.go:57-62`).
    Document the single-threaded-lookup contract or attribute per call.

### Test gaps (rule: tests accompany behavior)
21. **Money core is missing adversarial tests** (`decimal_test.go`: 4 tests): no overflow
    boundary (would have caught C1), no `"."`/`","` (C2), no `"$1,234"` pin, no
    `Add`/`MulInt` overflow, no `DivInt` half-rounding case.
22. **Provider retry/auth paths untested:** 429/5xx backoff, `Retry-After`, DigiKey/TI
    401→refresh, oversized-body rejection, Mouser 429-without-spare-key.
23. **No `store_test.go`:** `List`, `CacheStatus`, `Verify`, newer-schema rejection
    (`store.go:203-212`) undirectly tested; dotenv edge branches (export prefix, CRLF,
    inline `#`, unbalanced quotes) unpinned.
24. **CLI error paths thin:** `PROVIDER_CONFIGURATION_ERROR`/exit 4, `UNSUPPORTED_PROVIDER`,
    deadline/units/attrition bounds, `--pretty` shape, alternatives all-fail, and no test
    asserts stderr stays empty for machine commands.

---

## Minor

**money/pricing**
- `String()` renders negative values as `"-9223372036854.-551617"` (`decimal.go:80-85`); any
  package can construct `money.Decimal(-5)` directly. Guard sign in `String()`.
- `familyStrategy` mislabels MOQ-forced up-buys as `"next price break"`
  (`optimizer.go:466-475`); price correct, strategy string wrong.
- Dimension/ESR comparisons omitted from the audit trail when the original lacks them
  (`alternatives/evaluate.go:116-125, 153-158`); emit `"not_required"` rows.
- `documents/fetch.go:130, 174` — `keepTemporary` is dead code.
- `documents/fetch.go:55-79` — no client-level timeout (CLI always sets a context; library
  standalone use can stall).
- `sourcing/multi.go:154-170` — fallback path never calls `clearSelectedPlans`; a misbehaving
  adapter's plan leaks into `output.Offers` and alternatives ranking
  (`evaluate.go:336-346`).
- `sourcing/source.go:116-122` — `MIXED_CURRENCY` counted as `provider_error_count`
  (conflates normalization refusal with transport failure; pinned by tests — change
  deliberately).

**providers**
- DigiKey `headerMode = "account_id"` hard-coded/vestigial (`digikey/client.go:275-278, 318`).
- Token refresh thundering herd possible in DigiKey/TI (`digikey/client.go:338-345`,
  `ti/client.go:238-245`).
- 429 handling ignores `Retry-After` in all adapters.
- DigiKey swallows `ProductInformation` transport errors into bare `stock_unknown`
  (`digikey/resolver.go:100-107`).
- NXP `disabled` checked after `ensureProcess`; `PartDetail` never checks it
  (`nxp/client.go:187-193, 268-307`).
- NXP `PartDetail` fixed 1500 ms sleep before reading MOQ (`client.go:291-292`) — slow loads
  degrade every lookup to `MOQ_REVIEW_REQUIRED`.
- `cdp.go:135-148` — readLoop can block on the buffered channel if the consumer errors
  mid-`Search`; `Network.enable` left on after error paths.
- Mouser <3-char part numbers surface as provider `input` error, not an explicit state
  (`mouser/client.go:151-157`).
- No diacritic folding: "Würth Elektronik" vs "Wurth Elektronik" → false `PART_NOT_FOUND`
  (`mouser/resolver.go:411-457`).
- Duplicated helpers across adapters (`waitForRetry` ×3, `validCurrency` ×3,
  `normalizePartNumber` ×4, `plausiblyRelated` ×3) — drift risk; consider `providerutil`.
- Sanitizer truncation `message[:300]` can split a UTF-8 rune (three adapters).
- `ti/types.go:82-108` — `flexibleInt` rejects `3000.0`-shaped values.
- `provider/health.go:81, 131, 179, 233` — live checks pin specific catalog part numbers;
  worth a comment acknowledging the coupling.

**cache/config**
- Inline `#`-comments not stripped: `KEY=abc123 # prod` keeps the comment in the value
  (`dotenv.go:64`) — silent auth failures.
- Unbalanced double quote kept literal instead of erroring (`dotenv.go:65-73`).
- `export\tKEY`, bare `export KEY`, UTF-8 BOM on line 1 → confusing hard failures
  (`dotenv.go:55, 77-89`).
- `MkdirAll(parent, 0o700)` doesn't tighten a pre-existing looser directory (`store.go:117`).
- `CacheStatus` reports compile-time `SchemaVersion`, not `PRAGMA user_version`
  (`store.go:357`).
- Orphaned old-adapter-version entries counted as "fresh" for up to 365 d; no prune scope for
  superseded `adapter_version` rows (`types.go:26`, `store.go:362-368, 503-598`).
- `constantTimeTextEqual` used for non-secret checksums/keys (`store.go:602, 620, 629`) —
  implies a security property that doesn't exist.

**CLI/contract**
- Dead duplicate `--pretty` consumption in three subcommands (`run.go:188-193`,
  `cache.go:296-301`, `documents.go:54-59`).
- Part-number length limit counts bytes, not runes (`run.go:315`, dup at
  `documents.go:97-98`).
- `--providers=""` → misleading `UNSUPPORTED_PROVIDER` code (`run.go:926-934`).
- `--max-bytes -5` bypasses `INVALID_MAX_BYTES` (`documents.go:266-279`).
- `consumeValueFlag` consumes a following flag as a value (`run.go:798-827`).
- Stray trailing tabs in several help topics (`help.go:180, 198, 201, 208, 214, 223, 226`).
- `capabilities.commands` entries aren't all valid help topics; `--help price` shows the
  general page (`run.go:108-129`, `help.go:20-23, 53`).
- `cache.go:322-352` Lstat-then-Open TOCTOU; user-vs-system exit-code split inverted between
  `cache.go:408-422` (user error → 5) and `cache.go:309-314` (internal error → 2).
- `contracts.go:63` — JSON tag `ti_transport_implemented` vs field
  `TITransportImplementation`.
- `ecbom.go:32-48` — no Quantity vs len(Designators) consistency check (shipped test exports
  4 vs 2); no CSV formula-injection guard for `=`/`+`/`-`/`@`-leading cells.
- `output.schema.json:350-363` — `purchasePlan.required` lists `pricing_strategy` and
  `order_plan`, but neither appears under `properties`.

---

## Strengths (abridged; full detail in reviewer transcripts)

- **Money rule honored end to end:** no binary floats anywhere in the pricing slice
  (`big.Int`/`big.Rat` for ratios), `json.Number` + `UseNumber()` in adapters, string-only
  money JSON with schema-pinned 6-place pattern.
- **Credential hygiene designed-in and tested:** per-adapter sanitizers with leak tests;
  secrets in POST bodies/headers; Mouser suppresses `net/http` error text because its API
  forces the key into the query string; cache round-trip test proves reuse with credentials
  removed from the environment.
- **Fail-closed state discipline:** dual-layer safe-plan contract checks
  (`multi.go:81-93`, `source.go:66-79`), explicit `CURRENCY_CONVERSION_REQUIRED` /
  `MIXED_CURRENCY` refusals, `EngineeringReviewRequired` hardcoded true with schema
  `const true`.
- **The lookup cache is exceptionally engineered:** normalized-results-only single write
  path, versioned keys with documented adapter bumps, paranoid checksum/whitelist/identity
  read path, token-guarded transactional prune, 0600/0700 + symlink hardening, all tested.
- **One-JSON-document discipline enforced structurally** with centralized envelopes and a
  stable exit-code taxonomy; deterministic ordering throughout (stable sorts with total-order
  final keys).
- **Optimizer correctly detects buy-up savings** (cheaper-at-higher-break), with
  overflow-guarded quantity math.
- **Serious SSRF hardening in documents fetch:** public-IP validation at validation *and*
  dial time, redirect re-validation, port pinning, `O_EXCL` writes.
- **Tests are fully offline** (httptest/stub transports; no live endpoints).

## Per-slice assessments

- **CLI + contract:** good health; thin CLI, stable codes, meticulous cleanup. Fix the two
  contract-drift items first — the project's premise is machine-consumable contracts.
- **Providers:** DigiKey/Mouser/TI share a disciplined, high-quality template. NXP browser
  adapter is the clear weak point (brittle failure model, CDP race, money comma-strip).
- **Pricing core:** structurally sound safety architecture; `money.Parse` normalization is
  the weak spot; optimizer needs the cross-currency gate before multi-family callers appear.
- **Cache + config:** strongest slice; second-order risks only (pragma-per-connection,
  Put/decodeRow asymmetry, `.env` trust model).

## Suggested fix order

1. C1 + C2 + Important 1-2 together (one `money.Parse` hardening pass + adversarial tests).
2. C3 + Important 16 (schema drift; both are single-file schema fixes plus a pinning test).
3. Important 6 (NXP comma-strip) and Important 9 (Mouser squash) — live money/state bugs.
4. Important 13 (cache Put validation) — cheap now, expensive after a status is added.
5. Important 3-5, 7-8 (NXP failure model) as one adapter-hardening task.
6. Important 14-15 (`.env` trust + failure mode) as one deliberate trust-model decision.
7. Remaining Importants, then Minors opportunistically.
