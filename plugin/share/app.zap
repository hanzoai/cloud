# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package share

struct enableResp {
    AccountToken text @0
    Controller   text @8
    Namespace    text @16
    URLTemplate  text @24
}

struct rotated {
    Ended   i64  @0
    Pending text @8
}

struct sharesOut {
    Shares list<bytes> @0
}

interface share {
    # Returns the tunnel shares the caller's account currently has open —
    # their org's, or in a held org their own — across every environment it has
    # enabled. It is a READ and it degrades honestly: an
    # unconfigured deployment, an org that has not provisioned yet, and an unreachable
    # controller all answer an EMPTY list at 200 rather than an error, so the console
    # never error-toasts on load.
    get_share() returns (rep: sharesOut)
    # Provisions the caller's tunnel account — their org's, or in a held org,
    # where every self-serve signup lands, their own — and returns the credential the
    # `hanzo share` CLI needs to run a tunnel. It is idempotent: the account is keyed
    # deterministically off the VALIDATED org and, in a held org, the caller's
    # attested subject, so a repeat call hands back the same account rather than
    # creating a second one, and a caller can only ever provision their OWN. 503 when
    # the deployment has no share controller configured; 503 when that controller is
    # unreachable.
    post_share_enable() returns (rep: enableResp)
    # Replaces the credential of a tunnel account and ends every
    # environment enabled with it, shares and all, so a credential handed out before
    # — to a member who has left, or to anyone who read it — opens nothing from now
    # on. The account is kept: enable hands its holder the new credential.
    # In a customer's org the account is the org's, which its members share, and an
    # admin of the org or a SuperAdmin rotates it. In a held org each person's
    # account is their own, and they rotate it — every account they hold, the one
    # an API key of theirs was handed before included, so none stays live. An admin
    # of the org or a SuperAdmin then rotates the org's own account, which no call
    # there hands out, whether or not they hold an account of their own; when that
    # one cannot be rotated, `pending` says so and their own are rotated still.
    # An account never provisioned has nothing to rotate and counts 0. 503 when the
    # deployment has no share controller configured; 503 when that controller is
    # unreachable, or holds the caller's account under a credential this deployment
    # does not derive — out of a rotation's reach, so it is refused rather than
    # reported done.
    post_share_rotate() returns (rep: rotated)
}

# ---------------------------------------------------------------------
# 3 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   sharesOut.Shares  share.shareView (list element)
