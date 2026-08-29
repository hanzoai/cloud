# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package channels

struct allowlistRef {
    Channel text @0
}

struct approvePairingIn {
    Channel text @0
    Code    text @8
}

struct chatChannels {
    Channels list<bytes> @0
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

interface channels {
    # Reports every chat channel this org can send through, and whether it can
    # send through it right now.
    # A channel appears here whether or not it is connected — an empty list would
    # leave a caller unable to tell "this org has no Slack" from "Slack is down",
    # which are different problems with different fixes. Each entry carries the
    # connection behind it, so the answer to "why can I not post?" is in the same
    # response as the channel that cannot post.
    get_channels() returns (rep: chatChannels)
    # Returns the messages people have sent to the caller org's connected chat
    # bots, oldest first, in the portable envelope shape every transport normalises
    # into. It is a CURSOR feed, not a search: pass the returned cursor back as
    # `since` to get only what has arrived since. Only this org's messages are
    # stored under this org, so the feed can never carry another tenant's chat.
    get_channels_inbox(req: inboxIn) returns (rep: inboxPage)
    # Returns the pairing requests waiting for the caller org to approve
    # — one per person who messaged a connected bot on a channel whose DM policy is
    # "pairing" and who is not allowed yet. Each row carries the CODE an org admin
    # passes to POST /v1/channels/pairing/approve. Expired requests are not
    # returned. Codes are capability strings: they are shown here, and never logged.
    get_channels_pairing() returns (rep: pairingQueue)
    # Turns one pending pairing code into a standing allow entry, so
    # that person can DM the org's bot on that channel from now on. It requires ORG
    # ADMIN, not merely membership. The first approval an org makes on a channel also
    # bootstraps that sender as the channel's owner, which the answer reports. An
    # unknown or expired code is a 404, and a code always belongs to exactly one
    # org, so it can never approve someone into another tenant.
    post_channels_pairing_approve(req: approvePairingIn) returns (rep: pairingApproved)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   get_channels_allowlist  allowlistView.AccessGroups  map[string]map[string][]string  (map)
#   put_channels_allowlist  allowlistPutIn.AccessGroups  map[string]map[string][]string  (map)
#   put_channels_allowlist  allowlistView  channels.allowlistView  (reaches one)
#
# opaque (3) — crosses, arrives without its name:
#   chatChannels.Channels  channels.channelView (list element)
#   inboxPage.Messages  channels.inboxView (list element)
#   pairingQueue.Pending  channels.pairingView (list element)
