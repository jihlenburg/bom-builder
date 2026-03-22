"""Core typed data structures used throughout BOM Builder.

The project keeps nearly all non-trivial data flow inside Pydantic models so
validation happens at the module boundaries instead of deep inside pricing or
report-generation logic. These models represent three layers of state:

- raw design input loaded from JSON files
- aggregated part demand after design merging and unit scaling
- priced distributor-facing records plus final BOM summary statistics

Keeping those layers explicit makes it much easier to reason about which fields
are expected to exist at each phase of the pipeline and where enrichment such
as Mouser part numbers, pricing, or package inference may be added.
"""

from enum import Enum

from pydantic import BaseModel, Field


class MatchMethod(str, Enum):
    """Enumeration of the resolver strategies used to find a distributor part.

    The enum values are intentionally stable because they are persisted into
    JSON output, report files, and summary views. Human-facing formatting is
    exposed through :attr:`display_name` so the raw enum values can stay short
    and machine-friendly.
    """

    EXACT = "exact"
    BEGINS_WITH = "begins_with"
    FUZZY = "fuzzy"
    NOT_FOUND = "not_found"

    @property
    def display_name(self) -> str:
        """Return the console/report label associated with this match method."""
        return {
            MatchMethod.EXACT: "Exact match",
            MatchMethod.BEGINS_WITH: "BeginsWith match",
            MatchMethod.FUZZY: "Fuzzy match (review!)",
            MatchMethod.NOT_FOUND: "Not found",
        }[self]


class Part(BaseModel):
    """One BOM line as authored inside an input design file.

    A :class:`Part` represents the authoring-time view of a component before
    cross-design aggregation or distributor enrichment has happened. Optional
    fields such as ``description``, ``package``, and ``pins`` act as resolver
    hints rather than hard constraints, because real design files are often
    incomplete.
    """

    part_number: str = Field(description="Manufacturer part number")
    manufacturer: str = Field(description="Part manufacturer")
    quantity: int = Field(ge=1, description="Quantity per single unit")
    reference: str | None = Field(
        default=None, description="Reference designators, e.g. 'R1,R2,R3'"
    )
    description: str | None = Field(
        default=None, description="Human-readable part description"
    )
    package: str | None = Field(
        default=None, description="Package type, e.g. '0402', 'SOT-23', 'LQFP-64'"
    )
    pins: int | None = Field(
        default=None, ge=1, description="Number of pins"
    )


class Design(BaseModel):
    """A single design document loaded from JSON input.

    Each design contains a display name, an optional revision string, and a
    flat list of :class:`Part` entries. Multi-design builds simply merge
    several :class:`Design` instances before pricing.
    """

    design: str = Field(description="Design name/identifier")
    version: str | None = Field(default=None, description="Design revision")
    parts: list[Part] = Field(description="Parts list")


class AggregatedPart(BaseModel):
    """A deduplicated part requirement after design merging and scaling.

    ``AggregatedPart`` is the handoff object between BOM aggregation and price
    resolution. It carries the normalized quantity demand for the requested
    build volume, while still preserving optional hints such as package and pin
    count for later resolver stages.
    """

    part_number: str
    manufacturer: str
    quantity_per_unit: int
    total_quantity: int
    description: str | None = None
    reference: str | None = None
    package: str | None = None
    pins: int | None = None


class PricedPart(BaseModel):
    """An aggregated part enriched with distributor lookup results.

    Instances of this model are produced by the Mouser resolver. They may be
    fully priced, partially resolved with warnings, or completely unresolved.
    Output writers and the console summary operate on this model directly so
    they can show both successful pricing data and resolver diagnostics.
    """

    part_number: str
    manufacturer: str
    quantity_per_unit: int
    total_quantity: int
    description: str | None = None
    reference: str | None = None
    package: str | None = None
    pins: int | None = None
    mouser_part_number: str | None = None
    unit_price: float | None = Field(default=None, ge=0)
    extended_price: float | None = Field(default=None, ge=0)
    currency: str | None = None
    availability: str | None = None
    price_break_quantity: int | None = None
    match_method: MatchMethod | None = None
    match_candidates: int | None = None
    resolution_source: str | None = None
    review_required: bool = False
    lookup_error: str | None = None

    @classmethod
    def from_aggregated(cls, agg: AggregatedPart) -> "PricedPart":
        """Create a pricing record seeded from an aggregated BOM line.

        Parameters
        ----------
        agg:
            The aggregated input part to copy into a mutable pricing-oriented
            record.

        Returns
        -------
        PricedPart
            A new instance containing the aggregation fields and no pricing
            metadata yet.
        """
        return cls(**agg.model_dump())

    @property
    def is_priced(self) -> bool:
        """Return True when this part has a resolved extended price."""
        return self.extended_price is not None

    @property
    def has_lookup_error(self) -> bool:
        """Return True when lookup or pricing produced an error/warning."""
        return bool(self.lookup_error)


class BomSummary(BaseModel):
    """Computed summary stats for a priced BOM.

    This model is computed once via from_parts() and then shared across
    all report writers and the console summary output, avoiding duplicate
    cost/error calculations.
    """

    units: int
    total_parts: int
    total_components_per_unit: int
    total_cost: float
    cost_per_unit: float
    currency: str
    error_count: int
    priced_count: int

    @classmethod
    def from_parts(cls, parts: list[PricedPart], units: int) -> "BomSummary":
        """Compute summary statistics for a fully processed BOM.

        Parameters
        ----------
        parts:
            Final priced or partially priced part records.
        units:
            Requested production quantity for the current run.

        Returns
        -------
        BomSummary
            The shared summary object consumed by report writers and the
            console summary printer.
        """
        priced = [p for p in parts if p.is_priced]
        total_cost = sum(p.extended_price for p in priced)
        currency = next((p.currency for p in priced if p.currency), "")
        return cls(
            units=units,
            total_parts=len(parts),
            total_components_per_unit=sum(p.quantity_per_unit for p in parts),
            total_cost=round(total_cost, 2),
            cost_per_unit=round(total_cost / units, 2) if units > 0 else 0,
            currency=currency,
            error_count=sum(1 for p in parts if p.has_lookup_error),
            priced_count=len(priced),
        )
