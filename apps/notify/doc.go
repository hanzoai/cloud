// Package notify is transactional email and SMS, sent through your org's own
// provider credential.
//
// POST /v1/notify/send delivers one message by email or SMS through the caller
// org's OWN KMS-held credential, and notify.Send (send.go) is that same rail for
// in-process callers — one sender, never a second provider path.
//
// It is the native replacement for the standalone notifyd
// (github.com/hanzoai/notify) Deployment.
//
// SCOPE — the one live contract. notifyd's ONLY production consumer is Hanzo IAM,
// which POSTs OTP sends to POST /v1/notify/send?sync=true with the wire body
//
//	{"to":["…"],"channel":"sms|email","event":"iam.otp_sent",
//	 "template_vars":{"otp":"…","recipient":"…","app":"…"}}
//
// and treats a response status of "sent"/"delivered" as success (see
// hanzoai/iam object/notify_delivery_http.go). This subsystem serves that exact
// contract natively. Everything else notifyd carries — the tenants/templates/
// providers/preferences/unsubscribe/metering/events/messages collections and the
// hanzoai/tasks (Temporal) async worker — has NO live consumer (the live tenant's
// template/provider/event tables are empty and only IAM calls /send), so it is
// deliberately NOT folded. The Temporal notify-send queue plane is owned elsewhere
// and is not touched here; async sends (no ?sync=true) return 503, exactly as
// notifyd does when it runs without a connected worker.
//
// DRY — no reimplementation of provider plumbing. The actual provider
// implementations and the wire structs are notifyd's OWN public packages, imported
// directly: github.com/hanzoai/notify/service/{twilio,twilioemail,plivo,mail} and
// github.com/hanzoai/notify/pkg/types. Only the credential-to-client mapping is
// ours, and it is DECLARED rather than switched: provider.go holds the contract and
// the registry, and each vendor is one file beside it (twilio.go, plivo.go,
// mail.go). WHICH providers a deployment can reach is therefore the set of files it
// links, not a case list three functions have to agree about — a new provider is a
// new file, never a change to the send path.
//
// SECURITY — the trust boundary moves with the code. notifyd was ClusterIP-internal
// and trusted a raw X-Org-Id header. Mounted here, /v1/notify/send is reachable via
// the public gateway (api.hanzo.ai forwards every path to cloud), so it gates on a
// VALIDATED principal and derives the org from principal.Org (the identity middleware's
// trusted, gateway-minted X-Org-Id), never from a client-supplied header. An
// unauthenticated caller gets 401; a signed-in caller can only send scoped to their
// OWN org.
//
// CREDENTIALS — KMS only, never env, never plaintext, never logged. Each provider
// DECLARES the keys it reads (provider.Keys) and the subset it cannot deliver
// without (provider.Needs); credentials are read EXCLUSIVELY from cloud's embedded
// KMS via cloud.Deps.KMS,
// at the org-scoped, rotatable ref orgs/<org>/notify/<service>/<key>. KMS keeps
// that subtree on the org's behalf, so an org admin sets and rotates a credential
// through PUT /v1/notify/credentials/:provider/:key (credential.go) — no
// operator-injected env Secret, no restart to rotate — and the KMS REST surface
// neither answers nor replaces it. The org is the VALIDATED principal's tenant
// (never a client header). A missing key yields an empty value and the send
// fails closed; no secret is ever hard-coded, read from the environment, or
// logged.
package notify
