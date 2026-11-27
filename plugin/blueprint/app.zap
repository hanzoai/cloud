# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package blueprint

struct blueprintHealth {
    Blueprints i64   @0
    RateCard   bytes @8
    Service    text  @16
    Status     text  @24
}

struct blueprintIndex {
    Data list<bytes> @0
}

interface blueprint {
    # Returns every deployable blueprint with its service count and estimated
    # monthly compute cost.
    # It is the lightweight index the console renders as a template gallery before
    # drilling into one stack's bill of images — GET /v1/blueprint/sbom?template=<id>
    # is the detail view. The cost is the same figure the deploy path meters the
    # deploying org on and the 20% author royalty is taken from, priced from the
    # active rate card (GET /v1/blueprint/health echoes that card).
    get_blueprint() returns (rep: blueprintIndex)
    # Reports blueprint liveness and echoes the compute rate card in force.
    # The rate card is the one the estimator actually applies after the operator env
    # overlay, so an operator can confirm a tuned knob took effect rather than
    # inferring it from a price. Not JWT-gated — a liveness probe must be reachable —
    # and it always answers 200 while the subsystem is mounted.
    get_blueprint_health() returns (rep: blueprintHealth)
}

# ---------------------------------------------------------------------
# 2 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   blueprintHealth.RateCard  blueprint.RateCard
#   blueprintIndex.Data  blueprint.blueprintRow (list element)
