#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "$script_dir/.." && pwd)
version=${BOM_BUILDER_VERSION:-3.0.0}
output=${BOM_BUILDER_OUTPUT:-"$project_dir/bin/bom-builder"}
go_toolchain=${BOM_BUILDER_GO_TOOLCHAIN:-go1.25.12}

mkdir -p "$(dirname -- "$output")"

CGO_ENABLED=0 GOTOOLCHAIN="$go_toolchain" go build \
    -trimpath \
    -ldflags "-s -w -X github.com/jihlenburg/bom-builder/internal/app.Version=$version" \
    -o "$output" \
    "$project_dir/cmd/bom-builder"

"$output" --version
printf 'Native executable: %s\n' "$output"

if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$output"
elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$output"
fi
