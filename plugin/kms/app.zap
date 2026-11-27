# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package kms

struct kmsConfig {
    APIBase   text @0
    Brand     text @8
    Issuer    text @16
    LoginPath text @24
}

struct kmsHealth {
    Audit     text       @0
    Enforcing bool       @8
    Error     text       @16
    Policy    text       @24
    Ready     bool       @32
    Refused   text       @40
    Service   text       @48
    Signing   list<bool> @56
    Status    text       @64
}

struct kmsList {
    Env         text @0
    Environment text @8
    Path        text @16
    SecretPath  text @24
}

struct kmsLogin {
    ClientID     text @0
    ClientSecret text @8
}

struct kmsPut {
    Env   text @0
    Name  text @8
    Path  text @16
    Value text @24
}

struct kmsRef {
    Env    text @0
    Secret text @8
}

struct kmsRemoved {
    Deleted bool @0
    Env     text @8
    Name    text @16
}

struct kmsSecret {
    Env   text @0
    Name  text @8
    Value text @16
}

struct kmsSecrets {
    Names   list<text>  @0
    Secrets list<bytes> @8
    Total   i64         @16
}

struct kmsStored {
    Env    text @0
    Name   text @8
    Stored bool @16
}

struct kmsToken {
    AccessToken text @0
    ExpiresIn   i64  @8
    TokenType   text @16
}

