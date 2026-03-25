"""Sphinx configuration for the BOM Builder documentation site."""

from __future__ import annotations

from datetime import datetime
from pathlib import Path
import sys


DOCS_DIR = Path(__file__).resolve().parent
PROJECT_ROOT = DOCS_DIR.parent
sys.path.insert(0, str(PROJECT_ROOT))

project = "BOM Builder"
author = "BOM Builder contributors"
copyright = f"{datetime.now():%Y}, {author}"

extensions = [
    "myst_parser",
    "sphinx.ext.autodoc",
    "sphinx.ext.autosummary",
    "sphinx.ext.napoleon",
    "sphinx.ext.viewcode",
]

templates_path = ["_templates"]
exclude_patterns = ["_build", "Thumbs.db", ".DS_Store"]

source_suffix = {
    ".rst": "restructuredtext",
    ".md": "markdown",
}

autosummary_generate = True
autosummary_imported_members = False
autodoc_member_order = "bysource"
autodoc_typehints = "description"
autodoc_typehints_format = "short"

napoleon_google_docstring = False
napoleon_numpy_docstring = True
napoleon_include_init_with_doc = True
napoleon_attr_annotations = True

myst_enable_extensions = ["colon_fence"]

html_theme = "alabaster"
html_title = "BOM Builder Documentation"
