#!/bin/sh
#
# Build one self-contained BOM Builder executable for the host OS/architecture.
#
# PyInstaller does not cross-compile. Run this script once on each target
# platform (macOS, Linux, or a POSIX shell on Windows) that needs a binary.

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "$script_dir/.." && pwd)
python_bin=${PYTHON_BIN:-python3}
dist_dir=${BOM_BUILDER_DIST_DIR:-"$project_dir/dist"}
work_dir=${BOM_BUILDER_BUILD_DIR:-"$project_dir/build/pyinstaller"}

if ! "$python_bin" -c "import PyInstaller" >/dev/null 2>&1; then
    printf '%s\n' \
        "PyInstaller is required. Install it with:" \
        "  $python_bin -m pip install -e '${project_dir}[standalone]'" \
        >&2
    exit 2
fi

mkdir -p "$dist_dir" "$work_dir"

"$python_bin" -m PyInstaller \
    --clean \
    --noconfirm \
    --onefile \
    --name bom-builder \
    --distpath "$dist_dir" \
    --workpath "$work_dir/work" \
    --specpath "$work_dir" \
    --add-data "$project_dir/manufacturers.yaml:." \
    --add-data "$project_dir/packages.yaml:." \
    --collect-all playwright \
    "$project_dir/bom_builder_cli.py"

artifact=$dist_dir/bom-builder
if [ ! -x "$artifact" ]; then
    printf 'Build completed without the expected executable: %s\n' "$artifact" >&2
    exit 1
fi

"$artifact" --version
"$artifact" capabilities >/dev/null
providers_json=$("$artifact" providers list)
printf '%s' "$providers_json" | "$python_bin" -c \
    "import json, sys; payload=json.load(sys.stdin); assert next(item for item in payload['providers'] if item['name'] == 'nxp')['configured']"
printf 'Standalone executable: %s\n' "$artifact"

if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$artifact"
elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$artifact"
fi
