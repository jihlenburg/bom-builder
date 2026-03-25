"""Distributor-agnostic purchase-plan optimization helpers.

Supplier adapters normalize their packaging and price-break information into
``PurchaseFamily`` records. This module then handles the actual buy-plan
optimization, including exact buys, overbuy via larger price breaks, and
mixed bulk-plus-remainder plans such as ``3 reels + 600 cut tape``.
"""

from __future__ import annotations

from dataclasses import dataclass
from math import ceil
import os
from typing import Iterable

from models import PurchaseLeg


@dataclass(frozen=True)
class FamilyPriceBreak:
    """One normalized price break inside a purchase family."""

    quantity: int
    unit_price: float
    currency: str


@dataclass(frozen=True)
class PurchaseFamily:
    """One normalized purchasable packaging family from a distributor."""

    family_id: str
    package_type: str | None = None
    packaging_mode: str | None = None
    minimum_order_quantity: int | None = None
    order_multiple: int | None = None
    full_reel_quantity: int | None = None
    base_pricing_strategy: str | None = None
    strategy_mode: str = "static"
    allow_mixing_as_bulk: bool = False
    allow_mixing_as_remainder: bool = True
    mix_quantity: int | None = None
    price_breaks: tuple[FamilyPriceBreak, ...] = ()


@dataclass(frozen=True)
class OptimizedPurchasePlan:
    """A fully-priced purchase plan, possibly composed from multiple legs."""

    required_quantity: int
    purchased_quantity: int
    unit_price: float
    extended_price: float
    currency: str
    price_break_quantity: int | None
    surplus_quantity: int
    pricing_strategy: str
    order_plan: str | None
    purchase_legs: tuple[PurchaseLeg, ...]


DEFAULT_MANUFACTURING_PREFERENCE_PCT = 0.5


def _round_up_to_multiple(quantity: int, multiple: int | None) -> int:
    """Round a quantity up to the next legal multiple when needed."""
    if multiple is None or multiple <= 1:
        return quantity
    return int(ceil(quantity / multiple) * multiple)


def _batch_noun(leg: PurchaseLeg) -> str:
    """Return the preferred noun for one purchase leg's packaging."""
    packaging_text = _packaging_text(leg).lower()
    if "reel" in packaging_text and "cut tape" not in packaging_text and "mousereel" not in packaging_text:
        return "reel"
    if "tray" in packaging_text:
        return "tray"
    if "tube" in packaging_text:
        return "tube"
    return "batch"


def _packaging_text(leg: PurchaseLeg) -> str:
    """Return one de-duplicated packaging label string for a purchase leg."""
    if leg.packaging_mode:
        return " ".join(str(leg.packaging_mode).split())

    texts: list[str] = []
    seen: set[str] = set()
    for text in [leg.package_type]:
        normalized = (text or "").strip()
        if not normalized:
            continue
        lowered = normalized.lower()
        if lowered in seen:
            continue
        texts.append(normalized)
        seen.add(lowered)
    return " ".join(texts)


def format_purchase_leg(leg: PurchaseLeg) -> str:
    """Return a short human-readable string for one concrete purchase leg."""
    if leg.order_batch_quantity and leg.order_batch_count:
        noun = _batch_noun(leg)
        plural = noun if leg.order_batch_count == 1 else _pluralize_batch_noun(noun)
        return f"{leg.order_batch_count} {plural} x {leg.order_batch_quantity}"

    packaging_text = _packaging_text(leg)
    if packaging_text:
        return f"{leg.purchased_quantity} {packaging_text.lower()}"
    return f"{leg.purchased_quantity}"


def format_order_plan(legs: Iterable[PurchaseLeg]) -> str:
    """Return the combined display string for one or more purchase legs."""
    rendered = [format_purchase_leg(leg) for leg in legs]
    return " + ".join(item for item in rendered if item)


def _pluralize_batch_noun(noun: str) -> str:
    """Return the plural form used in human-readable order plans."""
    if noun.endswith("ch"):
        return f"{noun}es"
    return f"{noun}s"


def resolve_manufacturing_preference_pct(value: float | None = None) -> float:
    """Return the allowed cost delta for plant-friendly plan preference."""
    if value is not None:
        return max(value, 0.0)

    raw = os.getenv("BOM_BUILDER_MANUFACTURING_PREFERENCE_PCT", "").strip()
    if not raw:
        return DEFAULT_MANUFACTURING_PREFERENCE_PCT
    try:
        return max(float(raw), 0.0)
    except ValueError:
        return DEFAULT_MANUFACTURING_PREFERENCE_PCT


