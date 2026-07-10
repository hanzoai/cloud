package checkout

import "testing"

func TestAllowedCheckoutRedirect(t *testing.T) {
	// commerce serves the hanzo brand here (request host resolves to hanzo).
	const host = "commerce.hanzo.ai"

	cases := []struct {
		name     string
		url      string
		org      string
		websites []string
		want     bool
	}{
		// Brand org → its own brand's first-party hosts + registrable domains.
		{"hanzo apex", "https://hanzo.ai/onboarding-success", "hanzo", nil, true},
		{"hanzo app subdomain", "https://console.hanzo.ai/done", "hanzo", nil, true},
		{"hanzo chat subdomain", "https://maxpower.hanzo.chat/done", "hanzo", nil, true},
		{"hanzo first-party bot host", "https://playground.hanzo.bot/x", "hanzo", nil, true},
		{"hanzo agency onboarding success", "https://hanzo.agency/onboarding-success", "hanzo", nil, true},
		{"hanzo agency www", "https://www.hanzo.agency/onboarding-success", "hanzo", nil, true},
		{"hanzo agency cancel to pricing", "https://hanzo.agency/pricing", "hanzo", nil, true},
		{"http scheme accepted", "http://hanzo.ai/x", "hanzo", nil, true},

		// hanzo.agency is first-party to the hanzo brand ONLY — a lux org
		// must not be able to redirect a real payment link there.
		{"lux org to hanzo.agency rejected", "https://hanzo.agency/x", "lux", nil, false},
		// A spoof suffix of hanzo.agency must not match the exact-host entry.
		{"hanzo.agency suffix spoof", "https://hanzo.agency.evil.com/x", "hanzo", nil, false},

		// The open-redirect / phishing pivots the allowlist must reject.
		{"attacker host", "https://evil.com/steal", "hanzo", nil, false},
		{"suffix spoof", "https://hanzo.ai.evil.com/x", "hanzo", nil, false},
		{"embedded-at spoof", "https://evil.com?x=hanzo.ai", "hanzo", nil, false},
		{"javascript scheme", "javascript:alert(1)", "hanzo", nil, false},
		{"data scheme", "data:text/html,x", "hanzo", nil, false},
		{"empty", "", "hanzo", nil, false},
		{"scheme-relative (no host parsed)", "//evil.com/x", "hanzo", nil, false},

		// Custom (non-brand) org inherits the deployment's serving brand (hanzo)
		// so its hanzo.* subdomains work, but foreign hosts do not.
		{"custom org inherits brand subdomain", "https://maxpower.hanzo.chat/ok", "maxpower", nil, true},
		{"custom org foreign host rejected", "https://maxpower.example.com/ok", "maxpower", nil, false},
		// …unless it registered that domain as its own website.
		{"custom org own registered site", "https://maxpower.example.com/ok", "maxpower", []string{"https://maxpower.example.com"}, true},
		{"custom org own site bare host entry", "https://shop.acme.io/ok", "acme", []string{"shop.acme.io"}, true},

		// Cross-brand isolation: a lux org may not redirect to hanzo domains.
		{"lux org to lux domain", "https://lux.network/x", "lux", nil, true},
		{"lux org to hanzo domain rejected", "https://hanzo.ai/x", "lux", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AllowedCheckoutRedirect(tc.url, tc.org, tc.websites, host)
			if got != tc.want {
				t.Fatalf("AllowedCheckoutRedirect(%q, org=%q, sites=%v) = %v, want %v",
					tc.url, tc.org, tc.websites, got, tc.want)
			}
		})
	}
}
