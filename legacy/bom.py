"""Design-file loading and BOM aggregation.

This module converts one or more raw design JSON files into a deduplicated list
of :class:`models.AggregatedPart` records. It is intentionally separated from
pricing so quantity scaling, attrition handling, and reference merging stay
deterministic and easy to test without any distributor dependencies.
"""

import json
import logging
import math
from dataclasses import dataclass, field
from pathlib import Path

from models import AggregatedPart, Design, Part

log = logging.getLogger(__name__)

type PartKey = tuple[str, str]


@dataclass
class _PartAccumulator:
    """Mutable helper used while folding duplicate parts together.

    The accumulator exists only during aggregation. It gathers the per-unit
    quantity, first-seen descriptive hints, and merged reference strings before
    emitting an immutable :class:`AggregatedPart`.
    """

    part_number: str
    manufacturer: str
    quantity_per_unit: int = 0
    references: list[str] = field(default_factory=list)
    description: str | None = None
    package: str | None = None
    pins: int | None = None

    def merge(self, design_name: str, part: Part) -> None:
        """Merge one design-local part into the accumulator.

        Parameters
        ----------
        design_name:
            Name of the design currently being aggregated. It is prefixed onto
            the stored reference string so multi-design builds retain provenance.
        part:
            Raw input part being folded into the aggregated record.
        """
        self.quantity_per_unit += part.quantity

        if part.reference:
            self.references.append(f"{design_name}: {part.reference}")
        if self.description is None and part.description:
            self.description = part.description
        if self.package is None and part.package:
            self.package = part.package
        if self.pins is None and part.pins is not None:
            self.pins = part.pins

    def to_aggregated(self, units: int, attrition: float) -> AggregatedPart:
        """Finalize this accumulator into an immutable aggregated record.

        Parameters
        ----------
        units:
            Number of finished assemblies the user wants to build.
        attrition:
            Fractional attrition factor, for example ``0.02`` for 2% extra
            material coverage.
        """
        total_quantity = math.ceil(self.quantity_per_unit * units * (1 + attrition))
        reference = "; ".join(self.references) if self.references else None
        return AggregatedPart(
            part_number=self.part_number,
            manufacturer=self.manufacturer,
            quantity_per_unit=self.quantity_per_unit,
            total_quantity=total_quantity,
            description=self.description,
            reference=reference,
            package=self.package,
            pins=self.pins,
        )


def _part_key(part: Part) -> PartKey:
    """Build the deduplication key for a raw part line."""
    return part.part_number, part.manufacturer


def load_design(path: Path) -> Design:
    """Load and validate one design JSON document from disk.

    Parameters
    ----------
    path:
        File path pointing at a design JSON document.

    Returns
    -------
    Design
        Parsed and validated design model.

    Raises
    ------
    SystemExit
        Raised with a user-facing message when the file cannot be read or does
        not contain valid JSON.
    """
    try:
        with path.open(encoding="utf-8") as f:
            data = json.load(f)
    except json.JSONDecodeError as e:
        raise SystemExit(f"Invalid JSON in {path}: {e}")
    except OSError as e:
        raise SystemExit(f"Cannot read {path}: {e}")
    return Design(**data)


def aggregate_parts(
    designs: list[Design], units: int, attrition: float = 0.0
) -> list[AggregatedPart]:
    """Aggregate parts across designs and scale them to build demand.

    Parameters
    ----------
    designs:
        Parsed design documents to merge.
    units:
        Requested production quantity.
    attrition:
        Optional extra material factor applied before rounding up to total
        required quantity.

    Returns
    -------
    list[AggregatedPart]
        Deduplicated parts ready for distributor resolution.
    """
    aggregated: dict[PartKey, _PartAccumulator] = {}

    for design in designs:
        for part in design.parts:
            key = _part_key(part)
            accumulator = aggregated.setdefault(
                key,
                _PartAccumulator(
                    part_number=part.part_number,
                    manufacturer=part.manufacturer,
                ),
            )
            accumulator.merge(design.design, part)

    results = [item.to_aggregated(units, attrition) for item in aggregated.values()]

    log.debug("Aggregated %d unique parts from %d designs", len(results), len(designs))
    return results
