# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package market

struct History {
    Chain text        @0
    At    text        @8
    Reach bytes       @16
    Days  list<bytes> @24
}

struct Pools {
    Chain text        @0
    Reach bytes       @8
    Pools list<bytes> @16
}

struct Survey {
    Chain   text        @0
    RPC     text        @8
    Reach   bytes       @16
    Carries list<bytes> @24
}

struct Tokens {
    Chain  text        @0
    Reach  bytes       @8
    Tokens list<bytes> @16
}

struct where {
    Chain text @0
}

struct which {
    Chain text @0
    At    text @8
}

interface market {
    # Answers the automated market makers on one chain: their two tokens, their fee tier,
    # and what has moved through each.
    # The two price fields on a pool are the ratio its own reserves stand at, as the
    # indexer computed them. They are not a price ON either token and not a mark: nothing
    # here derives one, ranks the pools, or names a route through them.
    # A chain with no market maker deployed answers `read` with no pools. That is the
    # chain's real condition, and it is deliberately not the same answer as an indexer
    # that could not be asked.
    get_market_pools(req: where) returns (rep: Pools)
    # Answers which of the four settlement precompiles carry code on one chain.
    # An address with no code answers a call with empty data rather than an error, so
    # "this chain has no view precompile" and "this market was never opened" reach a
    # caller as the same silence — and only the second is a fact about a market. This
    # says which it is, by asking the node for the code at each address.
    # It reads presence and nothing else. No market, no quote, no depth and no order is
    # requested here, and `eth_getCode` is the only method this operation ever sends.
    get_market_survey(req: where) returns (rep: Survey)
    # Answers one token's daily history — open, high, low, close, price and volume per
    # UTC day, oldest first.
    # Every figure is the indexer's own arithmetic, passed through as the decimal string
    # it computed. Nothing here rounds one, converts one, or fills a gap: a day the
    # indexer holds no figure for arrives with that field absent, which says "not
    # indexed" where a zero would say "worth nothing".
    get_market_token(req: which) returns (rep: History)
    # Answers the tokens one chain's indexer has seen, with the decimals a caller needs
    # to read any amount of one correctly.
    # This is what the indexer INGESTED, which is not the same as what exists on the
    # chain: a token nothing has traded has no row here, and this is not a registry of
    # what is permitted or listed.
    get_market_tokens(req: where) returns (rep: Tokens)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_market_chains  Roster.Chains  []market.Market  (no wire form)
#
# opaque (8) — crosses, arrives without its name:
#   History.Days  market.Day (list element)
#   History.Reach  market.Reach
#   Pools.Pools  market.Pool (list element)
#   Pools.Reach  market.Reach
#   Survey.Carries  market.Precompile (list element)
#   Survey.Reach  market.Reach
#   Tokens.Reach  market.Reach
#   Tokens.Tokens  market.Token (list element)
