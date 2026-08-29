# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package web3

struct balances {
    Chain   text @0
    Address text @8
    Native  text @16
}

struct chainList {
    Chains list<bytes> @0
}

struct chainRef {
    Chain text @0
}

struct chainStatus {
    Chain  bytes @0
    Live   bool  @8
    Height i64   @16
}

struct rpcIn {
    Chain   text  @0
    JSONRPC text  @8
    ID      bytes @16
    Method  text  @24
    Params  bytes @32
}

struct rpcOut {
    JSONRPC text  @0
    ID      bytes @8
    Result  bytes @16
    Error   bytes @24
}

struct tokenRef {
    Chain   text @0
    Address text @8
}

interface web3 {
    # Reports the chains this deployment can reach. The list is the
    # declared registry, so it is exactly what /v1/web3/rpc will accept — a chain that
    # appears here is one this deployment actually has an upstream for.
    get_web3_chains() returns (rep: chainList)
    # Reports one chain and whether its upstream is answering. An
    # unreachable chain is still a 200 with live:false — the chain is configured,
    # which is a different fact from the chain being up, and a 502 here would make
    # a console page error rather than show the outage.
    get_web3_chains_by_chain(req: chainRef) returns (rep: chainStatus)
    # Reads an address's native balance on a chain.
    # ERC-20 positions are NOT enumerated here: eth_getBalance answers the native
    # one, but "every token this address holds" is an indexer question — there is no
    # RPC call that answers it, and walking a token list would return a number that
    # silently omits whatever the list missed. explorer owns the indexer
    # relationship; this returns the balance the chain itself can prove.
    get_web3_tokens_by_chain_by_address(req: tokenRef) returns (rep: balances)
    # Forwards a JSON-RPC call to the named chain and returns its answer
    # unchanged. Only declared chains are reachable, and only to a caller with a
    # validated principal — this is the deployment's upstream, not an open relay.
    post_web3_rpc_by_chain(req: rpcIn) returns (rep: rpcOut)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# opaque (3) — crosses, arrives without its name:
#   chainList.Chains  web3.Chain (list element)
#   chainStatus.Chain  web3.Chain
#   rpcOut.Error  web3.rpcError
