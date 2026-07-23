package idv

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// webhook.go is the ONE external ingestion path for a provider-signaled result. A
// real IdV provider (or a Hanzo relay in front of it) cannot present a
// gateway-minted, validated X-Org-Id, so it authenticates by SIGNATURE, not by an
// internal principal: an HMAC-SHA256 over the exact request body under a KMS-sealed
// secret. This is the SAME fail-closed idiom the git/GitHub and Slack webhooks use.
//
// The webhook is DISABLED unless CLOUD_IDV_WEBHOOK_KEY_REF names a resolvable KMS
// secret, so the default (Manual) deployment exposes no signature-authenticated
// terminal-status path at all. Even when enabled, the receiver treats the payload as
// a NOTIFICATION and identifies WHICH verification to reconcile by its provider
// reference — the settled status is then read from the wired provider's API
// (Provider.Check), never trusted from the webhook body. So a valid signature over a
// body that merely claims "verified" cannot force a verified record: the provider is
// the single source of truth, and the Manual provider's Check stays pending.

// WebhookRefHeader carries the payload signature: "sha256=<hex(HMAC_SHA256(secret,body))>".
const WebhookRefHeader = "X-Idv-Signature"

// Webhook authenticates a provider webhook and extracts the verification reference to
// reconcile. It holds only a resolver for the KMS-sealed secret (fetched per call,
// short-lived in memory, never logged) — mirroring how REST custodies its bearer.
type Webhook struct {
	secret func(ctx context.Context) ([]byte, error)
}

// WebhookFromConfig returns the configured webhook receiver, or nil when none is
// configured (the default — the endpoint is then not served). It is FAIL-CLOSED at
// mount: a named secret reference that does not resolve is an error the caller
// propagates to fail the mount, exactly like a named provider whose key is missing —
// never a silent downgrade to an unauthenticated endpoint.
//
// Configuration (env, resolved by the composition root):
//
//	CLOUD_IDV_WEBHOOK_KEY_REF  KMS secret reference for the webhook HMAC secret. Unset
//	                           ⟹ the webhook is disabled (Manual deployments).
func WebhookFromConfig(getSecret SecretFn, env EnvFn) (*Webhook, error) {
	ref := strings.TrimSpace(env("CLOUD_IDV_WEBHOOK_KEY_REF"))
	if ref == "" {
		return nil, nil // disabled: no external signature-authenticated path
	}
	if getSecret == nil {
		return nil, fmt.Errorf("idv: webhook requires KMS but none is available")
	}
	// Prove the secret resolves now, so a misconfigured webhook is caught at boot.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := getSecret(ctx, ref); err != nil {
		return nil, fmt.Errorf("idv: webhook key ref %q did not resolve: %w", ref, err)
	}
	return &Webhook{secret: func(ctx context.Context) ([]byte, error) { return getSecret(ctx, ref) }}, nil
}

// Verify reports whether sigHeader is a valid signature over body under the sealed
// secret. Fail-closed: an unresolvable secret is an error; an empty/malformed header,
// or any mismatch, is a plain false (never a panic, never a partial-credit accept).
// Constant-time compare.
func (w *Webhook) Verify(ctx context.Context, sigHeader string, body []byte) (bool, error) {
	secret, err := w.secret(ctx)
	if err != nil {
		return false, fmt.Errorf("idv: webhook secret unavailable: %w", err)
	}
	if len(secret) == 0 {
		return false, fmt.Errorf("idv: webhook secret is empty")
	}
	const prefix = "sha256="
	sig := strings.TrimSpace(sigHeader)
	if !strings.HasPrefix(sig, prefix) {
		return false, nil
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := prefix + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(want)), nil
}

// Reference extracts the provider reference the (verified) payload names — WHICH
// verification to reconcile. The receiver reconciles that reference against the
// provider API for the authoritative status; the body carries no trusted decision, so
// this reads only the correlating reference. A relay in front of a specific provider
// maps that provider's webhook shape onto this minimal envelope.
func (w *Webhook) Reference(body []byte) (string, bool) {
	var env struct {
		Reference string `json:"reference"`
		Ref       string `json:"ref"`
		ID        string `json:"id"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", false
	}
	for _, r := range []string{env.Reference, env.Ref, env.ID} {
		if r = strings.TrimSpace(r); r != "" {
			return r, true
		}
	}
	return "", false
}