def compose_purchase_plan(
    required_quantity: int,
    legs: Iterable[PurchaseLeg],
    pricing_strategy: str,
    *,
    price_break_quantity: int | None = None,
    order_plan: str | None = None,
) -> OptimizedPurchasePlan | None:
    """Aggregate one or more priced legs into a comparable purchase plan."""
    purchase_legs = tuple(legs)
    if not purchase_legs:
        return None

    currencies = {leg.currency for leg in purchase_legs if leg.currency}
    if len(currencies) != 1:
        return None

    purchased_quantity = sum(leg.purchased_quantity for leg in purchase_legs)
    extended_price = round(sum(leg.extended_price for leg in purchase_legs), 2)
    unit_price = round(extended_price / purchased_quantity, 6) if purchased_quantity else 0.0
    price_break = price_break_quantity
    if price_break is None and len(purchase_legs) == 1:
        price_break = purchase_legs[0].price_break_quantity

    return OptimizedPurchasePlan(
        required_quantity=required_quantity,
        purchased_quantity=purchased_quantity,
        unit_price=unit_price,
        extended_price=extended_price,
        currency=next(iter(currencies), ""),
        price_break_quantity=price_break,
        surplus_quantity=max(purchased_quantity - required_quantity, 0),
        pricing_strategy=pricing_strategy,
        order_plan=order_plan or format_order_plan(purchase_legs),
        purchase_legs=purchase_legs,
    )


def select_best_purchase_plan(
    plans: Iterable[OptimizedPurchasePlan],
    *,
    manufacturing_preference_pct: float | None = None,
) -> OptimizedPurchasePlan | None:
    """Return the preferred valid plan using cost and plant-friendly tiebreakers."""
    plan_list = list(plans)
    if not plan_list:
        return None
    cheapest_plan = min(
        plan_list,
        key=lambda plan: (
            plan.extended_price,
            plan.surplus_quantity,
            plan.purchased_quantity,
            len(plan.purchase_legs),
            float("inf") if plan.price_break_quantity is None else plan.price_break_quantity,
        ),
    )
    tolerance_pct = resolve_manufacturing_preference_pct(manufacturing_preference_pct)
    tolerance_multiplier = 1 + (tolerance_pct / 100.0)
    preferred_candidates = [
        plan
        for plan in plan_list
        if plan.extended_price <= (cheapest_plan.extended_price * tolerance_multiplier) + 1e-9
    ]
    return min(preferred_candidates, key=_manufacturing_preference_key)


def _manufacturing_preference_key(plan: OptimizedPurchasePlan) -> tuple[float, ...]:
    """Return the stable preference key for plant-friendly purchase plans."""
    reel_quantity = 0
    stable_pack_quantity = 0
    cut_tape_quantity = 0
    for leg in plan.purchase_legs:
        packaging_kind = _packaging_kind(leg)
        if packaging_kind == "reel":
            reel_quantity += leg.purchased_quantity
            stable_pack_quantity += leg.purchased_quantity
        elif packaging_kind == "stable_pack":
            stable_pack_quantity += leg.purchased_quantity
        elif packaging_kind == "cut_tape":
            cut_tape_quantity += leg.purchased_quantity

    return (
        -reel_quantity,
        -stable_pack_quantity,
        cut_tape_quantity,
        plan.extended_price,
        plan.surplus_quantity,
        len(plan.purchase_legs),
        plan.purchased_quantity,
        float("inf") if plan.price_break_quantity is None else plan.price_break_quantity,
    )


def _packaging_kind(leg: PurchaseLeg) -> str:
    """Return a coarse manufacturing-friendly packaging class for one leg."""
    text = _packaging_text(leg).lower()
    if not text:
        return "unknown"
    if "cut tape" in text:
        return "cut_tape"
    if "mousereel" in text or "mouse reel" in text:
        return "stable_pack"
    if any(token in text for token in ("full reel", "reel", "t&r", "tape & reel", "tape and reel")):
        return "reel"
    if any(token in text for token in ("tray", "tube", "bulk")):
        return "stable_pack"
    return "unknown"


def _family_strategy(
    family: PurchaseFamily,
    *,
    required_quantity: int,
    purchased_quantity: int,
    break_quantity: int,
) -> str:
    """Return the strategy label for one family-specific buy leg."""
    if family.strategy_mode == "static":
        return family.base_pricing_strategy or ""
    if family.strategy_mode == "full_reel":
        return "full reel"
    if family.strategy_mode == "price_break":
        if purchased_quantity > required_quantity:
            if family.order_multiple and purchased_quantity % family.order_multiple == 0:
                return "order multiple" if break_quantity <= required_quantity else "next price break"
            return "next price break"
        return family.base_pricing_strategy or "requested quantity"
    return family.base_pricing_strategy or ""


