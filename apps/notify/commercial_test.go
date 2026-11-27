package notify

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
)

// Transactional mail rides any provider the org holds. The fence is the other way
// round: commercial mail never rides notify (apps/email and apps/marketing send it
// by SES or SendGrid). An org whose only email credential is SES or SendGrid still
// gets its sign-in codes and receipts.

// An org with SES alone is given SES for a transactional send, on the routes and
// on the plane op IAM's codes take; one with Twilio Email too is given Twilio
// first, its rank.
func TestATransactionalSendRidesTheOrgsOnlyProvider(t *testing.T) {
	ses := fakeKMS{m: map[string]string{
		"orgs/acme/notify/ses/access-key-id":     "AKIA",
		"orgs/acme/notify/ses/secret-access-key": "s",
		"orgs/acme/notify/ses/from-email":        "team@acme.io",
	}}
	s := &service{kms: ses}
	if got, err := s.pick(t.Context(), "acme", "email"); err != nil || got != "ses" {
		t.Errorf("an org with SES alone: %q %v, want ses", got, err)
	}
	for k, v := range map[string]string{"account-sid": "AC1", "auth-token": "t", "from-email": "team@acme.io"} {
		ses.m["orgs/acme/notify/twilio_email/"+k] = v
	}
	if got, err := s.pick(t.Context(), "acme", "email"); err != nil || got != "twilio_email" {
		t.Errorf("an org with both: %q %v, want twilio_email first", got, err)
	}
}

// A send route may name SES or SendGrid: a receipt an org sends through its own
// SendGrid account is transactional all the same.
func TestTheSendRoutesTakeAnyProvider(t *testing.T) {
	var sent atomic.Int32
	var rail atomic.Value
	app := sendApp(t, func(_ context.Context, _, _, provider string, _ []string, _, _ string) (string, error) {
		sent.Add(1)
		rail.Store(provider)
		return provider, nil
	})
	for _, p := range []string{"ses", "sendgrid"} {
		code, raw := post(t, app, "/v1/notify/send/email?sync=true", "acme", "u_acme", map[string]any{
			"to": []string{"ada@acme.io"}, "provider": p, "subject": "Your receipt", "body": "Paid.",
		})
		if code != http.StatusOK || rail.Load() != p {
			t.Errorf("pinned %s: %d %s", p, code, raw)
		}
	}
	if sent.Load() != 2 {
		t.Errorf("%d sends, want 2", sent.Load())
	}
}

// The plane op IAM's codes take finds the org's only provider too; with none it
// refuses rather than sending nothing silently.
func TestThePlaneOpFindsTheOrgsOnlyProvider(t *testing.T) {
	s := &service{kms: fakeKMS{m: map[string]string{}}}
	if _, err := send(plane.For(t.Context(), "acme"), s, &client.Send{Org: "acme", Channel: "email", To: "ada@acme.io", Subject: "Your code", Body: "123456"}); err == nil {
		t.Error("an org with no provider was answered as sent")
	}
}
