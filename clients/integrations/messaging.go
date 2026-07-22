package integrations

// messaging.go registers the email, marketing, and SMS connectors both an
// online store and an AI startup depend on for transactional and lifecycle
// messaging. Customer-held keys, verified live via keyVerify.

import (
	"fmt"
	"strings"
)

func init() {
	register(&Provider{
		ID: "sendgrid", Name: "SendGrid",
		Description: "Transactional and marketing email.",
		Category:    "Email", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "sendgrid",
			origin:   constOrigin("SENDGRID_API_BASE", "https://api.sendgrid.com"),
			path:     "/v3/scopes", place: bearer, minLen: 16, prefix: "SG.",
		}),
	})

	register(&Provider{
		ID: "resend", Name: "Resend",
		Description: "Email for developers.",
		Category:    "Email", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "resend",
			origin:   constOrigin("RESEND_API_BASE", "https://api.resend.com"),
			path:     "/domains", place: bearer, minLen: 8, prefix: "re_",
		}),
	})

	register(&Provider{
		ID: "postmark", Name: "Postmark",
		Description: "Transactional email delivery.",
		Category:    "Email", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "postmark",
			origin:   constOrigin("POSTMARK_API_BASE", "https://api.postmarkapp.com"),
			path:     "/server", place: headerAt, name: "X-Postmark-Server-Token", minLen: 16,
			extra: map[string]string{"Accept": "application/json"},
		}),
	})

	register(&Provider{
		ID: "mailchimp", Name: "Mailchimp",
		Description: "Email marketing and audiences.",
		Category:    "Marketing", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "mailchimp",
			origin:   mailchimpOrigin,
			path:     "/3.0/", place: basicAt, name: "hanzo", minLen: 8,
		}),
	})

	register(&Provider{
		ID: "klaviyo", Name: "Klaviyo",
		Description: "Ecommerce email and SMS marketing.",
		Category:    "Marketing", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "klaviyo",
			origin:   constOrigin("KLAVIYO_API_BASE", "https://a.klaviyo.com"),
			path:     "/api/accounts/", place: headerAt, name: "Authorization",
			scheme: "Klaviyo-API-Key", minLen: 8,
			extra:  map[string]string{"revision": "2024-10-15"},
		}),
	})

	register(&Provider{
		ID: "twilio", Name: "Twilio",
		Description: "SMS, voice, and verification. Connect with Account SID + Auth Token.",
		Category:    "Communication", Scope: userScope,
		Secrets: []string{"auth_token"},
		Verify: keyVerify(keySpec{
			provider: "twilio",
			origin:   constOrigin("TWILIO_API_BASE", "https://api.twilio.com"),
			path:     "/2010-04-01/Accounts/{account}.json",
			place:    basicAt, name: accountToken,
			secret:   "auth_token", minLen: 16, echoAccount: true,
		}),
	})
}

// mailchimpOrigin derives the account's data-center origin from the key's
// suffix (Mailchimp keys end in "-<dc>", e.g. "...-us21"). Env-overridable.
func mailchimpOrigin(in VerifyInput) (string, error) {
	if v := envBase("MAILCHIMP_API_BASE"); v != "" {
		return v, nil
	}
	dc, err := datacenter(in.Token)
	if err != nil {
		return "", err
	}
	return "https://" + dc + ".api.mailchimp.com", nil
}

// alphanumeric reports whether s is a non-empty [a-z0-9] label — a real
// Mailchimp data center (e.g. "us21"), tight enough that a hostile key cannot
// shape the verify host beyond the fixed .api.mailchimp.com suffix.
func alphanumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// datacenter extracts the data-center label from a Mailchimp key's "-<dc>"
// suffix, rejecting a key that lacks one (it cannot be routed).
func datacenter(key string) (string, error) {
	i := strings.LastIndexByte(strings.TrimSpace(key), '-')
	if i < 0 {
		return "", fmt.Errorf("mailchimp key is missing its data-center suffix")
	}
	dc := strings.TrimSpace(key[i+1:])
	if !alphanumeric(dc) {
		return "", fmt.Errorf("mailchimp key is missing its data-center suffix")
	}
	return dc, nil
}
