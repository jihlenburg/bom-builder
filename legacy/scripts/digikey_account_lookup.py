#!/usr/bin/env python3
"""One-time Digi-Key account lookup helper for local setup.

The Product Information V4 APIs increasingly expect ``X-DIGIKEY-Account-ID``
for account-aware pricing and product details, but Digi-Key's official way to
discover that value is a separate ``AssociatedAccounts`` reference API that
uses 3-legged OAuth. This script guides a developer through that one-time flow:

1. generate the Digi-Key browser authorization URL
2. prompt the user to paste the final redirected URL or raw authorization code
3. exchange the code for tokens
4. call ``AssociatedAccounts``
5. optionally write the selected ``DIGIKEY_ACCOUNT_ID`` into ``.env``

The script is intentionally interactive and local-developer-oriented. It is not
part of the normal BOM pricing pipeline.
"""

from __future__ import annotations

import argparse
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from digikey_auth import (
    build_authorization_url,
    exchange_authorization_code,
    extract_authorization_code,
    fetch_associated_accounts,
    generate_oauth_state,
    resolve_digikey_client_credentials,
)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    """Parse command-line arguments for the Digi-Key account lookup helper."""
    parser = argparse.ArgumentParser(
        description="Look up Digi-Key Account IDs using a one-time 3-legged OAuth flow."
    )
    parser.add_argument(
        "--client-id",
        default="",
        help="Optional explicit Digi-Key client ID override",
    )
    parser.add_argument(
        "--client-secret",
        default="",
        help="Optional explicit Digi-Key client secret override",
    )
    parser.add_argument(
        "--redirect-uri",
        default="https://localhost",
        help=(
            "Redirect URI registered in the Digi-Key portal. "
            "It must match exactly, including any trailing slash."
        ),
    )
    parser.add_argument(
        "--code",
        default="",
        help="Optional raw authorization code or full redirected callback URL",
    )
    parser.add_argument(
        "--state",
        default="",
        help="Optional explicit OAuth state token for debugging",
    )
    parser.add_argument(
        "--print-only",
        action="store_true",
        help="Print the authorization URL and exit without continuing the flow",
    )
    parser.add_argument(
        "--write-env",
        action="store_true",
        help="Write the selected DIGIKEY_ACCOUNT_ID value into the local .env file",
    )
    parser.add_argument(
        "--env-file",
        type=Path,
        default=Path(".env"),
        help="Environment file to update when --write-env is used (default: .env)",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    """Run the interactive Digi-Key account lookup helper."""
    args = parse_args(argv)
    try:
        client_id, client_secret = resolve_digikey_client_credentials(
            args.client_id,
            args.client_secret,
        )
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 2

    state = args.state.strip() or generate_oauth_state()
    auth_url = build_authorization_url(client_id, args.redirect_uri, state)

    print("Digi-Key Account Lookup")
    print("=" * 24)
    print()
    print("1. Ensure your Digi-Key app registration uses this exact redirect URI:")
    print(f"   {args.redirect_uri}")
    print()
    print("2. Open this URL in your browser and complete the Digi-Key login/consent flow:")
    print(auth_url)
    print()
    print(
        "3. After Digi-Key redirects to localhost, copy the full URL from the browser "
        "address bar. If localhost is not running, a browser error page is expected."
    )
    print()

    if args.print_only:
        return 0

    callback_input = args.code.strip()
    if not callback_input:
        try:
            callback_input = input(
                "Paste the redirected URL or raw authorization code here: "
            ).strip()
        except (EOFError, KeyboardInterrupt):
            print("\nAborted.")
            return 130

    try:
        code, returned_state = extract_authorization_code(callback_input)
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 2

    if returned_state and returned_state != state:
        print(
            "Error: OAuth state mismatch. Refusing to continue because the returned "
            "state did not match the generated value.",
            file=sys.stderr,
        )
        return 2

    try:
        tokens = exchange_authorization_code(
            client_id,
            client_secret,
            code,
            args.redirect_uri,
        )
        accounts, email_used = fetch_associated_accounts(tokens.access_token, client_id)
    except Exception as e:
        print(f"Error: Digi-Key account lookup failed: {e}", file=sys.stderr)
        return 1

    print()
    if email_used:
        print(f"Authenticated Digi-Key user: {email_used}")
        print()

    if not accounts:
        print("No associated Digi-Key account IDs were returned.")
        return 1

    print("Associated Digi-Key Account IDs:")
    for index, account in enumerate(accounts, 1):
        details = [
            f"AccountId={account.account_id}",
            account.company_name or "Unknown company",
        ]
        location = ", ".join(
            part for part in (account.city, account.country_code, account.postal_code) if part
        )
        if location:
            details.append(location)
        print(f"  {index}. " + " | ".join(details))
    print()

    selected = accounts[0]
    if len(accounts) > 1:
        try:
            selection = input(
                f"Select which account to use in .env [1-{len(accounts)}] (default 1): "
            ).strip()
        except (EOFError, KeyboardInterrupt):
            print("\nAborted.")
            return 130
        if selection:
            try:
                selected = accounts[int(selection) - 1]
            except (ValueError, IndexError):
                print("Error: invalid selection.", file=sys.stderr)
                return 2

    print(f"Suggested .env line: DIGIKEY_ACCOUNT_ID={selected.account_id}")
    if args.write_env:
        write_or_update_env_value(args.env_file, "DIGIKEY_ACCOUNT_ID", str(selected.account_id))
        print(f"Wrote DIGIKEY_ACCOUNT_ID to {args.env_file}")

    return 0


def write_or_update_env_value(path: Path, key: str, value: str) -> None:
    """Write or replace one ``KEY=value`` entry in an environment file."""
    if path.exists():
        lines = path.read_text(encoding="utf-8").splitlines()
    else:
        lines = []

    updated = False
    new_lines: list[str] = []
    prefix = f"{key}="
    for line in lines:
        if line.startswith(prefix):
            new_lines.append(f"{prefix}{value}")
            updated = True
        else:
            new_lines.append(line)

    if not updated:
        if new_lines and new_lines[-1].strip():
            new_lines.append("")
        new_lines.append(f"{prefix}{value}")

    path.write_text("\n".join(new_lines) + "\n", encoding="utf-8")


if __name__ == "__main__":
    raise SystemExit(main())
