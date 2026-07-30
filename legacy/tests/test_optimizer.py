"""Tests for the distributor-agnostic purchase optimizer."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

from optimizer import FamilyPriceBreak, PurchaseFamily, optimize_purchase_families


class TestOptimizePurchaseFamilies:
    def test_selects_cheapest_single_family_overbuy(self):
        families = (
            PurchaseFamily(
                family_id="cut_tape",
                packaging_mode="Cut Tape",
                base_pricing_strategy="requested quantity",
                strategy_mode="price_break",
                price_breaks=(
                    FamilyPriceBreak(quantity=1, unit_price=0.10, currency="EUR"),
                    FamilyPriceBreak(quantity=1000, unit_price=0.09, currency="EUR"),
                ),
            ),
        )

        plan = optimize_purchase_families(950, families)

        assert plan is not None
        assert plan.purchased_quantity == 1000
        assert plan.extended_price == 90.0
        assert plan.pricing_strategy == "next price break"

    def test_selects_mixed_bulk_plus_remainder_when_cheaper(self):
        families = (
            PurchaseFamily(
                family_id="cut_tape",
                packaging_mode="Cut Tape",
                base_pricing_strategy="requested quantity",
                strategy_mode="price_break",
                allow_mixing_as_remainder=True,
                price_breaks=(
                    FamilyPriceBreak(quantity=1, unit_price=1.00, currency="EUR"),
                    FamilyPriceBreak(quantity=500, unit_price=0.60, currency="EUR"),
                    FamilyPriceBreak(quantity=1000, unit_price=0.55, currency="EUR"),
                ),
            ),
            PurchaseFamily(
                family_id="full_reel",
                packaging_mode="Full Reel",
                full_reel_quantity=1800,
                minimum_order_quantity=1800,
                order_multiple=1800,
                base_pricing_strategy="full reel",
                strategy_mode="full_reel",
                allow_mixing_as_bulk=True,
                allow_mixing_as_remainder=False,
                mix_quantity=1800,
                price_breaks=(
                    FamilyPriceBreak(quantity=1800, unit_price=0.40, currency="EUR"),
                ),
            ),
        )

        plan = optimize_purchase_families(6000, families)

        assert plan is not None
        assert plan.purchased_quantity == 6000
        assert plan.extended_price == 2520.0
        assert plan.pricing_strategy == "mixed packaging"
        assert plan.order_plan == "3 reels x 1800 + 600 cut tape"
        assert len(plan.purchase_legs) == 2

    def test_prefers_reel_heavy_plan_when_cost_is_equal(self):
        families = (
            PurchaseFamily(
                family_id="cut_tape",
                packaging_mode="Cut Tape",
                base_pricing_strategy="requested quantity",
                strategy_mode="static",
                allow_mixing_as_remainder=True,
                price_breaks=(
                    FamilyPriceBreak(quantity=1, unit_price=0.80, currency="EUR"),
                ),
            ),
            PurchaseFamily(
                family_id="full_reel",
                packaging_mode="Full Reel",
                minimum_order_quantity=3000,
                order_multiple=3000,
                full_reel_quantity=3000,
                base_pricing_strategy="full reel",
                strategy_mode="full_reel",
                allow_mixing_as_bulk=True,
                allow_mixing_as_remainder=False,
                mix_quantity=3000,
                price_breaks=(
                    FamilyPriceBreak(quantity=1, unit_price=0.80, currency="EUR"),
                ),
            ),
        )

        plan = optimize_purchase_families(
            10000,
            families,
            manufacturing_preference_pct=0.0,
        )

        assert plan is not None
        assert plan.extended_price == 8000.0
        assert plan.order_plan == "3 reels x 3000 + 1000 cut tape"
        assert len(plan.purchase_legs) == 2

    def test_prefers_reel_heavy_plan_within_small_cost_delta(self):
        families = (
            PurchaseFamily(
                family_id="cut_tape",
                packaging_mode="Cut Tape",
                base_pricing_strategy="requested quantity",
                strategy_mode="static",
                allow_mixing_as_remainder=True,
                price_breaks=(
                    FamilyPriceBreak(quantity=1, unit_price=0.8000, currency="EUR"),
                ),
            ),
            PurchaseFamily(
                family_id="full_reel",
                packaging_mode="Full Reel",
                minimum_order_quantity=3000,
                order_multiple=3000,
                full_reel_quantity=3000,
                base_pricing_strategy="full reel",
                strategy_mode="full_reel",
                allow_mixing_as_bulk=True,
                allow_mixing_as_remainder=False,
                mix_quantity=3000,
                price_breaks=(
                    FamilyPriceBreak(quantity=1, unit_price=0.8030, currency="EUR"),
                ),
            ),
        )

        plan = optimize_purchase_families(
            10000,
            families,
            manufacturing_preference_pct=0.5,
        )

        assert plan is not None
        assert plan.extended_price == 8027.0
        assert plan.order_plan == "3 reels x 3000 + 1000 cut tape"
