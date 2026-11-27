# LLM.md — apps/iam

Guidance for apps/iam. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Where identity is stored, and how it moves

`IAM_STORE_BACKEND` names it: empty is the encrypted file `{DataDir}/iam/iam.db`;
`sql` is hanzo-sql over ZAP at `IAM_SQL_ADDR` (`sql://sql.hanzo.svc.cluster.local:9651`,
the primary's ZAP listener; `hanzo-sql` has no 9651). Only a shared backend lets cloud
run more than one replica (`Config.Validate`).

## IAM's mail rides notify as HTML with its text beside it

IAM words every code (hanzoai/iam `internal/otp`); `apps/iam/sender.go` carries
it to notify over the plane (`client.NotifySend`). A message IAM designed for an
email client (`Message.HTML`, the sign-in mail with its link and code) goes as
notify's `Body` with IAM's text as `Text`, the plain-text alternative; every
other message is its text alone, as before. notify's `sendReal` hands `Text` to
any email provider that carries one (`alternative`: hanzoai/notify `mail` and
`twilioemail` ≥ v1.7.3, asserted at compile time in `mail.go` and `twilio.go`).
hanzo's email goes through `twilio_email` (KMS `orgs/hanzo/notify/twilio_email`),
whose API takes `content.html` and `content.text`. A `Body` is otherwise sent as
HTML by both email providers, so a caller with plain text and no `Text` gets its
line breaks collapsed.

## An organization's signing keys are sealed in KMS under the org (iam v1.34.193)
An org-owned application signs with an org-owned cert (`Scope` "tenant"), under the org's
own issuer `<IAM_TENANT_ISSUER>/<org>`, with discovery and a key set only at that issuer
(`/<org>/.well-known/openid-configuration`, `/<org>/.well-known/jwks`). IAM generates the
cert's RSA key; `vault.go` is the `iamserver.Vault` that keeps the private half, sealed
in KMS at `orgs/<org>/iam/signing/<cert name>` for the org the ref names, in the `iam`
custody subtree (`apps/kms/custody.go`), so the org's own people never read it. A key is
never replaced (Put refuses a held ref); a rotation is a new cert under a new name. The
platform boundary trusts the platform issuers and the platform JWKS only, so a tenant
token authenticates nothing here (`sdk/auth_tenant_cert_test.go`). The root `/:org`
prefixes in `manifest/apps.go` are bounded by the literal `.well-known` that follows them,
and a reserved slug (iam `pkg/schema/slug.go`) can never be an org. `IAM_TENANT_ISSUER`
unset: no tenant cert signs and those paths answer 404.
