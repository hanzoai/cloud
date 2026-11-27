# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package account

struct apiKey {
    ID      text       @0
    Name    text       @8
    Type    text       @16
    Prefix  text       @24
    Key     text       @32
    Status  text       @40
    Creator text       @48
    Created text       @56
    Expires text       @64
    Revoker text       @72
    Revoked text       @80
    Limit   list<text> @88
    Budget  bytes      @96
    Spend   bytes      @104
    Rate    i64        @112
    Used    text       @120
}

struct apiKeyList {
    Keys list<bytes> @0
}

struct appearance {
    Type    f64  @0
    Density text @8
    Accent  text @16
}

struct csrfResp {
    Token     text @0
    ExpiresIn i64  @8
}

struct embedStatusReq {
    App text @0
}

struct embedStatusResp {
    App       text @0
    Origin    text @8
    EmbedURL  text @16
    Reachable bool @24
    Entitled  bool @25
    Phase     text @32
}

struct keyEdit {
    ID      text       @0
    Name    list<text> @8
    Limit   list<text> @16
    Expires list<text> @24
    Budget  bytes      @32
    Rate    list<i64>  @40
}

struct keyIn {
    Name    text       @0
    Type    text       @8
    Limit   list<text> @16
    Expires text       @24
    Budget  bytes      @32
    Rate    i64        @40
}

struct keyRef {
    ID text @0
}

struct onboardReq {
    Name     text @0
    Personal bool @8
}

struct onboardResp {
    Org          text @0
    DisplayName  text @8
    AccessKey    text @16
    AccessSecret text @24
}

interface account {
    # Revokes exactly one API key, by id. Every other key keeps working.
    # The key stays listed as revoked, with who revoked it and when, and can never be
    # used again. IAM refuses it at once; cloud caches a resolved key for up to 60
    # seconds, so a request inside that window may still be served.
    # A member revokes their own keys; an org admin revokes any of the org's, and
    # revoking someone else's is recorded on the audit trail.
    delete_account_keys_by_id(req: keyRef) returns (rep: apiKey)
    # Returns the signed-in caller's own appearance preference — text
    # size, density and accent — read from their IAM account so it is the same on
    # every device and every Hanzo surface. An unset preference is an empty object.
    # A transient IAM read failure reports the empty preference rather than a 5xx, so
    # a surface applies its published default and never error-toasts on load — the
    # same fail-soft the key read uses.
    get_account_appearance() returns (rep: appearance)
    # Mints the anti-forgery token a browser echoes as X-CSRF-Token on
    # every change it asks for. The token is bound to the caller's validated identity
    # and expires, so one minted for one identity cannot authorize a change as another.
    # It is answered no-store, so it is never cached by a shared proxy. This is the
    # same-origin endpoint the embedded console reads — the Same-Origin Policy is what
    # stops a cross-site page from reading the response and forging a change.
    get_account_csrf() returns (rep: csrfResp)
    # Reports whether one of this brand's shared embedded apps (cms, erp,
    # help) may be framed by the caller and is actually running, so a console module
    # can choose between the embed and the provision panel.
    # It answers two questions the browser cannot answer for itself. ENTITLEMENT is
    # server-authoritative: each app is a single shared per-BRAND instance, so only a
    # member of the owning brand org — or a SuperAdmin — is given the embed URL; every
    # other caller gets phase "not-entitled" and no URL. REACHABILITY is a probe of
    # that origin, which a cross-origin page cannot read for itself.
    # The probed host is always <app>.<this deployment's own brand domain>: no part of
    # it comes from the request, so this can never be steered into probing an
    # arbitrary origin.
    get_account_embed(req: embedStatusReq) returns (rep: embedStatusResp)
    # Returns the API keys the caller may see, newest first: their own, or
    # every key the org holds when the caller administers it. Each is its own
    # credential with its own name, prefix, status, expiry and limit. Revoked keys
    # stay listed. No secret material comes back; a publishable key, which is public
    # by construction, carries its full value.
    get_account_keys() returns (rep: apiKeyList)
    # Changes one key's name, permissions, budget, rate or expiry. The secret
    # is not reissued: the key in your deployment keeps working, under its new
    # policy. Only the person the key belongs to may edit it, and a revoked key
    # cannot be edited.
    patch_account_keys_by_id(req: keyEdit) returns (rep: apiKey)
    # Stores the caller's appearance preference on their IAM account,
    # preserving every other field of the row. The accent is validated as a real
    # colour token before it is stored; an unset or invalid axis is dropped rather
    # than stored.
    post_account_appearance(req: appearance) returns (rep: appearance)
    # Creates a NEW API key and returns its secret ONCE. It never revokes
    # or changes any other key: every key the caller already holds keeps working.
    # The key is filed in the org the caller acts in and names them as its creator.
    # An org holds at most 1,000 live keys; past that the create is refused until one
    # is revoked.
    post_account_keys(req: keyIn) returns (rep: apiKey)
    # Creates an organization and makes the caller its owner.
    # The caller's own account is not touched: who they are, the org their account
    # lives in and how they sign in are the same before and after. The new org is a
    # separate organization they belong to by membership, carried in the signed
    # `orgs` claim of their next token, and reached by selecting it (X-Org-Id, the
    # org switcher). Teammates join it the same way. The org starts at a zero
    # balance, and its default API key is minted for the creator and returned once.
    # Moving the creator into the org instead re-keyed their identity to
    # "<org>/<name>", and sign-in, which resolves an address in the signup org,
    # then found nobody: the person was offered a second account for their own
    # address.
    post_account_orgs(req: onboardReq) returns (rep: onboardResp)
}

# ---------------------------------------------------------------------
# 9 op(s) here. What follows is what this schema does not carry.
#
# opaque (5) — crosses, arrives without its name:
#   apiKey.Budget  account.keyBudget
#   apiKey.Spend  account.keyBudget
#   apiKeyList.Keys  account.apiKey (list element)
#   keyEdit.Budget  account.keyBudget
#   keyIn.Budget  account.keyBudget
