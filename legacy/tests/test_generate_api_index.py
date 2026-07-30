"""Tests for the generated Sphinx API index helper."""

from __future__ import annotations

import importlib.util
from pathlib import Path


MODULE_PATH = Path(__file__).resolve().parents[1] / "docs" / "generate_api_index.py"
SPEC = importlib.util.spec_from_file_location("generate_api_index", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
generate_api_index = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(generate_api_index)


def test_discover_module_names_filters_non_runtime_files(tmp_path: Path) -> None:
    """Only top-level runtime modules should be included in the autosummary list."""

    for name in [
        "main.py",
        "bom.py",
        "_private.py",
        "test_helper.py",
        "conftest.py",
        "README.md",
    ]:
        (tmp_path / name).write_text("", encoding="utf-8")

    assert generate_api_index.discover_module_names(tmp_path) == ["bom", "main"]


def test_build_index_content_renders_autosummary_list() -> None:
    """The generated index should point autosummary at the generated subpages."""

    content = generate_api_index.build_index_content(["bom", "main"])

    assert ".. autosummary::" in content
    assert "   :toctree: generated" in content
    assert "   bom" in content
    assert "   main" in content


def test_write_index_reports_when_contents_change(tmp_path: Path) -> None:
    """The writer should avoid rewriting identical generated content."""

    output_path = tmp_path / "index.rst"
    content = "generated\n"

    assert generate_api_index.write_index(output_path, content) is True
    assert generate_api_index.write_index(output_path, content) is False
