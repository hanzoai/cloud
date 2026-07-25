package integrations

// chrome.go registers Chrome as an ORG apikey connector for the /connectors
// surface (the org-scoped /v1/integrations plane both hanzo.app and the console
// render). It represents pairing the Hanzo browser extension — a LOCAL install
// (source ~/work/hanzo/extension) that drives Chrome over an MCP bridge so agent
// tasks can navigate, read pages, and fill forms from the user's browser.
//
// HONEST MODEL — no third-party OAuth. The extension is a local install; there is
// NO external identity provider to run a 3-legged OAuth against, so this is NOT an
// oauth connector and does NOT invent a fake external OAuth app. Instead it is the
// framework's apikey shape with a LOCAL PAIRING credential: the user installs the
// extension (hanzo.ai/install), signs into their Hanzo account inside it (the
// extension already does IAM PKCE — see extension/packages/browser/src/shared/
// auth.ts), and pairs it here with the device-pairing token the extension exposes.
// That token is the credential — sealed into the org's KMS namespace under api_key
// (integrations.TokenFor(org, "chrome", "api_key")) so the agent plane can
// authenticate browser-drive requests as this org's paired browser.
//
// VERIFICATION is STRUCTURAL, not a live remote call: the extension bridge is
// bound to localhost and is unreachable from cloud, so there is nothing honest to
// ping. chromeVerify therefore validates the pairing token OFFLINE (non-empty,
// length + charset of a real device/PKCE token) and fails closed on anything
// malformed — a random paste stores nothing. It is token-free by construction (no
// error ever carries the credential value) and seal-before-row like every other
// connector. Pairing a browser-automation credential org-wide is a sensitive grant,
// so it is an org-admin action (AdminOnly), parity with the other org apikey
// connectors (cloudflare/warpcast/whatsapp).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// chromeProvider is the :provider slug.
	chromeProvider = "chrome"
	// chromePairMinLen is the OFFLINE floor for a pairing token. A real Hanzo
	// device/PKCE token is well over this; a stray paste that is too short never
	// seals. It is a reject threshold only — never an upper bound (the framework
	// caps size at maxCredentialLen before Verify runs).
	chromePairMinLen = 16
)

// chromeScopes is the browser-automation capability set the paired extension
// grants — display + connection metadata (not read back from any remote, there is
// none). It mirrors the extension's MCP browser actions (background.ts).
var chromeScopes = []string{"browser:navigate", "browser:read", "browser:control", "browser:automate"}

func init() {
	register(&Provider{
		ID:          chromeProvider,
		Name:        "Chrome",
		Description: "Automate and control Chrome with the Hanzo browser extension — navigate, read pages, fill forms, and run agent tasks from your browser. Install at hanzo.ai/install and pair with a device token.",
		Category:    "Developer",
		Kind:        apiKeyKind,
		AdminOnly:   true,
		Scopes:      chromeScopes,
		Secrets:     []string{apiKeySecret},
		// apikey-only: the local-pairing path is ALWAYS available (customer-held
		// device token) and there is no OAuth leg. Mount asserts an org provider
		// declares both Configured and Creds; Creds is an empty config (no OAuth app).
		Configured: func() bool { return true },
		Creds:      func() OAuthConfig { return OAuthConfig{} },
		Verify:     chromeVerify,
	})
}

// chromeVerify validates a Hanzo browser-extension pairing token OFFLINE and
// returns it to seal plus non-secret pairing metadata. It FAILS CLOSED — an empty,
// too-short, or malformed token yields an error so connect stores nothing — and the
// returned error NEVER contains the token value (only its shape/reason). There is
// no remote call: the extension's MCP bridge is local and unreachable from cloud,
// so verification is the structural check appropriate to a local install.
func chromeVerify(_ context.Context, in VerifyInput) (*ExchangeResult, error) {
	token := strings.TrimSpace(in.Token)
	if token == "" {
		return nil, fmt.Errorf("empty pairing token")
	}
	if len(token) < chromePairMinLen {
		return nil, fmt.Errorf("chrome pairing token is too short")
	}
	// A real device/PKCE token is base64url/JWT-shaped: [A-Za-z0-9._-]. Reject
	// whitespace, control bytes, or anything else so a prose paste never seals.
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return nil, fmt.Errorf("chrome pairing token has an unexpected format")
		}
	}
	return &ExchangeResult{
		Tokens:       map[string]string{apiKeySecret: token},
		ExternalID:   chromeDeviceID(token),
		AccountLabel: "Hanzo Browser Extension",
		Scopes:       chromeScopes,
	}, nil
}

// chromeDeviceID derives a stable, non-secret device identifier from the pairing
// token — a SHA-256 fingerprint, never the token itself — so the connection row and
// the console card can name the paired browser without ever storing the secret in
// metadata.
func chromeDeviceID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "chrome-" + hex.EncodeToString(sum[:6])
}
