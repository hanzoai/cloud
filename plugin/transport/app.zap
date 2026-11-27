# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package transport

struct allowlistRef {
    Transport text @0
}

struct approvePairingIn {
    Transport text @0
    Code      text @8
}

struct chatTransports {
    Transports list<bytes> @0
}

struct inboxIn {
    Since text @0
    Limit text @8
}

struct inboxPage {
    Cursor   i64         @0
    Messages list<bytes> @8
}

struct pairingApproved {
    OwnerBootstrapped bool @0
    Sender            text @8
}

struct pairingQueue {
    Pending list<bytes> @0
}

struct transportAgentRef {
    Transport text @0
}

interface transport {
    # Reports every chat transport this org can send through, and whether it can
    # send through it right now.
    # A transport appears here whether or not it is connected — an empty list would
    # leave a caller unable to tell "this org has no Slack" from "Slack is down",
    # which are different problems with different fixes. Each entry carries the
    # connection behind it, so the answer to "why can I not post?" is in the same
    # response as the transport that cannot post.
    get_transport() returns (rep: chatTransports)
    # Returns the caller's own messages to the org's connected chat bots —
    # those recorded on the caller's linked account — oldest first, in the portable
    # envelope shape every transport normalises into. It is a CURSOR feed, not a
    # search: pass the returned cursor back as `since` to get only what has arrived
    # since. A message is its person's: another member's is never listed, an admin
    # of the org reads only their own, and one recorded on no account is listed to a
    # SuperAdmin alone.
    get_transport_inbox(req: inboxIn) returns (rep: inboxPage)
    # Returns the pairing requests waiting for the caller org to approve
    # — one per person who messaged a connected bot on a transport whose DM policy is
    # "pairing" and who is not allowed yet. Each row carries the CODE an org admin
    # passes to POST /v1/transport/pairing/approve. Expired requests are not
    # returned. Codes are capability strings: they are shown here, and never logged.
    # Reading them is an org admin's, as approving them is.
    get_transport_pairing() returns (rep: pairingQueue)
    # Turns one pending pairing code into a standing allow entry, so
    # that person can DM the org's bot on that transport from now on. It requires ORG
    # ADMIN, not merely membership. The first approval an org makes on a transport also
    # bootstraps that sender as the transport's owner, which the answer reports. An
    # unknown or expired code is a 404, and a code always belongs to exactly one
    # org, so it can never approve someone into another tenant.
    post_transport_pairing_approve(req: approvePairingIn) returns (rep: pairingApproved)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# blocked (6) — the op is absent; the field has no wire form:
#   get_transport_agent  transportAgents.Channels  map[string]string  (map)
#   get_transport_allowlist  allowlistView.AccessGroups  map[string]map[string][]string  (map)
#   put_transport_agent  transportAgents  transport.transportAgents  (reaches one)
#   put_transport_agent  transportAgentsPut.Channels  map[string]string  (map)
#   put_transport_allowlist  allowlistPutIn.AccessGroups  map[string]map[string][]string  (map)
#   put_transport_allowlist  allowlistView  transport.allowlistView  (reaches one)
#
# opaque (3) — crosses, arrives without its name:
#   chatTransports.Transports  transport.transportView (list element)
#   inboxPage.Messages  transport.inboxView (list element)
#   pairingQueue.Pending  transport.pairingView (list element)
