# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package network

struct identityIn {
    Name  text       @0
    Roles list<text> @8
}

struct identityList {
    Identities list<bytes> @0
}

struct identityRef {
    ID text @0
}

struct identityView {
    ID         text       @0
    Name       text       @8
    Roles      list<text> @16
    Enrollment bytes      @24
}

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

struct publishedView {
    ID   text @0
    Name text @8
    DNS  text @16
}

struct routerList {
    Routers list<bytes> @0
}

struct serviceIn {
    Name text @0
    Host text @8
    Port i64  @16
}

interface network {
    # Removes one of the org's fabric identities. The device's
    # credential stops authenticating and its enrollment, if unspent, stops
    # enrolling.
    # An id belonging to another org — or to nothing — is 404 before any write
    # reaches the controller: whether an identity exists is itself a cross-tenant
    # fact, and a delete may only ever act on what the caller could list.
    delete_network_identities_by_id(req: identityRef)
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
    # Returns the fabric identities the caller's org owns.
    # One row per identity tagged with the org's "org-<org>" role attribute — a
    # device minted here, enrolled or not. An identity that has not yet enrolled
    # still carries its one-time enrollment, so a mislaid JWT is read again here
    # rather than re-minted.
    # A tenancy read over the full inventory, so like the mesh list it does NOT
    # degrade: an unconfigured deployment answers 503.
    get_network_identities() returns (rep: identityList)
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
    # Mints a fabric identity for a device the caller's org brings.
    # The identity is created of type Device, tagged with the org's "org-<org>" role
    # attribute plus any supplied roles — each scoped to the org, and a
    # "<service>-host" role refused unless the org has published that service. The
    # answer carries the controller's one-time enrollment JWT: the device presents
    # it once to join the fabric, and until it does the same token can be read back
    # off GET /v1/network/identities.
    # A write, so it does not degrade: an unconfigured deployment answers 503.
    post_network_identities(req: identityIn) returns (rep: identityView)
    # Puts a name on the org's overlay: a fabric service forwarding
    # to host:port on whichever of the org's devices carries the "<name>-host"
    # role, dialable at "<name>.<org>.ziti" by any of the org's identities — and by
    # the cloud's own, which is what lets a BYO cluster's apiserver be attached to
    # the fleet with a ".ziti" kubeconfig.
    # Answers 201 with the service and its DNS name. The objects behind it are
    # created in dependency order and unwound on failure, so a half-published
    # service never lingers on the fabric.
    # A write, so it does not degrade: an unconfigured deployment answers 503.
    post_network_services(req: serviceIn) returns (rep: publishedView)
}

# ---------------------------------------------------------------------
# 8 op(s) here. What follows is what this schema does not carry.
#
# opaque (5) — crosses, arrives without its name:
#   identityList.Identities  network.identityView (list element)
#   identityView.Enrollment  network.enrollmentView
#   meshServiceList.Services  network.meshView (list element)
#   networkList.Networks  network.networkView (list element)
#   routerList.Routers  network.routerView (list element)
