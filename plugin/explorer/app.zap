# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package explorer

struct indexersOut {
    Indexers list<bytes> @0
}

struct oraclesOut {
    Oracles list<bytes> @0
}

interface explorer {
    # Reports the deployment's chain indexer(s) and how far each has
    # indexed. Identity and health come from the indexer's /health; the latest indexed
    # block (height + time) from its /v1/explorer/blocks. The row EXISTS if EITHER call
    # reaches the indexer; when the indexer is entirely unreachable the answer degrades
    # to an honest-EMPTY list at 200, not a 502. No chain HEAD is exposed by the indexer
    # REST, so `lag` is honestly omitted rather than fabricated.
    get_explorer_indexers() returns (rep: indexersOut)
    # Reports the on-chain price/data oracles from the graph's O-Chain
    # PriceFeed registry. A reachable graph with no feeds answers an honest empty list;
    # an unreachable or erroring graph likewise degrades to an empty list at 200 rather
    # than a 502, so the console never error-toasts. No feed is ever fabricated.
    get_explorer_oracles() returns (rep: oraclesOut)
}

# ---------------------------------------------------------------------
# 2 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   indexersOut.Indexers  explorer.indexerView (list element)
#   oraclesOut.Oracles  explorer.oracleView (list element)
