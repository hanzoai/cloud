// Copyright © 2026 Hanzo AI. MIT License.

package kms

import "strings"

// custody.go names what this deployment seals on an org's behalf.
//
// An app that holds a credential FOR an org — the MCP server it relays to, a
// repository's environment secret, an OAuth token, a wallet's signing key, a
// dedicated database's admin password — seals it under that org's root and reads
// it back itself, over the internal plane, at the moment it acts. The org's
// people never read it: the app that set it answers them a mask, a name, or
// nothing. That subtree is CUSTODIED, and the app is its custodian.
//
// The REST surface addresses the same store under the same root, so without this
// rule a member reads the credential the custodian holds, and an org admin
// replaces or deletes it: the grant declaration measures (`enforce: false`), every
// value it does not cover opens as a would-deny, and a write asks only for org
// admin. A custodied value is read, written and deleted over REST exactly as it
// would be with the declaration enforcing — by SuperAdmin holding an access token
// (audited), or a holder a declared grant covers — and by nobody else, whatever
// the declaration says about enforcement. A person gets nothing from it on
// membership or org admin, and the custodian never asks through REST at all. An
// org sets what it hands a custodian through the custodian's own admin-gated
// operation (notify's PUT /v1/notify/credentials/:provider/:key, environment's
// PUT /v1/environment/:repo/secrets/:name), never through the store's.
//
// Two subtrees are also read by the org's OWN deployments, because that is what
// they are sealed for: a platform app's secret environment and a validator's
// staking material, which the KMS operator projects into `tenant-<org>` logged
// in as the org's sync identity (`<org>-platform-kms`), and which a pod there
// may read on the plane. The sync identity reads them over REST and writes
// nothing there. They are `sync`, and only what lies BENEATH their root is
// custodied: an app's or a validator's own folder. A key at the root itself is
// not the custodian's — the deployment keeps its own there — and is answered as
// any hand-kept value is, so no app may seal one there:
// TestNoAppSealsAtASyncSubtreesRoot refuses any spelling of a sync subtree that
// reaches fewer than two segments beneath it.
//
// THE SET LIVES HERE, in the one process that answers every read, rather than
// being registered by the owning apps: an app registering at init would register
// in its own process, and the store would never hear of it.
// TestEveryOrgSubtreeAnAppSealsIsCustodied holds it to the refs the apps build.
var custodied = map[string]holding{
	// apps/admin (provider_keys.go): per-org and per-user AI provider API key overrides
	// (OpenRouter, NVIDIA, etc.), sealed and read back by admin/ai on the plane.
	"ai-keys": {app: "admin"},
	// apps/books (finance app): an org's Plaid item index and each item's access token.
	"books": {app: "finance"},
	// internal/host: the deployment's Web Bot Auth key set under the brand's org,
	// read by every process whose agents fetch the web and published (public
	// halves only) by crawl at /.well-known/http-message-signatures-directory.
	"botauth": {app: "crawl"},
	// apps/destination: a publishing destination's OAuth tokens.
	"destinations": {app: "destination"},
	// plugin/licensing: the engine license signing seed, read for each signature.
	"engine": {app: "licensing"},
	// apps/email: a delivery rail's credentials (SES keys and topic, SendGrid key
	// and webhook public key), read at orgs/<org>/email/<provider>/<key> when a
	// message leaves or a signed webhook arrives (credentials.go).
	"email": {app: "email"},
	// apps/environment: a repository's environment secrets, read by a coding run.
	"environment": {app: "environment"},
	// apps/form: an org's form signing secret, sealed and read by the form plane.
	"form": {app: "form"},
	// apps/iam: an organization's signing keys, one PEM per cert, which IAM
	// generates and reads back to sign (vault.go). The org's people never read them.
	"iam": {app: "iam"},
	// apps/provider: an org's connected integrations and their tokens.
	"integrations": {app: "provider"},
	// apps/platform: the org's KMS-sync identity's own client credential.
	"kms-auth": {app: "platform"},
	// apps/admin (linked_compute.go): per-org and per-user linked compute endpoints
	// and their bearer tokens, sealed and read back by admin/ai on the plane.
	"linked-compute": {app: "admin"},
	// apps/marketing: the org's own SMTP mailbox its drip email leaves through
	// (mailbox.go), set by SuperAdmin at marketing/mail.
	"marketing": {app: "marketing"},
	// apps/notify: a delivery provider's credentials, which an org admin sets
	// through notify's own operation (credential.go).
	"notify": {app: "notify"},
	// apps/platform: a deployed app's secret environment, projected into the
	// org's namespace by the KMS operator as the org's sync identity.
	"platform": {app: "platform", sync: true},
	// apps/link: a connected account's sealed credential.
	"providers": {app: "link"},
	// apps/settings: a product's secret settings, masked on every read.
	"settings": {app: "settings"},
	// apps/tools: an org MCP server's credential, read by the tool relay.
	"tools-mcp": {app: "tools"},
	// apps/social and apps/provider: an org's own app keys for each network it
	// posts to (orgs/<org>/social/apps/<provider>), set by an org admin.
	"social": {app: "social"},
	// apps/provider: a member's own connector tokens.
	"users": {app: "provider"},
	// apps/validator: a validator's staking material, projected like platform's.
	"validators": {app: "validator", sync: true},
	// apps/wallet: a custodied wallet's signing key.
	"wallets": {app: "wallet"},
	// apps/websearch: the platform's search-engine API keys (MOJEEK_KEY,
	// BRAVE_KEY) under org admin, set through PUT /v1/websearch/engines/:name/key.
	"websearch": {app: "websearch"},
	// apps/webhook: an org's outbound webhook signing secrets.
	"webhooks": {app: "webhook"},
	// apps/provisioning: a provisioned resource's admin password, one subtree
	// per kind (orgs/<org>/<kind>/<name>).
	"datastore": {app: "provisioning"},
	"docdb":     {app: "provisioning"},
	"kv":        {app: "provisioning"},
	"s3":        {app: "provisioning"},
	"search":    {app: "provisioning"},
	"sql":       {app: "provisioning"},
	"vector":    {app: "provisioning"},
}

// holding is the app that holds a custodied subtree, and whether the org's own
// deployments read it (see sync above).
type holding struct {
	app  string
	sync bool
}

// custodyOf answers the custodian of a subpath beneath an org's root ("" or
// "/" is the root itself), and whether the subpath is custodied.
func custodyOf(sub string) (holding, bool) {
	root, rest, _ := strings.Cut(strings.Trim(normalizePath(sub), "/"), "/")
	h, ok := custodied[root]
	return h, ok && (rest != "" || !h.sync)
}

// custodyOfRef is custodyOf for a flat plane ref: the subpath is what follows
// "orgs/<org>/" in the ref's path, and a ref that names no tenant is the
// deployment's, which no tenant reaches at all (admit).
func custodyOfRef(ref string) (holding, bool) {
	path, _, _ := parseRef(ref)
	rest, ok := strings.CutPrefix(strings.Trim(path, "/"), "orgs/")
	if !ok {
		return holding{}, false
	}
	_, sub, _ := strings.Cut(rest, "/")
	return custodyOf(sub)
}

// Custodied reports whether a flat ref ("orgs/<org>/…[@env]", the KMSClient
// grammar) lies in a subtree this deployment seals on the org's behalf. An
// owning app's test asks it of the refs the app builds.
func Custodied(ref string) bool {
	_, held := custodyOfRef(ref)
	return held
}
