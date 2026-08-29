# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package account

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

struct keyTypeIn {
    Type  text       @0
    Limit list<text> @8
}

struct mintedKey {
    Type      text       @0
    Key       text       @8
    AccessKey text       @16
    Limit     list<text> @24
}

struct onboardReq {
    Name     text @0
    Personal bool @8
}

struct onboardResp {
    Org          text @0
    DisplayName  text @8
    Additional   bool @16
    AccessKey    text @24
    AccessSecret text @32
}

struct revokedKey {
    OK   bool @0
    Type text @8
}

interface account {
    # Revokes the caller's own API key of the requested class. The class is
    # the same field mint takes — `?type=publishable`, defaulting to secret — so
    # revoking the key that ships in a browser bundle does not sign its holder out of
    # their own API: the other key keeps working.
    # Revoking is how a key is replaced when it does not need replacing; minting the
    # same class again rotates it in one step. IAM drops the credential immediately,
    # but the gateway caches keys for a few minutes, so a request that beat the cache
    # expiry may still be served.
    # For callers written against the older shape, the class is also accepted in a JSON
    # request body, read only when `?type=` is absent.
    delete_account_keys(req: keyTypeIn) returns (rep: revokedKey)
    # Returns the signed-in caller's own appearance preference — text
    # size, density and accent — read from their IAM account so it is the same on
    # every device and every Hanzo surface. An unset preference is an empty object.
    # A transient IAM read failure reports the empty preference rather than a 5xx, so
    # a surface applies its published default and never error-toasts on load — the
    # same fail-soft the key read uses.
    get_account_appearance() returns (rep: appearance)
    # IssueCSRFToken mints the anti-forgery token a browser echoes as X-CSRF-Token on
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
    # Returns the caller's own API keys — every type they hold, read
    # AUTHORITATIVELY from IAM rather than from the session claim, which lags a key
    # minted moments ago. No secret material comes back: a secret key is represented
    # by its prefix, and only a publishable key (public by construction) carries its
    # full value.
    # A transient IAM read failure reports an empty set rather than a 5xx, so the
    # page shows the honest empty state and never a fabricated key.
    get_account_keys() returns (rep: apiKeyList)
    # Stores the caller's appearance preference on their IAM account,
    # preserving every other field of the row. The accent is validated as a real
    # colour token before it is stored; an unset or invalid axis is dropped rather
    # than stored.
    post_account_appearance(req: appearance) returns (rep: appearance)
    # Creates — or rotates — the caller's API key of the requested type and
    # returns it ONCE. A real IAM failure surfaces as 502, never a fabricated key.
    # Rotating is what creating means here: a user holds one key per type, so the
    # endpoint is idempotent by (caller, type) and the superseded credential stops
    # working. Two live secrets for one user would make "revoke my key" a lie.
    post_account_keys(req: keyTypeIn) returns (rep: mintedKey)
    # Onboard creates the caller's organization. Two flows, keyed on whether the caller
    # already has a home org (mirrors app/onboard/route.ts):
    # - FIRST-RUN (no home org): create + MOVE the user in as admin, so their next
    # JWT carries the new owner and the cloud scopes everything to it. This is the
    # path a fresh OAuth sign-up takes, from the sign-up application's org.
    # - ADDITIONAL (owner set): create the org but do NOT move the user — a move
    # changes their IAM owner (stripping a SuperAdmin's status + orphaning their
    # current org). They reach the new org via the OrgSwitcher, which re-scopes
    # X-Org-Id without touching IAM membership. A personal-org request from someone
    # who already has an org is meaningless → 409.
    post_account_orgs(req: onboardReq) returns (rep: onboardResp)
}

# ---------------------------------------------------------------------
# 8 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   apiKeyList.Keys  account.apiKey (list element)
