package destination

import (
	"net/url"
	"strings"
)

// firewall.go is what no ad or analytics vendor is ever sent. Our own /v1/event schema
// is richer than what GA4, Meta, LinkedIn, X or TikTok see: a prompt, chat text, form
// input, an API payload, a file name, an email address, anything a user wrote, and every
// URL query parameter except the ones a platform needs to credit an ad click never reach
// them. The rule is applied in one place, to every outbound conversion, so no adapter
// can forget it.

// keptQuery are the query parameters a vendor may see: campaign tags and click ids.
func keptQuery(k string) bool {
	k = strings.ToLower(k)
	return strings.HasPrefix(k, "utm_") || k == "gclid" || k == "gbraid" || k == "wbraid" || k == "fbclid"
}

// vendorURL is a page address without its fragment and with only the allowed query.
func vendorURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.Fragment, u.RawFragment, u.User = "", "", nil
	q := u.Query()
	for k := range q {
		if !keptQuery(k) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// firewall is a conversion as a vendor may see it. The raw property bag is dropped
// whole (an adapter renders commerce from the typed fields, never from it); the page
// and referrer lose their queries; the email, phone and hash that match a person are
// kept only when the visitor granted advertising.
func firewall(cv Conversion) Conversion {
	cv.Properties = nil
	cv.URL = vendorURL(cv.URL)
	cv.Referrer = vendorURL(cv.Referrer)
	if !cv.Consent.Marketing {
		cv.User.Email, cv.User.Phone, cv.User.Hashed = "", "", ""
	}
	return cv
}
