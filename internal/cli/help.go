// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/app"
	"github.com/jihlenburg/bom-builder/internal/contract"
)

func helpRequest(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	if args[0] == "help" {
		return strings.Join(args[1:], " "), true
	}
	for _, argument := range args {
		if argument == "--help" || argument == "-h" {
			if args[0] == "--help" || args[0] == "-h" {
				return "", true
			}
			target := args[0]
			if (target == "cache" ||
				target == "documents" ||
				target == "providers" ||
				target == "resolutions") &&
				len(args) > 1 &&
				args[1] != "--help" &&
				args[1] != "-h" &&
				!strings.HasPrefix(args[1], "-") {
				target += " " + args[1]
			}
			return target, true
		}
	}
	return "", false
}

func runHelp(target string, stdout io.Writer) int {
	target = strings.ToLower(strings.TrimSpace(target))
	help, exists := commandHelp[target]
	if !exists {
		fmt.Fprintf(
			stdout,
			"Unknown help topic %q. Run `bom-builder help` for available commands.\n",
			target,
		)
		return contract.ExitInput
	}
	fmt.Fprint(stdout, help)
	return contract.ExitOK
}

var commandHelp = map[string]string{
	"": `bom-builder ` + app.Version + ` — deterministic electronic BOM sourcing

Usage:
  bom-builder <command> [options]

Machine discovery:
  bom-builder capabilities --full --pretty
  bom-builder schema output --pretty

Commands:
  alternatives <file|->    Compare and source proposed passive replacements
  cache <subcommand>        Inspect, verify, and safely prune lookup cache
  capabilities             Return the command/feature/schema manifest
  validate <file|->        Strictly validate one or more design files
  lookup <mpn>             Source and price one part
  price <file|->           Aggregate and price one or more designs
  documents list <mpn>     Discover datasheet and product-page links
  documents fetch <url>    Download and verify one PDF without overwriting
  export ec-bom <file|->   Write a Eurocircuits assembly BOM (upload-ready CSV)
  interactive              Manage approved resolutions in a terminal UI
  serve                    Serve the local web UI for approved resolutions
  providers list           Report safe provider configuration facts
  providers check          Run configuration or bounded live checks
  resolutions <subcommand> Record and audit human-approved part resolutions
  schema <target>          Return a public JSON Schema

Run ` + "`bom-builder help <command>`" + ` for copyable examples.
`,
	"capabilities": `Usage:
  bom-builder capabilities [--full] [--pretty]

Example:
  bom-builder capabilities --full --pretty
`,
	"alternatives": `Usage:
  bom-builder alternatives <request.json|-> [--providers <auto|list>]
    [--only-if-shortage] [--deadline <duration>] [cache-options] [--pretty]

The request identifies a resistor, capacitor, or inductor; its original
specifications; and up to 25 proposed candidates. Decimal physical values are
strings. Missing critical candidate data remains unknown, and every result
requires engineering review.

` + "`--only-if-shortage`" + ` sources candidates only when the original part
does not have a safe in-stock plan.

Example:
  bom-builder alternatives alternatives.json --providers auto --pretty
`,
	"validate": `Usage:
  bom-builder validate <design.json|-> [...] [--pretty]

Examples:
  bom-builder validate design.json --pretty
  generate-design | bom-builder validate -
`,
	"export": `Usage:
  bom-builder export ec-bom <design.json|-> --output <file.csv> [--pretty]

Renders ONE validated design as a Eurocircuits assembly BOM:
semicolon-separated, CRLF line endings, upload-ready column set
(Item;Quantity;Designators;Manufacturer;MPN;Description;Value;
Package;Mounted;Comment). Optional part fields designators, value,
mounted, and comment fill the matching columns; parts without a
mounted flag export as mounted. Existing output files are never
overwritten. No provider credentials are required.

Example:
  bom-builder export ec-bom design.json --output ec_bom.csv --pretty
`,
	"lookup": `Usage:
  bom-builder lookup <mpn> --manufacturer <name>
    [--quantity <n>] [--providers <auto|list>] [--deadline <duration>]
    [cache-options] [--resolutions-db <path>] [--ignore-resolutions]
    [--currency <ISO>] [--pretty]

Example:
  bom-builder lookup RC0402FR-0710KL --manufacturer Yageo \
    --quantity 100 --providers mouser,digikey --pretty

When the approved-resolutions database exists, a demand with an active
resolution is sourced as its approved replacement and the result carries
the approval's identity in parts[].resolution. --ignore-resolutions skips
the store for one run; a missing database is a silent no-op.

The "microchip" provider (explicit selection only) returns
credential-free factory availability and lifecycle EVIDENCE without
pricing; such results always remain review-required.
`,
	"price": `Usage:
  bom-builder price <design.json|-> [...] --units <n>
    [--attrition <0..1>] [--providers <auto|list>]
    [--deadline <duration>] [cache-options]
    [--resolutions-db <path>] [--ignore-resolutions]
    [--currency <ISO>] [--pretty]

Example:
  bom-builder price design.json --units 100 --attrition 0.02 \
    --providers auto --pretty

Demands with an active approved resolution are sourced as their approved
replacement (see ` + "`bom-builder help resolutions`" + `); each affected
part reports the approval's identity in parts[].resolution.

Without --currency, safe totals exist only when every selected plan shares
one currency (mixed currencies fail closed). With --currency <ISO>, totals
are converted using the ECB's dated daily reference quotes; the summary
then reports quote_source and quote_date, per-part plans keep their native
currency, and a plan the quotes cannot convert fails the run explicitly.
`,
	"documents": `Usage:
  bom-builder documents list <mpn> --manufacturer <name> [options]
  bom-builder documents fetch <https-url> --output <path> [options]

Run ` + "`bom-builder help documents list`" + ` or
` + "`bom-builder help documents fetch`" + ` for details.
`,
	"documents list": `Usage:
  bom-builder documents list <mpn> --manufacturer <name>
    [--quantity <n>] [--providers <auto|list>] [--deadline <duration>]
    [cache-options] [--pretty]

Example:
  bom-builder documents list RC0402FR-0710KL --manufacturer Yageo \
    --providers mouser,digikey --pretty
`,
	"documents fetch": `Usage:
  bom-builder documents fetch <https-url> --output <new-file>
    [--max-bytes <n>] [--deadline <duration>] [--pretty]

The target directory must exist. Existing files are never overwritten.

Example:
  bom-builder documents fetch https://example.com/part.pdf \
    --output ./part.pdf --pretty
`,
	"providers": `Usage:
  bom-builder providers list [--pretty]
  bom-builder providers check [--providers <all|list>]
    [--live] [--deadline <duration>] [--pretty]
`,
	"providers list": `Usage:
  bom-builder providers list [--pretty]
`,
	"providers check": `Usage:
  bom-builder providers check [--providers <all|list>]
    [--live] [--deadline <duration>] [--pretty]

Example:
  bom-builder providers check --providers mouser,digikey,ti,nxp \
    --live --deadline 90s --pretty
	`,
	"cache": `Usage:
  bom-builder cache status [--cache-db <path>] [--pretty]
  bom-builder cache list [--provider <name>] [--limit <1..1000>]
    [--include-stale] [--cache-db <path>] [--pretty]
  bom-builder cache verify [--cache-db <path>] [--pretty]
  bom-builder cache prune [--all] [--apply <preview-token>]
    [--cache-db <path>] [--pretty]

Sourcing cache options:
  --cache-policy <prefer|refresh|only|offline|off>
  --cache-db <path>
  --cache-ttl <duration>

` + "`prefer`" + ` uses fresh entries and refreshes misses; ` + "`refresh`" + `
always uses providers and replaces results; ` + "`only`" + ` accepts fresh
entries without initializing provider clients; ` + "`offline`" + ` also accepts
expired entries; and ` + "`off`" + ` bypasses SQLite.
	`,
	"cache status": `Usage:
  bom-builder cache status [--cache-db <path>] [--pretty]
	`,
	"cache list": `Usage:
  bom-builder cache list [--provider <mouser|digikey|ti|nxp>]
    [--limit <1..1000>] [--include-stale] [--cache-db <path>] [--pretty]

Cached provider payloads are never printed; this command returns safe identity,
expiry, status, adapter-version, and source-request metadata only.
	`,
	"cache verify": `Usage:
  bom-builder cache verify [--cache-db <path>] [--pretty]

Runs SQLite integrity checking and validates every cached JSON payload, checksum,
status, expiry, and demand-derived key.
	`,
	"cache prune": `Usage:
  bom-builder cache prune [--all] [--cache-db <path>] [--pretty]
  bom-builder cache prune [--all] --apply <preview-token>
    [--cache-db <path>] [--pretty]

Without ` + "`--apply`" + ` this is a read-only exact preview. The returned
token applies only while the selected entry set and payload hashes are unchanged.
The default scope is expired entries; ` + "`--all`" + ` selects every entry.
	`,
	"schema": `Usage:
  bom-builder schema <input|alternatives|cache|output|providers|resolutions>
    [--pretty]
	`,
	"resolutions": `Usage:
  bom-builder resolutions approve <request.json|->
    [--resolutions-db <path>] [--pretty]
  bom-builder resolutions list [--manufacturer <name>] [--part <mpn>]
    [--limit <1..1000>] [--include-inactive] [--resolutions-db <path>] [--pretty]
  bom-builder resolutions history [--manufacturer <name>] [--part <mpn>]
    [--limit <1..1000>] [--resolutions-db <path>] [--pretty]
  bom-builder resolutions revoke --id <resolution-id> --revoked-by <name>
    [--reason <text>] [--apply <preview-token>] [--resolutions-db <path>] [--pretty]

A resolution records that a named person cleared engineering review for one
replacement of one original part. BOM Builder never approves anything itself.
Approving a demand that already has an active resolution supersedes the old
one; every change lands in an append-only audit history.

The request schema is available with ` + "`bom-builder schema resolutions`" + `.
The database defaults to the per-user configuration directory; override with
--resolutions-db or the BOM_BUILDER_RESOLUTIONS_DB process environment
variable (refused from .env, like every trusted-path override).

Example:
  bom-builder resolutions approve approval.json --pretty
	`,
	"resolutions approve": `Usage:
  bom-builder resolutions approve <request.json|->
    [--resolutions-db <path>] [--pretty]

The strict JSON request names the original manufacturer/part, the approved
replacement (optionally pinned to one provider SKU), the approving person,
and optional https evidence documents with SHA-256 hashes.
	`,
	"resolutions list": `Usage:
  bom-builder resolutions list [--manufacturer <name>] [--part <mpn>]
    [--limit <1..1000>] [--include-inactive] [--resolutions-db <path>] [--pretty]

Filters match case-insensitively. Without --include-inactive only active
resolutions are returned.
	`,
	"resolutions history": `Usage:
  bom-builder resolutions history [--manufacturer <name>] [--part <mpn>]
    [--limit <1..1000>] [--resolutions-db <path>] [--pretty]

Returns the append-only audit trail (approved, superseded, revoked), newest
event first.
	`,
	"interactive": `Usage:
  bom-builder interactive [--resolutions-db <path>]

Full-screen terminal mode for managing approved resolutions: browse active
and inactive records, inspect details and audit history, approve new
resolutions, and revoke active ones. Every decision requires the acting
person's name, exactly like the JSON commands.

The resolver flow ("l") sources one part with the same provider, cache,
and selection semantics as the lookup command, lists every normalized
candidate with match method, stock, price, and review status, and seeds
the approve form with the chosen candidate. The approver still reviews
every field and types their own name: choosing a row never clears
engineering review by itself.

Keys: arrows/j/k move · enter detail · a approve · r revoke · h history ·
l resolve a part · i toggle inactive · esc back · ctrl+s submit · q quit

This is the one command that does not emit JSON on stdout: it renders a
human interface and refuses to start when stdin or stdout is not a
terminal, so scripts and agents always get a machine-readable error
instead of a hanging screen.
	`,
	"serve": `Usage:
  bom-builder serve [--listen 127.0.0.1:0] [--resolutions-db <path>]

Serves the local web interface for approved resolutions: browse records
and audit history, approve and revoke with the same person-named rules as
the JSON commands, and resolve parts through the same provider pipeline
as lookup. The frontend is embedded in the binary; nothing is fetched
from the network.

Local-only by design: the listener must be a loopback address (non-loopback
--listen values are refused), every request needs the per-session token
embedded in the printed URL, and DNS-rebinding and cross-origin browser
requests are rejected.

Stdout carries exactly one JSON startup document containing the URL; the
process then serves until Ctrl+C. Progress notes go to stderr. The web
API is an internal contract between the binary and its own frontend — the
public machine interface remains the CLI.

Example:
  bom-builder serve
  bom-builder serve --listen 127.0.0.1:8722 --pretty
	`,
	"resolutions revoke": `Usage:
  bom-builder resolutions revoke --id <resolution-id> --revoked-by <name>
    [--reason <text>] [--resolutions-db <path>] [--pretty]
  bom-builder resolutions revoke --id <resolution-id> --revoked-by <name>
    --apply <preview-token> [--reason <text>] [--resolutions-db <path>] [--pretty]

Without ` + "`--apply`" + ` this is a read-only exact preview. The returned
token applies only while the previewed record is unchanged; any concurrent
approval or revocation invalidates it. Revocation retires the resolution but
keeps its full audit history.
	`,
}
