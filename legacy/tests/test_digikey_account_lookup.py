"""Tests for the Digi-Key account lookup helper script."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

from scripts.digikey_account_lookup import write_or_update_env_value


class TestWriteOrUpdateEnvValue:
    def test_appends_missing_key(self, tmp_path):
        env_file = tmp_path / ".env"
        env_file.write_text("FOO=bar\n", encoding="utf-8")

        write_or_update_env_value(env_file, "DIGIKEY_ACCOUNT_ID", "12345")

        assert env_file.read_text(encoding="utf-8") == (
            "FOO=bar\n\nDIGIKEY_ACCOUNT_ID=12345\n"
        )

    def test_replaces_existing_key(self, tmp_path):
        env_file = tmp_path / ".env"
        env_file.write_text(
            "DIGIKEY_ACCOUNT_ID=1\nOTHER=value\n",
            encoding="utf-8",
        )

        write_or_update_env_value(env_file, "DIGIKEY_ACCOUNT_ID", "12345")

        assert env_file.read_text(encoding="utf-8") == (
            "DIGIKEY_ACCOUNT_ID=12345\nOTHER=value\n"
        )
