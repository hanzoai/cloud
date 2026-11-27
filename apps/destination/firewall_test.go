package destination

import "testing"

func TestVendorURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://hanzo.ai/pricing?utm_source=x&q=secret+prompt&gclid=G1&email=a%40b.c&fbclid=F1#frag": "https://hanzo.ai/pricing?fbclid=F1&gclid=G1&utm_source=x",
		"https://hanzo.ai/chat?thread=abc": "https://hanzo.ai/chat",
		"https://hanzo.ai/":                "https://hanzo.ai/",
		"":                                 "",
	} {
		if got := vendorURL(in); got != want {
			t.Errorf("vendorURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirewall(t *testing.T) {
	cv := Conversion{
		URL: "https://hanzo.ai/chat?q=my+prompt", Referrer: "https://x.com/?s=1",
		Properties: map[string]any{"prompt": "hello", "filename": "tax.pdf"},
		User:       UserData{Email: "a@b.c", Phone: "+1555", Hashed: "abc"},
	}
	got := firewall(cv)
	if got.Properties != nil || got.URL != "https://hanzo.ai/chat" || got.Referrer != "https://x.com/" {
		t.Errorf("properties and queries leak: %+v", got)
	}
	if got.User.Email != "" || got.User.Phone != "" || got.User.Hashed != "" {
		t.Errorf("match keys leave without the advertising grant: %+v", got.User)
	}
	cv.Consent.Marketing = true
	if firewall(cv).User.Email != "a@b.c" {
		t.Error("the advertising grant lets the match keys through to be hashed")
	}
}