def purchase_leg_from_family(
    family: PurchaseFamily,
    quantity: int,
) -> PurchaseLeg | None:
    """Return the cheapest legal single-family leg for one quantity target."""
    if quantity <= 0:
        return None

    legs: list[PurchaseLeg] = []
    for price_break in family.price_breaks:
        break_quantity = int(price_break.quantity)
        if break_quantity <= 0:
            continue

        base_quantity = max(quantity, break_quantity, family.minimum_order_quantity or 0)
        rounding_multiple = family.full_reel_quantity or family.order_multiple
        purchased_quantity = _round_up_to_multiple(base_quantity, rounding_multiple)
        purchased_quantity = max(purchased_quantity, base_quantity)

        order_batch_quantity = next(
            (
                candidate
                for candidate in [
                    family.full_reel_quantity,
                    family.order_multiple,
                    family.minimum_order_quantity,
                ]
                if candidate is not None and candidate > 1 and purchased_quantity % candidate == 0
            ),
            None,
        )
        order_batch_count = (
            purchased_quantity // order_batch_quantity
            if order_batch_quantity is not None
            else None
        )
        legs.append(
            PurchaseLeg(
                purchased_quantity=purchased_quantity,
                unit_price=price_break.unit_price,
                extended_price=round(price_break.unit_price * purchased_quantity, 2),
                currency=price_break.currency,
                price_break_quantity=break_quantity,
                pricing_strategy=_family_strategy(
                    family,
                    required_quantity=quantity,
                    purchased_quantity=purchased_quantity,
                    break_quantity=break_quantity,
                ),
                package_type=family.package_type,
                packaging_mode=family.packaging_mode,
                order_batch_quantity=order_batch_quantity,
                order_batch_count=order_batch_count,
            )
        )

    if not legs:
        return None

    return min(
        legs,
        key=lambda leg: (
            leg.extended_price,
            leg.purchased_quantity,
            float("inf") if leg.price_break_quantity is None else leg.price_break_quantity,
        ),
    )


def purchase_plan_from_family(
    family: PurchaseFamily,
    quantity: int,
) -> OptimizedPurchasePlan | None:
    """Return the cheapest legal single-family plan for one quantity target."""
    leg = purchase_leg_from_family(family, quantity)
    if leg is None:
        return None
    return compose_purchase_plan(
        quantity,
        [leg],
        leg.pricing_strategy or family.base_pricing_strategy or "",
    )


def _candidate_bulk_quantities(
    required_quantity: int,
    family: PurchaseFamily,
) -> tuple[int, ...]:
    """Return the candidate bulk quantities worth testing for mixed plans."""
    mix_quantity = family.mix_quantity or family.full_reel_quantity or family.order_multiple
    if mix_quantity is None or mix_quantity <= 1:
        return ()

    max_break_quantity = max((price_break.quantity for price_break in family.price_breaks), default=0)
    max_quantity = max(required_quantity, max_break_quantity)
    max_count = max(1, ceil(max_quantity / mix_quantity))
    return tuple(count * mix_quantity for count in range(1, max_count + 1))


def optimize_purchase_families(
    required_quantity: int,
    families: Iterable[PurchaseFamily],
    *,
    mixed_strategy: str = "mixed packaging",
    manufacturing_preference_pct: float | None = None,
) -> OptimizedPurchasePlan | None:
    """Return the best plan across all single-family and mixed-family options."""
    family_list = [family for family in families if family.price_breaks]
    plans: list[OptimizedPurchasePlan] = []

    for family in family_list:
        plan = purchase_plan_from_family(family, required_quantity)
        if plan is not None:
            plans.append(plan)

    bulk_families = [family for family in family_list if family.allow_mixing_as_bulk]
    remainder_families = [family for family in family_list if family.allow_mixing_as_remainder]

    for bulk_family in bulk_families:
        for bulk_quantity in _candidate_bulk_quantities(required_quantity, bulk_family):
            bulk_leg = purchase_leg_from_family(bulk_family, bulk_quantity)
            if bulk_leg is None:
                continue

            remainder_quantity = max(required_quantity - bulk_leg.purchased_quantity, 0)
            if remainder_quantity <= 0:
                continue

            for remainder_family in remainder_families:
                if remainder_family.family_id == bulk_family.family_id:
                    continue
                remainder_leg = purchase_leg_from_family(remainder_family, remainder_quantity)
                if remainder_leg is None:
                    continue
                plan = compose_purchase_plan(
                    required_quantity,
                    [bulk_leg, remainder_leg],
                    mixed_strategy,
                )
                if plan is not None:
                    plans.append(plan)

    return select_best_purchase_plan(
        plans,
        manufacturing_preference_pct=manufacturing_preference_pct,
    )
