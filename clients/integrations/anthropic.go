package integrations

// anthropic.go registers the Anthropic USER connector (/v1/connectors plane):
// setup-token or API-key intake with a LIVE verify. Upstream acceptance of a
// setup token is offline-syntactic; the live GET /v1/models call here is a
// deliberate this-port addition required by the intake contract — ALWAYS
// verify-before-store. Both credential flavours share ONE KMS slot
// ("api_key"); the sk-ant-oat01- prefix decides the auth header at use time.
// Token credentials are not refreshable: ExpiresAt stays 0 and GET /:id/token
// serves the static slot.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode"
)

const (
	anthropicProvider = "anthropic"
	// anthropicKeySecret is the ONE slot both setup tokens (sk-ant-oat01-) and
	// API keys custody under; the prefix decides the auth header at use time.
	anthropicKeySecret   = "api_key"
	anthropicSetupPrefix = "sk-ant-oat01-"
	anthropicSetupMinLen = 80
	anthropicVersion     = "2023-06-01"
	anthropicOAuthBeta   = "oauth-2025-04-20"
)

// anthropicBase reads ANTHROPIC_API_BASE at call time (httptest seam),
// defaulting to the production API origin.
func anthropicBase() string {
	if v := strings.TrimSpace(os.Getenv("ANTHROPIC_API_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.anthropic.com"
}

func init() {
	register(&Provider{
		ID:          anthropicProvider,
		Name:        "Anthropic",
		Description: "Claude via setup token or API key.",
		Category:    "AI",
		Scope:       userScope,
		Secrets:     []string{anthropicKeySecret},
		Verify:      anthropicVerify,
	})
}

// anthropicVerify validates a setup token or API key LIVE against /v1/models
// and returns it for custody. FAIL CLOSED throughout: any non-200 stores
// nothing, and no error ever contains the credential value.
func anthropicVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	// Strip ALL whitespace (setup tokens are often pasted with line wraps) —
	// upstream normalizeAnthropicSetupTokenInput parity.
	tok := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, in.Token)
	if tok == "" {
		return nil, fmt.Errorf("empty credential")
	}
	setup := strings.HasPrefix(tok, anthropicSetupPrefix)
	if setup && len(tok) < anthropicSetupMinLen {
		// OFFLINE reject: a syntactically impossible setup token never reaches the
		// network (tests assert zero requests).
		return nil, fmt.Errorf("setup token too short")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anthropicBase()+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("anthropic verify: build request: %w", err)
	}
	if setup {
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("anthropic-beta", anthropicOAuthBeta)
	} else {
		req.Header.Set("x-api-key", tok)
	}
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Accept", "application/json")
	resp, err := authClient.Do(req)
	if err != nil {
		// Deliberately unwrapped: a transport error can echo the URL but never a
		// header; the message stays token-free regardless.
		return nil, fmt.Errorf("anthropic verify request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain (bounded) so the connection can be reused; the content is unused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("anthropic rejected the credential (%d)", resp.StatusCode)
	default:
		return nil, fmt.Errorf("anthropic verify failed (%d)", resp.StatusCode)
	}
	return &ExchangeResult{
		Tokens: map[string]string{anthropicKeySecret: tok},
		// /v1/models discloses no account identity; the caller hint is the only
		// source and "" is acceptable on the user plane (no ResolveOrgByExternalID
		// routing here).
		ExternalID: strings.TrimSpace(in.AccountID),
	}, nil
}
