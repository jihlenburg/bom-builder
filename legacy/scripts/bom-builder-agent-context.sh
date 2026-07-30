#!/bin/sh
#
# Emit BOM Builder's complete public automation contract as one JSON document.
#
# BOM_BUILDER_BIN may point at an installed command or a standalone binary.
# Without an override, this helper prefers the repository's standalone build
# and then falls back to an installed `bom-builder` command.

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "$script_dir/.." && pwd)

if [ -n "${BOM_BUILDER_BIN:-}" ]; then
    cli=$BOM_BUILDER_BIN
elif [ -x "$project_dir/dist/bom-builder" ]; then
    cli=$project_dir/dist/bom-builder
elif command -v bom-builder >/dev/null 2>&1; then
    cli=$(command -v bom-builder)
else
    printf '%s\n' \
        "bom-builder was not found; build it with scripts/build-standalone.sh" \
        >&2
    exit 127
fi

exec "$cli" capabilities --full
