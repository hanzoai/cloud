package provider

// back.go is where an OAuth return sends the person: the Hanzo page that started
// the connect, or the console's connectors page when none did.
//
// A connect start may name a return address ("return"). backTo accepts it only
// when it names one of this deployment's return pages (brand.Info.Returns) —
// scheme, host and path exactly, and the app address `at` as the only query
// that counts — and answers the registered page itself, never the caller's
// bytes. A page list rather than an origin list, because a free path on an
// allowed origin reaches whatever that origin redirects to with the query kept
// (hanzo.ai/docs → docs.hanzo.ai), and a free `at` reaches any run or project
// view. The page is stored beside the flow's nonce (oauth_nonces.back), never
// carried in the URL the provider sees, so the callback reads it from the row the
// signed state names. An absent or refused address returns to the console.

import (
	"net/url"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/brand"
)

// maxBack bounds a return address. A page address is short; a longer one is
// refused before it is parsed.
const maxBack = 512

// backTo answers the registered return page raw names, or "" when it names none.
func backTo(raw string) string {
	if raw == "" || len(raw) > maxBack || !brand.Registered(cloud.Brand()) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return ""
	}
	page := u.Scheme + "://" + u.Host + u.Path
	if u.Path == "" {
		page += "/"
	}
	if at := u.Query()["at"]; len(at) == 1 {
		page += "?at=" + at[0]
	}
	returns := brand.For(cloud.Brand()).Returns
	if i := slices.Index(returns, page); i >= 0 {
		return returns[i]
	}
	return ""
}

// returnURL is the one return Location: back (a backTo answer) or the console's
// connectors page, carrying {statusKey}={provider}&{detailKey}={detail}. Every
// dynamic value is query-escaped, so none can leave the query or put a CR/LF in
// the header.
func returnURL(console, back, statusKey, provider, detailKey, detail string) string {
	answer := statusKey + "=" + url.QueryEscape(provider) + "&" + detailKey + "=" + url.QueryEscape(detail)
	switch {
	case back == "":
		return console + "/connectors?" + answer
	case strings.Contains(back, "?"):
		return back + "&" + answer
	default:
		return back + "?" + answer
	}
}
