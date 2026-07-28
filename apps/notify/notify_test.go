package notify

import (
	"context"
	"errors"
	"strings"
	"testing"

	ntypes "github.com/hanzoai/notify/pkg/types"
)

// fakeKMS is a cloud.KMSClient backed by a ref→value map. It models cloud's
// embedded KMS: creds() reads orgs/<org>/notify/<svc>/<key>. Any unseeded ref
// returns ErrSecretNotFound so a partially-configured provider fails closed.
type fakeKMS struct{ m map[string]string }

func (f fakeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	if v, ok := f.m[ref]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("secret not found")
}
func (f fakeKMS) PutSecret(context.Context, string, []byte) error { return nil }
func (f fakeKMS) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("sign unsupported")
}

func TestRenderOTPEmail(t *testing.T) {
	subject, body, err := render(&ntypes.SendRequest{
		Channel:      ntypes.ChannelEmail,
		Event:        "iam.otp_sent",
		TemplateVars: map[string]any{"otp": "483920", "app": "Hanzo Console"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(subject, "Hanzo Console") {
		t.Errorf("subject missing app: %q", subject)
	}
	if !strings.Contains(body, "483920") {
		t.Errorf("body missing otp: %q", body)
	}
}

func TestRenderOTPSMS(t *testing.T) {
	subject, body, err := render(&ntypes.SendRequest{
		Channel:      ntypes.ChannelSMS,
		Event:        "iam.otp_sent",
		TemplateVars: map[string]any{"otp": "119284", "app": "Hanzo"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if subject != "" {
		t.Errorf("sms should have no subject, got %q", subject)
	}
	if !strings.Contains(body, "119284") {
		t.Errorf("body missing otp: %q", body)
	}
}

func TestRenderDefaultsAppWhenMissing(t *testing.T) {
	_, body, err := render(&ntypes.SendRequest{
		Channel:      ntypes.ChannelSMS,
		Event:        "iam.otp_sent",
		TemplateVars: map[string]any{"otp": "000111"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(body, "Hanzo") {
		t.Errorf("body should default app to Hanzo: %q", body)
	}
}

func TestRenderRawBodyVerbatim(t *testing.T) {
	subject, body, err := render(&ntypes.SendRequest{
		Channel: ntypes.ChannelEmail,
		Subject: "Raw subject",
		Body:    "Raw body wins",
		Event:   "iam.otp_sent", // ignored because Body is set
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if subject != "Raw subject" || body != "Raw body wins" {
		t.Errorf("raw body/subject not verbatim: %q / %q", subject, body)
	}
}

func TestRenderMissingBodyAndTemplate(t *testing.T) {
	if _, _, err := render(&ntypes.SendRequest{Channel: ntypes.ChannelSMS}); err == nil {
		t.Fatal("expected error when neither body nor template/event is set")
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	if _, _, err := render(&ntypes.SendRequest{
		Channel: ntypes.ChannelEmail,
		Event:   "does.not.exist",
	}); err == nil {
		t.Fatal("expected error for unknown template id")
	}
}

func TestDefaultProviderSMSTwilio(t *testing.T) {
	// Twilio creds live ONLY in KMS at orgs/<org>/notify/twilio/<key>.
	s := &service{kms: fakeKMS{m: map[string]string{
		"orgs/hanzo/notify/twilio/account-sid": "ACxxxx",
		"orgs/hanzo/notify/twilio/auth-token":  "tok",
		"orgs/hanzo/notify/twilio/from-number": "+15551230000",
	}}}
	got, err := s.defaultProvider(t.Context(), "hanzo", "sms")
	if err != nil {
		t.Fatalf("defaultProvider: %v", err)
	}
	if got != "twilio" {
		t.Errorf("want twilio, got %q", got)
	}
}

func TestDefaultProviderScopedPerOrg(t *testing.T) {
	// Creds are org-scoped: a token for org "hanzo" must NOT read another org's
	// twilio creds. Seeding only "lux" leaves "hanzo" unconfigured.
	s := &service{kms: fakeKMS{m: map[string]string{
		"orgs/lux/notify/twilio/account-sid": "ACxxxx",
		"orgs/lux/notify/twilio/auth-token":  "tok",
		"orgs/lux/notify/twilio/from-number": "+15551230000",
	}}}
	if _, err := s.defaultProvider(t.Context(), "hanzo", "sms"); err == nil {
		t.Fatal("expected error: hanzo has no twilio creds; lux's must not leak")
	}
	if got, err := s.defaultProvider(t.Context(), "lux", "sms"); err != nil || got != "twilio" {
		t.Fatalf("want twilio for lux, got %q err=%v", got, err)
	}
}

func TestDefaultProviderSMSNoneConfigured(t *testing.T) {
	// Empty KMS → no provider → fail closed. Also covers the nil-store case.
	s := &service{kms: fakeKMS{m: map[string]string{}}}
	if _, err := s.defaultProvider(t.Context(), "hanzo", "sms"); err == nil {
		t.Fatal("expected error when no SMS provider is configured")
	}
	if _, err := (&service{}).defaultProvider(t.Context(), "hanzo", "sms"); err == nil {
		t.Fatal("expected error when KMS store is nil (fail closed)")
	}
}

func TestDefaultProviderEmailMail(t *testing.T) {
	// No twilio_email account → falls back to SMTP mail (both from KMS only).
	s := &service{kms: fakeKMS{m: map[string]string{
		"orgs/hanzo/notify/mail/smtp-host":    "smtp.example.com",
		"orgs/hanzo/notify/mail/sender-email": "no-reply@hanzo.ai",
	}}}
	got, err := s.defaultProvider(t.Context(), "hanzo", "email")
	if err != nil {
		t.Fatalf("defaultProvider: %v", err)
	}
	if got != "mail" {
		t.Errorf("want mail, got %q", got)
	}
}

func TestCredsNeverReadEnv(t *testing.T) {
	// Regression: even with the legacy env vars set, creds() must ignore them —
	// KMS is the ONLY source. A nil store yields no creds regardless of env.
	t.Setenv("TWILIO_ACCOUNT_SID", "ACfromenv")
	t.Setenv("TWILIO_AUTH_TOKEN", "envtok")
	t.Setenv("TWILIO_PHONE_NUMBER", "+15559999999")
	if c := (&service{}).creds(t.Context(), "hanzo", "twilio"); len(c) != 0 {
		t.Fatalf("creds must not read env; got %v", c)
	}
}

func TestDefaultProviderUnsupportedChannel(t *testing.T) {
	s := &service{}
	if _, err := s.defaultProvider(t.Context(), "hanzo", "push"); err == nil {
		t.Fatal("expected error for unsupported channel")
	}
}

func TestConstructTwilioRequiresFromNumber(t *testing.T) {
	_, err := constructProvider("twilio", map[string]string{
		"account-sid": "AC", "auth-token": "tok", // no from-number
	}, []string{"+15550001111"})
	if err == nil {
		t.Fatal("expected error when twilio from-number is missing")
	}
}

func TestConstructProviderUnknown(t *testing.T) {
	if _, err := constructProvider("carrier-pigeon", map[string]string{}, nil); err == nil {
		t.Fatal("expected error for an unwired provider")
	}
}

func TestConstructPlivoOK(t *testing.T) {
	n, err := constructProvider("plivo", map[string]string{
		"auth-id": "MA", "auth-token": "tok", "from-number": "+15550001111",
	}, []string{"+15550002222"})
	if err != nil {
		t.Fatalf("constructProvider plivo: %v", err)
	}
	if n == nil {
		t.Fatal("nil notifier")
	}
}
