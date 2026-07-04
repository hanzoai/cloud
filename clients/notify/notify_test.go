package notify

import (
	"strings"
	"testing"

	ntypes "github.com/hanzoai/notify/pkg/types"
)

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
	t.Setenv("TWILIO_ACCOUNT_SID", "ACxxxx")
	t.Setenv("TWILIO_AUTH_TOKEN", "tok")
	t.Setenv("TWILIO_PHONE_NUMBER", "+15551230000")
	s := &service{}
	got, err := s.defaultProvider(t.Context(), "hanzo", "sms")
	if err != nil {
		t.Fatalf("defaultProvider: %v", err)
	}
	if got != "twilio" {
		t.Errorf("want twilio, got %q", got)
	}
}

func TestDefaultProviderSMSFromNumberAlias(t *testing.T) {
	// Only the TWILIO_FROM_NUMBER alias is set (not TWILIO_PHONE_NUMBER); it must
	// still satisfy the from-number requirement — mirrors notifyd's envFirst.
	t.Setenv("TWILIO_ACCOUNT_SID", "ACxxxx")
	t.Setenv("TWILIO_AUTH_TOKEN", "tok")
	t.Setenv("TWILIO_PHONE_NUMBER", "")
	t.Setenv("TWILIO_FROM_NUMBER", "+15551230000")
	s := &service{}
	got, err := s.defaultProvider(t.Context(), "hanzo", "sms")
	if err != nil || got != "twilio" {
		t.Fatalf("want twilio via alias, got %q err=%v", got, err)
	}
}

func TestDefaultProviderSMSNoneConfigured(t *testing.T) {
	for _, k := range []string{
		"TWILIO_ACCOUNT_SID", "TWILIO_AUTH_TOKEN", "TWILIO_PHONE_NUMBER", "TWILIO_FROM_NUMBER",
		"PLIVO_AUTH_ID", "PLIVO_AUTH_TOKEN",
	} {
		t.Setenv(k, "")
	}
	s := &service{}
	if _, err := s.defaultProvider(t.Context(), "hanzo", "sms"); err == nil {
		t.Fatal("expected error when no SMS provider is configured")
	}
}

func TestDefaultProviderEmailMail(t *testing.T) {
	// No twilio_email from-email → falls back to SMTP mail.
	t.Setenv("TWILIO_FROM_EMAIL", "")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SENDER_EMAIL", "no-reply@hanzo.ai")
	s := &service{}
	got, err := s.defaultProvider(t.Context(), "hanzo", "email")
	if err != nil {
		t.Fatalf("defaultProvider: %v", err)
	}
	if got != "mail" {
		t.Errorf("want mail, got %q", got)
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
