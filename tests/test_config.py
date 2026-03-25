"""Tests for logging configuration."""

import logging
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

from config import install_console_trace, resolve_trace_path, setup_logging


class TestSetupLogging:
    def test_verbose_logs_go_to_stdout(self, capsys):
        setup_logging(verbose=True)

        logging.getLogger("tests.verbose").debug("debug trace line")
        captured = capsys.readouterr()

        assert "debug trace line" in captured.out
        assert captured.err == ""

    def test_non_verbose_logs_stay_on_stderr(self, capsys):
        setup_logging(verbose=False)

        logging.getLogger("tests.default").warning("warning line")
        captured = capsys.readouterr()

        assert "warning line" in captured.err
        assert captured.out == ""

    def test_trace_console_mirrors_stdout_stderr_and_logs(self, capsys, tmp_path):
        trace_path = tmp_path / "runs" / "bom.log"

        with install_console_trace(trace_path):
            setup_logging(verbose=False)
            print("stdout line")
            print("stderr line", file=sys.stderr)
            logging.getLogger("tests.trace").warning("warning line")

        captured = capsys.readouterr()
        trace_text = trace_path.read_text(encoding="utf-8")

        assert "stdout line" in captured.out
        assert "stderr line" in captured.err
        assert "warning line" in captured.err
        assert "stdout line" in trace_text
        assert "stderr line" in trace_text
        assert "warning line" in trace_text

    def test_resolve_trace_path_uses_trace_dir_env(self, monkeypatch, tmp_path):
        monkeypatch.delenv("BOM_BUILDER_TRACE_FILE", raising=False)
        monkeypatch.setenv("BOM_BUILDER_TRACE_DIR", str(tmp_path))

        trace_path = resolve_trace_path()

        assert trace_path is not None
        assert trace_path.parent == tmp_path
        assert trace_path.name.startswith("bom-builder-")
        assert trace_path.suffix == ".log"
