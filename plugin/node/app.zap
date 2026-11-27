# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package node

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
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   nodesView.Nodes  node.nodeView (list element)
