package integrations

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newRecaptchaMock stands in for siteverify: it always answers 200, echoing a chosen
// success flag + error-codes (siteverify's real behavior — the outcome is in the body,
// not the status). It records the posted form so a test can prove the secret rode the
// body, never the URL.
func newRecaptchaMock(t *testing.T, success bool, codes string) *string {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		body = string(b)
		s := "false"
		if success {
			s = "true"
		}
		_, _ = w.Write([]byte(`{"success":` + s + `,"error-codes":[` + codes + `]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("RECAPTCHA_API_BASE", srv.URL)
	return &body
}

// TestRecaptchaAcceptsValidSecret: a valid secret with our deliberately-invalid
// response token comes back complaining ONLY about the response — the secret is good.
func TestRecaptchaAcceptsValidSecret(t *testing.T) {
	body := newRecaptchaMock(t, false, `"invalid-input-response"`)
	res, err := recaptchaVerify(context.Background(), VerifyInput{Token: sentinel})
	if err != nil {
		t.Fatalf("a valid secret (response-only error) must verify: %v", err)
	}
	if res.Tokens[apiKeySecret] != sentinel {
		t.Errorf("must seal the secret, got %q", res.Tokens[apiKeySecret])
	}
	if !strings.Contains(*body, "secret=") || strings.Contains(*body, "?secret=") {
		t.Errorf("secret must ride the POST body: %q", *body)
	}
}

// TestRecaptchaRejectsInvalidSecret: invalid-input-secret means the KEY is bad.
func TestRecaptchaRejectsInvalidSecret(t *testing.T) {
	newRecaptchaMock(t, false, `"invalid-input-secret"`)
	if res, err := recaptchaVerify(context.Background(), VerifyInput{Token: sentinel}); err == nil || res != nil {
		t.Fatalf("invalid-input-secret must fail closed, got res=%v err=%v", res, err)
	}
}

// TestRecaptchaRejectsMissingSecret: missing-input-secret also means the key is bad.
func TestRecaptchaRejectsMissingSecret(t *testing.T) {
	newRecaptchaMock(t, false, `"missing-input-secret"`)
	if _, err := recaptchaVerify(context.Background(), VerifyInput{Token: sentinel}); err == nil {
		t.Fatal("missing-input-secret must fail closed")
	}
}

// TestRecaptchaAcceptsSuccess: a bare success:true (no error-codes) also verifies.
func TestRecaptchaAcceptsSuccess(t *testing.T) {
	newRecaptchaMock(t, true, "")
	if _, err := recaptchaVerify(context.Background(), VerifyInput{Token: sentinel}); err != nil {
		t.Fatalf("success:true must verify: %v", err)
	}
}

// TestRecaptchaTokenFree: neither an accept nor a reject path may echo the secret.
func TestRecaptchaTokenFree(t *testing.T) {
	newRecaptchaMock(t, false, `"invalid-input-secret"`)
	_, err := recaptchaVerify(context.Background(), VerifyInput{Token: sentinel})
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("reject error must be present and secret-free: %v", err)
	}
	if _, err := recaptchaVerify(context.Background(), VerifyInput{Token: "   "}); err == nil {
		t.Fatal("empty secret must reject")
	}
}
