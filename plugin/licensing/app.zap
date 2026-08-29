# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package licensing

struct DownloadRequest {
    Release text @0
    Token   text @8
}

struct FingerprintRequest {
    Signals bytes @0
}

struct FingerprintResponse {
    Fingerprint text @0
    Version     text @8
}

struct HealthView {
    Status  text @0
    Service text @8
    Env     text @16
    Signer  text @24
}

struct IssueRequest {
    Product     text  @0
    Holder      text  @8
    Fingerprint text  @16
    Signals     bytes @24
    Release     text  @32
    TTLSeconds  i64   @40
}

struct IssueResponse {
    Token   text       @0
    AppID   text       @8
    Holder  text       @16
    Nonce   text       @24
    Exp     i64        @32
    Bound   bool       @40
    Feature list<text> @48
}

struct PubkeyView {
    Alg         text        @0
    Provider    text        @8
    PublicKey   text        @16
    Schema      u8          @24
    TokenFormat text        @32
    Keys        list<bytes> @40
}

struct Release {
    ID              text       @0
    Product         text       @8
    AppID           text       @16
    Version         text       @24
    Platform        text       @32
    ArtifactRef     text       @40
    SHA256          text       @48
    CosignSignature text       @56
    CosignCert      text       @64
    MinFeatures     list<text> @72
    Yanked          bool       @80
    CreatedAt       i64        @88
}

struct ReleaseAsset {
    Release         bytes @0
    CosignSignature text  @8
    CosignCert      text  @16
    DownloadURL     text  @24
}

struct ReleaseList {
    Releases list<bytes> @0
}

struct ReleaseRef {
    Release text @0
}

struct RevokeRequest {
    Scope  text @0
    Value  text @8
    Reason text @16
}

struct RevokeResponse {
    Revoked bool  @0
    Entry   bytes @8
}

struct VerifyRequest {
    Token text @0
    App   text @8
}

struct VerifyResponse {
    Valid   bool       @0
    Reason  text       @8
    AppID   text       @16
    Holder  text       @24
    Nonce   text       @32
    Exp     i64        @40
    Revoked bool       @48
    Bound   bool       @49
    Feature list<text> @56
}

