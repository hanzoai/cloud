// Package share is a public URL for a service on your own machine, and a list of
// what you have open.
//
// It fronts the zrok controller so a Hanzo org can publish a local service to a
// public https://share-<token>.hanzo.app URL — "ngrok on our own stack" — folded
// into the ONE cloud binary. The heavy data plane (the ziti fabric + the public
// frontend proxy) stays a separate runtime by design; this subsystem is the thin,
// org-scoped CONTROL surface: it provisions a per-org zrok account from the
// caller's validated IAM identity and hands the CLI the credential it needs, so
// `hanzo share 3000` needs zero manual setup.
//
// STATELESS provisioning. There is no local store: the per-org account is keyed
// deterministically off the org slug (email share+<org>@hanzo.ai, password =
// HMAC(SHARE_ACCOUNT_SECRET, org)), so ensure-account + login reconstruct the
// same credential every time from the zrok controller — the controller IS the
// store. Idempotent: creating an existing account is ignored; login always
// returns the current account token.
//
// FAIL-CLOSED. Absent the admin credential (ZROK_ADMIN_TOKEN, KMS-injected)
// every op returns an honest 503; it never fabricates a share or token.
package share
