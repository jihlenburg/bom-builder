"""Tests for logging configuration."""

import logging
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

from config import setup_logging


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
