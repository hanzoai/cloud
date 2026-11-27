# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package node

struct callNodeIn {
    ID             text  @0
    Command        text  @8
    Params         bytes @16
    TimeoutMs      i64   @24
    IdempotencyKey text  @32
}

struct invokeView {
    OK      bool  @0
    Payload bytes @8
    Code    text  @16
    Message text  @24
}

struct nodesView {
    Nodes list<bytes> @0
}

interface node {
    # Returns the caller org's currently connected bot nodes: what each one
    # calls itself, the platform it runs on, its agent version, when its socket was
    # established, and the capabilities and commands it reported.
    # Only this org's nodes are listed — the org is half of every key in the table it
    # reads — and only nodes attached to THIS replica, because the list is of live
    # sockets rather than of registrations. The capability and command lists are the
    # node's own self-report: useful to show, never load-bearing, because what a node
    # may actually be asked to do is decided at the socket against the deployment's
    # allowlist.
    get_node() returns (rep: nodesView)
    # Asks one of the caller org's connected machines to execute a declared
    # command (such as computer control, browser proxy, or local MCP tool call) and
    # returns the node's JSON answer. Projected into POST /v1/mcp so mobile apps,
    # cloud agents, and third-party MCP clients can control linked machines securely
    # under the caller's IAM-validated organization.
    post_node_by_id_call(req: callNodeIn) returns (rep: invokeView)
}

# ---------------------------------------------------------------------
# 2 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   nodesView.Nodes  node.nodeView (list element)
