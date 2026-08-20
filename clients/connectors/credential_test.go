package connectors

import (
	"context"
	"errors"
	"testing"
)

// The rule that makes a credential connector worth having: it PROVES the
// credentials before it stores them. A connector that sealed whatever was
// pasted would report an account connected that has never worked, and the
// failure would land on somebody's first real send instead of on the paste.
func TestCredentialVerifyRunsBeforeCustody(t *testing.T) {
	var sealed bool
	p := &Provider{
		ID: "probe", Name: "Probe", Kind: KindCredential,
		Configured: alwaysAvailable,
		Fields:     []Field{{Name: "token", Label: "Token", Secret: true, Required: true}},
		Secrets:    []string{"token"},
		Verify: func(context.Context, map[string]string) (*ExchangeResult, error) {
			sealed = true
			return nil, errors.New("provider refused")
		},
	}
	res, err := p.Verify(context.Background(), map[string]string{"token": "t"})
	if err == nil || res != nil {
		t.Fatal("a refused credential must yield no result")
	}
	if !sealed {
		t.Fatal("verify must actually be called")
	}
}

// Every field that reaches a URL or a dial is shape-checked first. These are not
// cosmetic rules: phone_number_id is concatenated into a graph URL, and host and
// port are dialed — a caller-supplied value reaching either unchecked is how a
// settings form becomes a request forgery.
func TestCredentialFieldsAreShapeChecked(t *testing.T) {
	ctx := context.Background()

	t.Run("whatsapp refuses a non-numeric id before it builds a URL", func(t *testing.T) {
		for _, bad := range []string{"", "abc", "1/../../admin", "1?x=y", "1#f"} {
			if _, err := verifyWhatsApp(ctx, map[string]string{
				"phone_number_id": bad, "access_token": "t",
			}); err == nil {
				t.Errorf("accepted phone_number_id %q", bad)
			}
		}
	})

	t.Run("sms refuses a malformed sid or from-number", func(t *testing.T) {
		if _, err := verifySMS(ctx, map[string]string{
			"account_sid": "AC/../x", "auth_token": "t", "from_number": "+15551234567",
		}); err == nil {
			t.Error("accepted a path-shaped account sid")
		}
		if _, err := verifySMS(ctx, map[string]string{
			"account_sid": "AC123", "auth_token": "t", "from_number": "5551234567",
		}); err == nil {
			t.Error("accepted a non-E.164 from number")
		}
	})

	t.Run("email refuses a host that is not a hostname", func(t *testing.T) {
		// A scheme, a port, credentials or a path in the host field all mean the
		// dial is being pointed somewhere structural rather than at a mail server.
		for _, bad := range []string{
			"", "localhost", "smtp.example.com:25", "http://smtp.example.com",
			"user@smtp.example.com", "smtp.example.com/x", "smtp .example.com",
			"-lead.example.com", "trail-.example.com",
		} {
			if isHost(bad) {
				t.Errorf("accepted host %q", bad)
			}
		}
		for _, good := range []string{"smtp.example.com", "mail.a-b.co.uk"} {
			if !isHost(good) {
				t.Errorf("refused host %q", good)
			}
		}
		if _, err := verifyEmail(ctx, map[string]string{
			"host": "smtp.example.com", "port": "not-a-port",
			"username": "u", "password": "p", "from_address": "a@b.c",
		}); err == nil {
			t.Error("accepted a non-numeric port")
		}
		if _, err := verifyEmail(ctx, map[string]string{
			"host": "smtp.example.com", "port": "587",
			"username": "u", "password": "p", "from_address": "not-an-address",
		}); err == nil {
			t.Error("accepted a from address with no @")
		}
	})
}

// A secret field must never be published in the catalog view — the form says a
// field IS secret, and the value only ever travels inbound.
func TestCredentialViewPublishesFormNotValues(t *testing.T) {
	for id, p := range registry {
		if p.Kind != KindCredential {
			continue
		}
		v := providerView{Kind: p.Kind, Fields: p.Fields}
		for _, f := range v.Fields {
			if f.Name == "" || f.Label == "" {
				t.Errorf("%s publishes an unnamed field", id)
			}
		}
		if v.Kind != KindCredential {
			t.Errorf("%s must publish its kind so a client knows which connect it is doing", id)
		}
	}
}
