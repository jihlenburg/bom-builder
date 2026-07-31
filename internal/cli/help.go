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
			if (target == "cache" || target == "documents" || target == "providers") &&
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
  providers list           Report safe provider configuration facts
  providers check          Run configuration or bounded live checks
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
    [cache-options] [--pretty]

Example:
  bom-builder lookup RC0402FR-0710KL --manufacturer Yageo \
    --quantity 100 --providers mouser,digikey --pretty

The "microchip" provider (explicit selection only) returns
credential-free factory availability and lifecycle EVIDENCE without
pricing; such results always remain review-required.
`,
	"price": `Usage:
  bom-builder price <design.json|-> [...] --units <n>
    [--attrition <0..1>] [--providers <auto|list>]
    [--deadline <duration>] [cache-options] [--pretty]

Example:
  bom-builder price design.json --units 100 --attrition 0.02 \
    --providers auto --pretty
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
  bom-builder schema <input|alternatives|cache|output|providers> [--pretty]
	`,
}
