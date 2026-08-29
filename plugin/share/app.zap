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

struct sharesOut {
    Shares list<bytes> @0
}

interface share {
    # Returns the tunnel shares the caller's org currently has open, across
    # every environment that org has enabled. It is a READ and it degrades honestly: an
    # unconfigured deployment, an org that has not provisioned yet, and an unreachable
    # controller all answer an EMPTY list at 200 rather than an error, so the console
    # never error-toasts on load.
    get_share() returns (rep: sharesOut)
    # Enable provisions the caller org's tunnel account and returns the credential the
    # `hanzo share` CLI needs to run a tunnel. It is idempotent: the account is keyed
    # deterministically off the VALIDATED org, so a repeat call hands back the same
    # account rather than creating a second one, and a caller can only ever provision
    # their OWN org's account. 503 when the deployment has no share controller
    # configured; 502 when that controller is unreachable.
    post_share_enable() returns (rep: enableResp)
}

# ---------------------------------------------------------------------
# 2 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   sharesOut.Shares  share.shareView (list element)
