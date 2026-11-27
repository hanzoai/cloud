# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package network

struct InviteCheckIn {
    Code text @0
}

struct InviteIssueIn {
    Count       i64  @0
    Note        text @8
    ExpiresDays i64  @16
}

struct InviteIssued {
    Invites list<bytes> @0
}

struct InviteList {
    Data list<bytes> @0
    Next text        @8
}

struct InviteQuery {
    Limit  i64  @0
    Cursor text @8
}

struct InviteRef {
    ID text @0
}

struct InviteVerdict {
    Valid bool @0
}

struct WaitlistEntry {
    Status text @0
}

struct WaitlistIn {
    Email   text @0
    XHandle text @8
    Role    text @16
    Source  text @24
    Locale  text @32
}

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
    ExternalID text       @16
    Roles      list<text> @24
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

struct serviceRef {
    ID text @0
}

interface network {
    # Revokes one invite and answers 204: its code stops verifying at once.
    # Revoking one that is already revoked is the same 204, and an id that names no
    # invite is 404. It is a SuperAdmin's; another caller is refused 403, audited. Every
    # attempt is on the audit trail with its outcome, and the trail takes the intent before
    # an invite is changed and answers 503 with nothing changed when it cannot.
    delete_admin_network_invites_by_id(req: InviteRef)
    # Takes one of the org's fabric identities out of the org.
    # The identity's "org-<org>" role and every role scoped to the org are
    # removed and the rest is left alone, so an identity another org shares keeps
    # working there. The identity itself is deleted when no org role is left on
    # it, or when the caller IS its IAM subject — a person may always remove
    # themselves.
    # An id belonging to another org — or to nothing — is 404 before any write
    # reaches the controller: whether an identity exists is itself a cross-tenant
    # fact, and a delete may only ever act on what the caller could list, which for
    # a plain member is its own identity. A fabric administrator is the
    # controller's own and is refused.
    delete_network_identities_by_id(req: identityRef)
    # Takes a name off the org's overlay: the two policies, the
    # service and its two configs that publishing made, in that order, and the
    # "<name>-host" role from each of the org's identities — that role exists
    # only for the bind policy to select, and a role naming no service is one
    # the identities endpoint refuses to write.
    # Only objects carrying this service's own names are deleted, so a policy
    # that selects the service by attribute is left alone. An id belonging to
    # another org — or to nothing — is 404 before any write, as for an identity.
    # Like publishing it is a steward's act, and a plain member is 403 before the
    # controller is read.
    delete_network_services_by_id(req: serviceRef)
    # Lists the invites, newest first: each one's id, status, note, issue
    # time, expiry and who issued it. It never returns a code, nor anything a code can
    # be got from. It is a SuperAdmin's; another caller is refused 403, audited. A
    # status is active, revoked, or expired for an active invite past its expiry.
    get_admin_network_invites(req: InviteQuery) returns (rep: InviteList)
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
    # One row per identity tagged with the org's "org-<org>" role attribute, each
    # naming the IAM subject it logs in as. A steward sees every one; a plain member
    # sees only its own, since who else is on the org's network is not a member's
    # to read, and a plain member's API key, which holds no identity, is refused.
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
    # Issues invite codes and answers them in clear, once. It is a
    # SuperAdmin's, and no one else may call it: another caller is refused 403 and the
    # refusal is audited. Each code is HNZ- and ten characters of Crockford base32, shown
    # as HNZ-XXXXX-XXXXX, from crypto/rand and unique among the codes issued; only an HMAC
    # of it is stored, so a code
    # shown here and lost is revoked by id and issued again. The audit trail takes the
    # intent, with the count and the note and never a code, before anything is written,
    # and answers 503 with nothing written when it cannot.
    # A count is 1 to 100 and a life 1 to 365 days, and an absent one is one code for
    # 30 days; anything else is 400.
    post_admin_network_invites(req: InviteIssueIn) returns (rep: InviteIssued)
    # Puts the caller on the org's overlay as its own IAM subject.
    # The identity is the one whose externalId is the caller's `sub`, admitted by
    # the controller's "iam" auth policy: the caller logs in to the fabric with its
    # own IAM access token, and nothing is enrolled. It is named by the subject
    # unless a name is given, and carries the org's "org-<org>" role attribute plus
    # any supplied roles — each scoped to the org, and a "<service>-host" role
    # refused unless the org has published that service. Roles are a steward's to
    # take: a plain member joins with the org role alone, and asking for more is
    # 403 before the controller is written.
    # An API key is refused 403: the fabric admits an IAM access token, a person's
    # or an application's, and a key has none to log in with.
    # IDEMPOTENT: a caller who already has an identity gets the same one back, with
    # any of these roles it lacked added — which is how one person's identity comes
    # to serve every org they act in.
    # A write, so it does not degrade: an unconfigured deployment answers 503.
    post_network_identities(req: identityIn) returns (rep: identityView)
    # Says whether an invite code is live: {"valid": true} or {"valid":
    # false}, and nothing else. It never says why a code is not valid: a code that was
    # never issued, one that is not shaped like a code, one that was revoked and one that
    # has expired get the same status and the same body, and it says nothing of who issued
    # a code. Verifying does not consume the code and does not spend it, and it needs no
    # account. Nothing on the server enforces an invite until redemption exists; the
    # operations that will need one will answer with the problem type invite_required.
    # A deployment that cannot hold invites, or has issued none yet, answers 503; then a
    # caller past ten requests an hour is 429, a body over 4 KiB 413 and a missing code or a
    # body that is not JSON 400, in that order. A caller that has missed five times in an
    # hour is refused 429 until the window passes, whatever it asks, so the refusal says
    # nothing about whether a code would have been valid, and Retry-After says when to come
    # back. The limits are per network address (an IPv6 /64, with the /48 around it counted
    # at sixteen times as much): clients behind one address share them, and so do callers
    # whose address is unknown.
    post_network_invite_verify(req: InviteCheckIn) returns (rep: InviteVerdict)
    # Puts a name on the org's overlay: a fabric service forwarding
    # to host:port on whichever of the org's devices carries the "<name>-host"
    # role, dialable at "<name>.<org>.zt" by any of the org's identities — and by
    # the cloud's own, which is what lets a BYO cluster's apiserver be attached to
    # the fleet with a ".zt" kubeconfig.
    # Answers 201 with the service and its DNS name. The objects behind it are
    # created in dependency order and unwound on failure, so a half-published
    # service never lingers on the fabric. Publishing is a steward's act — an
    # admin of the org or its own machine client — and a plain member is 403.
    # A write, so it does not degrade: an unconfigured deployment answers 503.
    post_network_services(req: serviceIn) returns (rep: publishedView)
    # Puts an address or an X handle on the Hanzo Network waitlist and answers
    # 202 pending. Nothing is mailed. It gives the same answer for an identifier that is
    # new and one asked for before, however often, and with the same headers, so it says
    # nothing about who is on the list; a repeat changes nothing, and the first entry
    # stands. An X handle is never looked up anywhere and no wallet address is taken. No
    # account is needed, and the entry keeps nothing about the caller. It also keeps
    # no email or handle: only a keyed HMAC digest of the identifier, under the one
    # invite secret with a domain of its own, is filed.
    # A deployment that cannot hold the waitlist, or has issued no invite yet, answers 503;
    # then a caller past ten requests an hour is 429 (with Retry-After), a body over 4 KiB
    # 413, and a body that is not JSON, one with neither or both of email and xHandle, an
    # invalid one or a role that is not miner, builder, researcher or staker 400, in that
    # order. The ten an hour are per network address: clients behind one address share them,
    # and so do callers whose address is unknown. Nothing here is gated by an invite yet; the operations that will be answer with
    # the problem type invite_required.
    post_network_waitlist(req: WaitlistIn) returns (rep: WaitlistEntry)
}

# ---------------------------------------------------------------------
# 14 op(s) here. What follows is what this schema does not carry.
#
# opaque (6) — crosses, arrives without its name:
#   InviteIssued.Invites  network.IssuedInvite (list element)
#   InviteList.Data  network.InviteView (list element)
#   identityList.Identities  network.identityView (list element)
#   meshServiceList.Services  network.meshView (list element)
#   networkList.Networks  network.networkView (list element)
#   routerList.Routers  network.routerView (list element)
