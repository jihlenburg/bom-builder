#!/usr/bin/env python3
"""Small Digi-Key Product Information V4 probe for local verification.

The goal of this helper is operational confidence, not end-user BOM output. It
lets a developer verify that:

- Digi-Key 2-legged OAuth works with the configured credentials
- the locale headers are producing the expected market/currency behavior
- the quantity-pricing endpoint returns usable data for a sample product
- the account/customer header fallback strategy is behaving as expected
"""

from __future__ import annotations

import argparse
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from digikey import DigiKeyClient, best_pricing_option, resolve_digikey_locale


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    """Parse CLI arguments for the Digi-Key probe helper."""
    parser = argparse.ArgumentParser(
        description="Probe Digi-Key Product Information V4 pricing for one product number."
    )
    parser.add_argument(
        "--product-number",
        default="P5555-ND",
        help="Digi-Key product number to query (default: P5555-ND)",
    )
    parser.add_argument(
        "--quantity",
        type=int,
        default=100,
        help="Requested quantity for pricing-by-quantity (default: 100)",
    )
    parser.add_argument("--site", default="", help="Override DIGIKEY_LOCALE_SITE")
    parser.add_argument(
        "--language",
        default="",
        help="Override DIGIKEY_LOCALE_LANGUAGE",
    )
    parser.add_argument(
        "--currency",
        default="",
        help="Override DIGIKEY_LOCALE_CURRENCY",
    )
    parser.add_argument(
        "--ship-to-country",
        default="",
        help="Override DIGIKEY_LOCALE_SHIP_TO_COUNTRY",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    """Run one Digi-Key pricing probe and print the normalized result."""
    args = parse_args(argv)
    locale = resolve_digikey_locale(
        site=args.site,
        language=args.language,
        currency=args.currency,
        ship_to_country=args.ship_to_country,
    )

    with DigiKeyClient(locale=locale) as client:
        result = client.pricing_by_quantity(args.product_number, args.quantity)

    print("Digi-Key Product Information V4 Probe")
    print("=" * 37)
    print(f"Requested product:    {result.requested_product}")
    print(f"Requested quantity:   {result.requested_quantity}")
    print(f"Manufacturer:         {result.manufacturer_name or '—'}")
    print(f"Manufacturer PN:      {result.manufacturer_part_number or '—'}")
    print(f"Currency:             {result.currency}")
    print(
        "Locale used:          "
        f"{locale.site}/{locale.language}/{locale.currency}/{locale.ship_to_country}"
    )
    print(f"Header mode used:     {result.header_mode_used}")
    print(f"Customer ID used:     {result.customer_id_used if result.customer_id_used is not None else '—'}")
    print(
        "Rate limit remaining: "
        f"{result.rate_limit_remaining if result.rate_limit_remaining is not None else '—'}"
    )
    print()

    best = best_pricing_option(result)
    if best is None:
        print("No pricing options returned.")
        return 1

    print("Best pricing option:")
    print(f"  Type:               {best.pricing_option}")
    print(f"  Total quantity:     {best.total_quantity_priced}")
    print(f"  Total price:        {best.total_price:.4f} {result.currency}")
    print(f"  Effective unit:     {best.effective_unit_price:.6f} {result.currency}")
    print(f"  Quantity available: {best.quantity_available if best.quantity_available is not None else '—'}")
    print()

    for index, product in enumerate(best.products, 1):
        print(f"Product {index}:")
        print(f"  Digi-Key PN:        {product.digikey_product_number}")
        print(f"  Quantity priced:    {product.quantity_priced}")
        print(f"  MOQ:                {product.minimum_order_quantity}")
        print(f"  Unit price:         {product.unit_price:.6f} {result.currency}")
        print(f"  Extended price:     {product.extended_price:.4f} {result.currency}")
        print(f"  Package:            {product.package_type or '—'}")
        print()

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
