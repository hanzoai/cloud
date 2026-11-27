// Package integrations is how your org connects third-party accounts like Slack, and
// revokes them.
//
// It is the generic, provider-agnostic OAuth connector plane for the unified Hanzo
// Cloud binary — the /v1/provider surface (Slack today; GitHub scaffolded;
// Google / Salesforce plug into the SAME registry later) — and it hands the
// resulting per-org tokens to KMS custody.
//
// ONE framework, N providers. A provider self-registers (its file's init calls
// register) into a package registry declaring how to build its authorize URL,
// exchange the code, and revoke. The five HTTP handlers here are provider-blind:
// they resolve the provider by :provider, apply the SAME org gate, CSRF/state,
// KMS custody and console redirect for every one. Adding a provider is a new file,
// never a new route.
//
// Surface (subsystem name "integrations", prefix /v1/provider):
//
//	GET    /v1/provider                      list providers + this org's status  -> {providers:[...]}
//	GET    /v1/provider/:provider            one provider (404 unknown id)        -> Provider
//	POST   /v1/provider/:provider/connect    begin OAuth (org-authed)             -> {authorizeUrl} | 503 | 403
//	GET    /v1/provider/:provider/callback   PUBLIC, state-authed                  -> 302 to console
//	POST   /v1/provider/:provider/disconnect revoke + forget (org-authed)         -> {disconnected:true}
//
// TENANT ISOLATION. connect/list/get/disconnect derive the org from
// principal.Org (a VALIDATED principal — a client-forged X-Org-Id with no
// bearer is refused 403). The callback is Slack/GitHub-initiated and therefore
// UNAUTHENTICATED, so its org is taken ONLY from the HMAC-signed, single-use
// state — never a header (see state.go). Every org that reaches KMS keys through
// kms.OrgPath, which derives its path segment rather than trusting it, so no org
// can smuggle path structure into a secret key.
//
// SECRET CUSTODY. Per-org customer tokens live ONLY in KMS (sealed, AES-256-GCM
// envelope), keyed /orgs/{org}/integrations/{provider}. The store holds only
// non-secret connection metadata (external id, account label, granted scopes).
// Provider APP creds (client id/secret) come from ENV, injected by the operator
// from KMS via KMSSecret — never plaintext in the store or a manifest. If KMS is
// not Ready the connect/callback flows fail closed (503 / failure redirect); a
// token is NEVER written in plaintext and NEVER put in SQLite.
package provider
