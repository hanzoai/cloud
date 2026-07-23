package idv

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestManualNeverAutoApproves is the load-bearing property: the default provider,
// which is what ships when no external provider is configured, can never produce a
// verified result on its own. Start yields pending; Check stays pending forever.
func TestManualNeverAutoApproves(t *testing.T) {
	m := Manual{}
	sess, err := m.Start(context.Background(), "acme", Subject{Kind: KindIndividual, Email: "f@acme.test", Ref: "u1"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.Status != StatusPending {
		t.Fatalf("manual Start status = %q, want pending", sess.Status)
	}
	if sess.Status.Terminal() {
		t.Fatalf("manual Start must not be terminal")
	}
	if sess.VerifyURL != "" {
		t.Fatalf("manual has no hosted URL, got %q", sess.VerifyURL)
	}
	res, err := m.Check(context.Background(), "acme", sess.Ref)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Status != StatusPending {
		t.Fatalf("manual Check status = %q, want pending (never self-verifies)", res.Status)
	}
}

// TestManualRequiresCorrelator: a manual verification with nothing to correlate the
// eventual decision to is rejected rather than silently opened.
func TestManualRequiresCorrelator(t *testing.T) {
	if _, err := (Manual{}).Start(context.Background(), "acme", Subject{Kind: KindIndividual}); err == nil {
		t.Fatalf("expected error starting manual verification with no email/ref")
	}
}

// TestClassifyFailClosed proves the pure decision core: only an explicitly tabled
// success token verifies; everything else — unknown, empty, a provider-internal
// working state — is pending.
func TestClassifyFailClosed(t *testing.T) {
	table := classifierFor("persona")
	cases := []struct {
		token string
		want  Status
	}{
		{"approved", StatusVerified},
		{"APPROVED", StatusVerified}, // case-insensitive
		{"declined", StatusRejected},
		{"needs_review", StatusReview},
		{"", StatusPending},           // empty → pending
		{"created", StatusPending},    // provider working state (not tabled) → pending
		{"processing", StatusPending}, // unknown → pending
		{"verified_maybe", StatusPending},
	}
	for _, c := range cases {
		if got := classify(table, c.token); got != c.want {
			t.Errorf("classify(%q) = %q, want %q", c.token, got, c.want)
		}
	}
}

// TestRESTFailsClosedOnTransportError: a provider 5xx is an error, never a verified
// result, and the error surfaces only the HTTP code — not the bearer token.
func TestRESTFailsClosedOnTransportError(t *testing.T) {
	const token = "super-secret-bearer-value"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	r := &REST{
		name: "persona", base: srv.URL,
		key:      func(context.Context) ([]byte, error) { return []byte(token), nil },
		classify: classifierFor("persona"),
		http:     srv.Client(),
	}
	_, err := r.Check(context.Background(), "acme", "inq_1")
	if err == nil {
		t.Fatalf("expected error on provider 5xx, got nil (a failure must never verify)")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("bearer token leaked into error: %v", err)
	}
}

// TestRESTKeyUnavailableFailsClosed: if the sealed key cannot be resolved, the call
// errors and never reaches the provider — no verification without a credential.
func TestRESTKeyUnavailableFailsClosed(t *testing.T) {
	r := &REST{
		name: "persona", base: "https://unused.example",
		key:      func(context.Context) ([]byte, error) { return nil, errors.New("kms down") },
		classify: classifierFor("persona"),
		http:     http.DefaultClient,
	}
	if _, err := r.Check(context.Background(), "acme", "inq_1"); err == nil {
		t.Fatalf("expected error when key is unavailable")
	}
}

// TestRESTStartHappyPath: a well-formed hosted inquiry returns the provider ref +
// hosted URL and a non-terminal status; a fresh inquiry is never verified even if
// the provider prematurely reports a terminal token.
func TestRESTStartHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Provider prematurely says "approved" on a brand-new inquiry — must be
		// downgraded to pending (a fresh inquiry cannot be a settled decision).
		_, _ = w.Write([]byte(`{"id":"inq_9","hosted_url":"https://verify.example/inq_9","status":"approved"}`))
	}))
	defer srv.Close()

	r := &REST{
		name: "persona", base: srv.URL,
		key:      func(context.Context) ([]byte, error) { return []byte("k"), nil },
		classify: classifierFor("persona"),
		http:     srv.Client(),
	}
	sess, err := r.Start(context.Background(), "acme", Subject{Kind: KindIndividual, Email: "f@acme.test", Ref: "u1"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.Ref != "inq_9" || sess.VerifyURL != "https://verify.example/inq_9" {
		t.Fatalf("unexpected session %+v", sess)
	}
	if sess.Status != StatusPending {
		t.Fatalf("fresh inquiry status = %q, want pending (never a premature terminal)", sess.Status)
	}
}

// TestRESTCheckMapsProviderDecision: the provider's settled decisions map through
// honestly — a real "verified/approved" verifies, a real "declined" rejects.
func TestRESTCheckMapsProviderDecision(t *testing.T) {
	var reply string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	r := &REST{
		name: "persona", base: srv.URL,
		key:      func(context.Context) ([]byte, error) { return []byte("k"), nil },
		classify: classifierFor("persona"),
		http:     srv.Client(),
	}
	reply = `{"id":"inq_1","status":"approved"}`
	if res, err := r.Check(context.Background(), "acme", "inq_1"); err != nil || res.Status != StatusVerified {
		t.Fatalf("approved → %v %q (want verified)", err, res.Status)
	}
	reply = `{"id":"inq_1","status":"declined"}`
	if res, err := r.Check(context.Background(), "acme", "inq_1"); err != nil || res.Status != StatusRejected {
		t.Fatalf("declined → %v %q (want rejected)", err, res.Status)
	}
}

// TestFromConfigSelection covers provider selection + the fail-closed rules.
func TestFromConfigSelection(t *testing.T) {
	okKey := func(context.Context, string) ([]byte, error) { return []byte("k"), nil }
	badKey := func(context.Context, string) ([]byte, error) { return nil, errors.New("no such secret") }
	env := func(m map[string]string) EnvFn { return func(k string) string { return m[k] } }

	// Unset → Manual, no error.
	p, err := FromConfig(okKey, env(map[string]string{}))
	if err != nil {
		t.Fatalf("unset: %v", err)
	}
	if _, ok := p.(Manual); !ok {
		t.Fatalf("unset provider = %T, want Manual", p)
	}

	// Explicit manual → Manual.
	p, _ = FromConfig(okKey, env(map[string]string{"CLOUD_IDV_PROVIDER": "manual"}))
	if _, ok := p.(Manual); !ok {
		t.Fatalf("manual provider = %T, want Manual", p)
	}

	// Unknown provider → error (no silent manual fallback for a typo).
	if _, err := FromConfig(okKey, env(map[string]string{"CLOUD_IDV_PROVIDER": "acme-kyc"})); err == nil {
		t.Fatalf("expected error for unknown provider")
	}

	// Real provider without a key ref → error.
	if _, err := FromConfig(okKey, env(map[string]string{"CLOUD_IDV_PROVIDER": "persona"})); err == nil {
		t.Fatalf("expected error for persona with no key ref")
	}

	// Real provider whose key does not resolve → error (fail-closed at mount).
	if _, err := FromConfig(badKey, env(map[string]string{"CLOUD_IDV_PROVIDER": "persona", "CLOUD_IDV_KEY_REF": "kms://kyc"})); err == nil {
		t.Fatalf("expected error when the provider key does not resolve")
	}

	// Real provider with a resolving key → REST.
	p, err = FromConfig(okKey, env(map[string]string{"CLOUD_IDV_PROVIDER": "persona", "CLOUD_IDV_KEY_REF": "kms://kyc"}))
	if err != nil {
		t.Fatalf("persona ok: %v", err)
	}
	if rp, ok := p.(*REST); !ok || rp.Name() != "persona" {
		t.Fatalf("configured provider = %T (%v), want *REST persona", p, p.Name())
	}
}
