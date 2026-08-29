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
    Error   text @0
    Ready   bool @8
    Service text @16
    Signing bool @24
    Status  text @32
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
    # Requires ADMIN authority over the org, like the write: destroying a secret is
    # an administrative act, and a credential distributed to READ one must not be
    # able to remove it. A machine credential holds no membership and so is never an
    # org admin. Fail-closed admission, in order: admin of the org, well-formed org,
    # master key present — 403, 400 and 503, all decided before any record is
    # touched.
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
    # Admission is fail-closed and in order: a validated member, an org that is a
    # DNS-1123 label, and a store holding a master key — 403, 400 and 503
    # respectively, all decided before any record is touched.
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
    # Admission is fail-closed and in order — a validated member, an org that is a
    # DNS-1123 label, and a store holding a master key — 403, 400 and 503, all
    # decided before any record is touched, so an unconfigured master key is a 503
    # rather than an empty read.
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
    # the org is taken from the validated claim rather than the body.
    # Requires ADMIN authority over the org — a member reads, an admin writes. A
    # machine credential holds no membership and so is never an org admin: it can
    # read the secrets it was issued for and cannot replace one. Fail-closed
    # admission, in order: admin of the org, well-formed org, master key present —
    # 403, 400 and 503, all decided before any record is touched.
    post_kms_secrets(req: kmsPut) returns (rep: kmsStored)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   kmsSecrets.Secrets  kms.SecretMeta (list element)
