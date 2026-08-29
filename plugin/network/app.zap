# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package network

struct meshServiceList {
    Services list<bytes> @0
}

struct networkList {
    Networks list<bytes> @0
}

struct networkRef {
    ID text @0
}

struct networkView {
    ID     text @0
    Name   text @8
    Status text @16
    Nodes  i64  @24
}

struct routerList {
    Routers list<bytes> @0
}

interface network {
    # Returns the caller's org overlay network on the Zero Trust fabric.
    # The org has at most ONE overlay, projected from the edge-routers tagged with its
    # "org-<org>" role attribute: nodes is the real router count and status is
    # "connected" once at least one router has dialed home, "provisioning" while none
    # has. An org with no routers gets an empty list, never a fabricated network.
    # The read degrades rather than erroring: a deployment with no ZT credential, and a
    # controller that cannot be reached, both answer 200 with an empty list so the
    # console's Networks page renders a clean empty state instead of an error.
    get_network() returns (rep: networkList)
    # Returns one overlay network by id, scoped to the caller's org.
    # The org has exactly one overlay network and its id is derived from the org, so
    # any other id — another tenant's, or one that does not exist — is 404 rather than
    # a peek across the tenant boundary. An org whose network exists but has no
    # edge-routers is 404 too, for the same reason the list is empty: there is no
    # overlay until something is on it.
    get_network_by_id(req: networkRef) returns (rep: networkView)
    # Returns the Zero Trust routers the caller's org owns.
    # One row per real ZT edge-router tagged with the org's "org-<org>" role attribute,
    # carrying the controller's own health signal: "online" when connected, "disabled"
    # when administratively disabled, "offline" otherwise. region is filled only from a
    # "region-<slug>" role attribute and omitted when the router carries none, so the
    # column renders "—" rather than a guess.
    # The read degrades rather than erroring: a deployment with no ZT credential, and a
    # controller that cannot be reached, both answer 200 with an empty list.
    get_network_routers() returns (rep: routerList)
    # Returns the Zero Trust edge services the caller's org owns.
    # One row per real ZT edge service tagged with the org's "org-<org>" role
    # attribute: mtls is "required" when the service mandates end-to-end encryption and
    # "enabled" otherwise (the fabric always mutually authenticates every link), and
    # status is "active" because a listed service is a configured, dialable entry. A
    # service tagged for another org, or tagged for none, is invisible here.
    # Unlike the network and router reads this does NOT degrade: an unconfigured
    # deployment answers 503 and an unreachable controller surfaces the upstream's
    # status, so a mesh page never renders "no services" for a fabric it simply could
    # not read.
    get_network_services() returns (rep: meshServiceList)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# opaque (3) — crosses, arrives without its name:
#   meshServiceList.Services  network.meshView (list element)
#   networkList.Networks  network.networkView (list element)
#   routerList.Routers  network.routerView (list element)
