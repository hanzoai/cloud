package integrations

// recaptcha.go registers Google reCAPTCHA as a per-user connector for the site's
// form/bot protection. The custodied credential is the reCAPTCHA SECRET key.
//
// siteverify answers HTTP 200 whether the secret is valid or not — the outcome is in
// the body's error-codes — so this keeps a bespoke Verify over the shared verifyPost
// transport (keyVerify is status-only). We probe with a deliberately-invalid response
// token: a VALID secret comes back complaining only about the response
// (invalid-input-response); an INVALID secret comes back with invalid-input-secret.
// Accept the secret iff the error is about the response, never the secret.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

func init() {
	register(&Provider{
		ID: "recaptcha", Name: "reCAPTCHA",
		Description: "Form and bot protection. Connect with your reCAPTCHA secret key.",
		Category:    "Security", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify:  recaptchaVerify,
	})
}

func recaptchaVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	secret := strings.TrimSpace(in.Token)
	if secret == "" {
		return nil, fmt.Errorf("empty credential")
	}
	base := envBase("RECAPTCHA_API_BASE")
	if base == "" {
		base = "https://www.google.com"
	}
	form := url.Values{"secret": {secret}, "response": {"hanzo-connector-verify"}}
	raw, err := verifyPost(ctx, "recaptcha", base+"/recaptcha/api/siteverify", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return nil, err
	}
	var r struct {
		Success    bool     `json:"success"`
		ErrorCodes []string `json:"error-codes"`
	}
	if jerr := json.Unmarshal(raw, &r); jerr != nil {
		return nil, fmt.Errorf("recaptcha verify: decode")
	}
	// A valid secret with our deliberately-invalid dummy response fails ONLY about the
	// response. Accept iff success (unexpected but valid) OR the complaint is the
	// response AND nothing else — an ALLOWLIST (fail-closed): an unknown code, an empty
	// error set, or any secret/key/request complaint means the key is NOT proven good.
	sawResponse := false
	for _, code := range r.ErrorCodes {
		switch strings.ToLower(strings.TrimSpace(code)) {
		case "invalid-input-response", "timeout-or-duplicate":
			sawResponse = true
		default: // invalid-input-secret / missing-input-secret / invalid-keys / bad-request / anything unknown
			return nil, fmt.Errorf("recaptcha rejected the secret key")
		}
	}
	if !r.Success && !sawResponse {
		return nil, fmt.Errorf("recaptcha did not confirm the secret key")
	}
	return &ExchangeResult{Tokens: map[string]string{apiKeySecret: secret}}, nil
}