interface kms {
    # Removes one secret from your org.
    # Forgets one secret belonging to the caller's own org and confirms the name and
    # environment that were removed. Deleting a secret that is not there is a 404,
    # not a silent success, so a caller can tell a real deletion from a typo.
    # `secret` is the coordinate beneath the caller's org root, subpath and name
    # together, and over HTTP it is the trailing path itself. `env` selects the
    # environment and falls back to the default when omitted. The org comes from the
    # validated claim, never from the request.
    # A delete is a WRITE to the grant rule, like the put: destroying a secret is as
    # consequential as replacing it, and a credential distributed to READ one must
    # not be able to remove it. It opens only for SuperAdmin holding an access
    # token, audited, or for a holder a declared WRITE grant covers. Fail-closed
    # admission, in order: a validated member, well-formed org, master key present,
    # well-formed address, then the grant — 401/403, 400, 503, 400 and 403, all
    # decided before any record is touched.
    # A value this deployment seals on the org's behalf is removed by the app that
    # holds it, and never here for membership or org admin, whatever the grant
    # declaration says about enforcement: deleting a wallet's signing key or an MCP
    # server's credential destroys what the app acts with.
    delete_kms_secrets_by_wildcard1(req: kmsRef) returns (rep: kmsRemoved)
    # Returns the runtime configuration for the KMS console.
    # What the console needs before anyone has signed in: the brand, the OIDC issuer
    # it authenticates against, the API base for this subsystem and the path of the
    # login exchange.
    # Public on purpose, and it holds nothing sensitive — it is deliberately kept
    # under this subsystem's own namespace rather than under an admin prefix, so a
    # gateway that admin-gates the admin routes cannot break the console's
    # legitimate pre-login fetch.
    get_kms_config() returns (rep: kmsConfig)
    # Reports whether this broker can actually serve secrets.
    # A real readiness probe, not a liveness stub: 200 only when the store is open
    # AND a master key is configured, with `signing` reporting whether signing keys
    # are set up too. Anything less answers 503 with `ready:false` and the reason —
    # no in-process store, or no master key — which are exactly the two states in
    # which the secret operations refuse.
    # Not token-gated, because the platform must be able to probe it without a
    # credential. It reports the broker's configuration state only; no secret, no
    # key material and no tenant name appears in it.
    get_kms_health() returns (rep: kmsHealth)
    # Lists the secrets your org holds, without their values.
    # Returns the METADATA of the caller's own secrets: each one's name, path,
    # environment and sealing scheme. No value and no ciphertext is included — this
    # operation exists to enumerate what is held, and reading a value is a separate,
    # per-secret call.
    # Scoped to the caller's own org and nothing else, structurally: there is no org
    # in the path, the store root is derived from the validated org claim, and a
    # caller therefore has no way to name another tenant's namespace. `path` narrows
    # to a subpath and `env` selects the environment; both are also accepted under
    # the operator's spellings, `secretPath` and `environment`. An omitted `env`
    # means every environment and an omitted `path` means the whole org, because a
    # default here reported a populated store as empty.
    # Who sees what: a SuperAdmin holding an access token (audited) sees every name,
    # and so does an admin of the org holding their own access token, outside the
    # brand and platform orgs, whose stores are the platform's. Any other caller sees
    # only the names a declared grant lets it read or write, and is refused with a 403
    # when none of its grants lies inside the subtree asked about. Membership alone
    # shows nothing.
    # Admission is fail-closed and in order: a validated member, an org that is a
    # DNS-1123 label, a store holding a master key, well-formed input, then who may
    # list — 403, 400, 503, 400 and 403 respectively, all decided before any record
    # is touched.
    get_kms_secrets(req: kmsList) returns (rep: kmsSecrets)
    # Reads one secret's value from your org.
    # Opens one sealed secret belonging to the caller's own org and returns its
    # value, with the name and environment it was resolved under. This is the
    # broker's purpose, and the response body is the ONLY place the value appears —
    # it is not logged, and it is never carried in an error.
    # `secret` is the coordinate beneath the caller's org root, subpath and name
    # together, and over HTTP it is the trailing path itself. `env` selects the
    # environment and falls back to the default when omitted. A secret that is not
    # there is a plain 404 that names nothing about the store.
    # Scoped to the caller's own org and nothing else: there is no org in the
    # address, so another tenant's secret is not merely refused, it is unnameable.
    # Within that org the value opens only for SuperAdmin holding an access token,
    # audited, or for a holder a declared READ grant covers at this exact path, key
    # and environment; membership and org admin alone open nothing. Admission is
    # fail-closed and in order — a validated member, an org that is a DNS-1123
    # label, a store holding a master key, a well-formed address, then the grant —
    # 401/403, 400, 503, 400 and 403, all decided before any record is touched, so
    # a refused caller learns nothing about whether the secret exists.
    # A value this deployment seals on the org's behalf — an MCP server's
    # credential, a repository's environment secret, a connected account's token —
    # is read by the app that sealed it, and never opens here for membership or org
    # admin, whatever the grant declaration says about enforcement. A deployed
    # app's or a validator's own folder also opens for the org's KMS-sync identity,
    # which projects it into the org's namespace.
    get_kms_secrets_by_wildcard1(req: kmsRef) returns (rep: kmsSecret)
    # Exchanges a machine credential for an IAM bearer token.
    # Takes a tenant's machine credential — a client id and client secret — and
    # returns an owner-scoped IAM access token with its lifetime, which is the
    # bearer the caller then carries on the org-scoped secret operations.
    # It is deliberately public and unauthenticated, because it IS the credential
    # exchange and runs before any principal exists. That makes it the one route in
    # this subsystem rate-limited PER SOURCE IP, keyed on the real TCP peer rather
    # than on any caller-supplied header, and body-capped in the same place.
    # The submitted secret is never logged and never echoed, and failures collapse
    # to one clean status with no upstream detail: 401 when the credential does not
    # authenticate, 502 when the identity provider is unreachable, 503 when no
    # issuer is configured. That is on purpose — a richer error would be a validity
    # oracle for guessed credentials.
    post_kms_auth_login(req: kmsLogin) returns (rep: kmsToken)
    # Stores or replaces one secret in your org.
    # Upserts one secret under the caller's own org. The value is sealed before it
    # is written — a fresh per-secret data key, itself wrapped by the master key —
    # so plaintext never reaches disk. The receipt confirms the name and environment
    # that were written and does not echo the value.
    # `env` is REQUIRED on a write and has no default, which is the rule most easily
    # got wrong here: reads and deletes still fall back to the default environment
    # for older callers, but a write must not, because the environment is part of
    # the storage key. A silently defaulted write lands in a bucket the readers that
    # resolve project, environment and path never look in, and the stale value keeps
    # being served — so the write fails loudly instead.
    # `name` is required, `path` is an optional subpath beneath the org root, and
    # the org is taken from the validated claim rather than the body. A `path` that
    # starts with `/orgs/` is refused with a 400 that names where it would have
    # landed: the root is already `/orgs/<org>`, so `/orgs/hanzo` written in org
    # admin is stored at `/orgs/admin/orgs/hanzo`, which no reader of org hanzo
    # looks in. The org is chosen with the credential (the CLI's `--as`).
    # Opens only for SuperAdmin holding an access token, audited, or for a holder
    # a declared WRITE grant covers at this exact path, key and environment. Org
    # admin confers nothing here. Fail-closed admission, in order: a validated
    # member, well-formed org, master key present, well-formed input, then the
    # grant — 401/403, 400, 503, 400 and 403, all decided before any record is
    # touched.
    # A value this deployment seals on the org's behalf — an MCP server's
    # credential, a wallet's signing key, a delivery provider's token — is written
    # by the app that holds it, through that app's own operation, and is never
    # replaced here for membership or org admin, whatever the grant declaration
    # says about enforcement.
    post_kms_secrets(req: kmsPut) returns (rep: kmsStored)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   kmsSecrets.Secrets  kms.SecretMeta (list element)