interface licensing {
    # Download resolves a release to its artifact, gated on a valid license.
    # The gate is the LICENSE token, not the IAM bearer: being signed in is not
    # permission to download a paid binary — holding a good license for it is. The
    # token must verify against this deployment's public key, be unrevoked, be
    # scoped to the release's app, and carry every feature the release requires.
    # Present it as the `X-License-Token` header (preferred, since a header does not
    # land in proxy logs) or as `?token=`.
    # The response pairs the artifact URL with its cosign signature so the client
    # verifies the binary BEFORE trusting it: a signed URL alone proves where the
    # bytes came from, not what they are. A yanked release is 410 Gone.
    get_licensing_download_by_release(req: DownloadRequest) returns (rep: ReleaseAsset)
    # Health reports which signer this deployment mints with, and in which env.
    # It answers 200 whenever the process is up: there is nothing downstream to
    # probe, since the KMS is reached only when a token is actually minted. Its
    # value is the `signer` field — `"signer":"local"` on a production host says
    # that deployment is signing licenses with a development key, which is a
    # misconfiguration worth paging on rather than a healthy 200.
    get_licensing_healthz() returns (rep: HealthView)
    # Pubkey publishes the Ed25519 PUBLIC verification key, at both /pubkey and
    # /jwks.
    # This is the only public-safe surface here and the reason the whole scheme
    # works offline: the engine embeds or fetches this key once and then verifies
    # every license itself, with no call home per launch. The private half never
    # enters this process — it lives in the KMS — so nothing served here is a
    # secret. `provider` names the KMS holding that half; `"local"` means a
    # development key, and a token signed by one is not a production credential.
    get_licensing_jwks() returns (rep: PubkeyView)
    # Pubkey publishes the Ed25519 PUBLIC verification key, at both /pubkey and
    # /jwks.
    # This is the only public-safe surface here and the reason the whole scheme
    # works offline: the engine embeds or fetches this key once and then verifies
    # every license itself, with no call home per launch. The private half never
    # enters this process — it lives in the KMS — so nothing served here is a
    # secret. `provider` names the KMS holding that half; `"local"` means a
    # development key, and a token signed by one is not a production credential.
    get_licensing_pubkey() returns (rep: PubkeyView)
    # Lists the signed binary releases this deployment can serve.
    # Metadata only, and no download URL: the artifact is behind GET
    # /v1/licensing/download/{release}, which is gated on a valid license token.
    # Knowing that a release exists is not permission to run it, which is why this
    # list needs no license of its own.
    get_licensing_releases() returns (rep: ReleaseList)
    # Reads one release's metadata: its product, version, platform and the
    # cosign material a client verifies the binary against.
    # An unknown id is 404. Like the list, this is metadata only — the bytes are
    # behind the license-gated download.
    get_licensing_releases_by_release(req: ReleaseRef) returns (rep: Release)
    # Fingerprint turns raw device signals into the opaque value that binds a license
    # to one machine.
    # This is the anti-copy step: the value returned here is folded into the signed
    # token, so a token minted with it runs only on the device it was bound to. The
    # derivation is one-way and salted — the signals are never stored and never
    # echoed back — so the response is safe to persist client-side and pass to
    # issue. Signals too weak to identify a machine (a hostname alone) are refused
    # rather than turned into a binding that would collide with other machines.
    post_licensing_fingerprint(req: FingerprintRequest) returns (rep: FingerprintResponse)
    # Issue mints a signed license token for a product the caller's org already pays
    # for.
    # The order is the whole security argument: the caller is an IAM-validated
    # principal, commerce is then asked whether that principal's ORG holds an ACTIVE
    # entitlement for the product, and only then is a token signed — by the KMS,
    # never by key material in this process. A product the org does not own answers
    # 403 and no token. The signed features are the plan's features verbatim, so the
    # engine enforces exactly what was bought, and the expiry is clamped to the
    # entitlement's so a token cannot outlive the subscription that paid for it.
    # The token is the credential the engine runs on. Treat it as a secret.
    post_licensing_issue(req: IssueRequest) returns (rep: IssueResponse)
    # Publishes a signed binary release, answering 201 Created.
    # Outside dev a release MUST carry its cosign signature: this is how a binary
    # becomes downloadable, so accepting an unsigned one would let an unverifiable
    # artifact into the distribution path. Org-admin only — publishing is an
    # operator action, not something a licensee does.
    post_licensing_releases(req: Release) returns (rep: Release)
    # Revoke turns off tokens that have already been issued.
    # A signed token cannot be un-signed, so revocation is the only way to withdraw
    # one: this appends an entry that verify and the license-gated download both
    # consult. It is a POST rather than a DELETE because it APPENDS a durable,
    # attributed record — the entry names the admin who recorded it and when —
    # rather than removing one.
    # Org-admin only. Scope it as narrowly as the incident allows: "nonce" for one
    # leaked token, "holder" for one compromised account, "fingerprint" for one
    # stolen machine, "release" when a whole build is bad.
    post_licensing_revoke(req: RevokeRequest) returns (rep: RevokeResponse)
    # Verify checks a license token online: signature, schema, expiry, app_id and
    # the revocation list.
    # It is UNAUTHENTICATED and always answers 200 — a bad token is `valid:false`
    # with a reason rather than an error status, because "is this token good" is a
    # question anyone may ask about a credential they already hold and the answer is
    # the same either way. It is also OPTIONAL: the engine verifies OFFLINE against
    # the published public key (GET /v1/licensing/pubkey) and needs this endpoint
    # only to learn about revocation, so an outage here never stops a paid customer
    # working.
    post_licensing_verify(req: VerifyRequest) returns (rep: VerifyResponse)
}

# ---------------------------------------------------------------------
# 11 op(s) here. What follows is what this schema does not carry.
#
# opaque (6) — crosses, arrives without its name:
#   FingerprintRequest.Signals  licensing.DeviceSignals
#   IssueRequest.Signals  licensing.DeviceSignals
#   PubkeyView.Keys  licensing.JWK (list element)
#   ReleaseAsset.Release  licensing.Release
#   ReleaseList.Releases  licensing.Release (list element)
#   RevokeResponse.Entry  licensing.RevocationEntry
