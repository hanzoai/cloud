package email

// links.go — every link in a message we send says where the visit came from.
//
// A link in a message POST /v1/email sends gets five query parameters, each only
// when the link does not already carry it, so a sender who sets one keeps theirs:
//
//	utm_source=email           the channel
//	utm_medium=<provider>      ses or sendgrid, the rail it went by
//	utm_campaign=<campaign>    the request's campaign, when it names one
//	utm_content=link<n>        which link of the message it is, from 1
//	from=<message id>          our id for the message (msg_…), which the ledger, the
//	                           event stream and GET /v1/email/:id all name it by
//
// So a visit joins the message, and through it the campaign, without the address
// ever riding in a URL. Only absolute http and https links are touched: the opt-out
// footer is added after, and mailto: and anything relative are left alone.

import (
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// An href in HTML, quoted either way, and an address in plain text.
var (
	hrefs = regexp.MustCompile(`(?i)(href\s*=\s*)("([^"]*)"|'([^']*)')`)
	bare  = regexp.MustCompile(`https?://[^\s<>"'()\[\]{}]+`)
)

// tag is the parameters a message's links carry.
type tag struct {
	message, provider, campaign string
}

// html tags every http(s) href of an HTML body.
func (t tag) html(body string) string {
	n := 0
	return hrefs.ReplaceAllStringFunc(body, func(m string) string {
		parts := hrefs.FindStringSubmatch(m)
		raw, quote := parts[3], `"`
		if strings.HasPrefix(parts[2], "'") {
			raw, quote = parts[4], `'`
		}
		link := html.UnescapeString(raw)
		if !web(link) {
			return m
		}
		n++
		return parts[1] + quote + html.EscapeString(t.link(link, n)) + quote
	})
}

// text tags every http(s) address in a plain-text body. Punctuation that ends a
// sentence is not part of the address.
func (t tag) text(body string) string {
	n := 0
	return bare.ReplaceAllStringFunc(body, func(m string) string {
		link := strings.TrimRight(m, ".,;:!?")
		n++
		return t.link(link, n) + m[len(link):]
	})
}

func web(link string) bool {
	l := strings.ToLower(strings.TrimSpace(link))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// link adds to one address the parameters it does not already carry. The query it
// has keeps its order and its spelling; an address that does not parse is left
// as it is.
func (t tag) link(link string, n int) string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return link
	}
	have := u.Query()
	q := u.RawQuery
	for _, p := range [][2]string{
		{"utm_source", "email"},
		{"utm_medium", t.provider},
		{"utm_campaign", t.campaign},
		{"utm_content", "link" + strconv.Itoa(n)},
		{"from", t.message},
	} {
		if p[1] == "" || have.Has(p[0]) {
			continue
		}
		if q != "" {
			q += "&"
		}
		q += p[0] + "=" + url.QueryEscape(p[1])
	}
	u.RawQuery = q
	return u.String()
}

// private are the query parameters a clicked link may carry that say who clicked:
// the opt-out link's address and its token, an address a sender put in a link.
var private = []string{"address", "email", "token", "em"}

// trueLink is the address a click went to, as the sender wrote it: unwrapped when a
// provider's click tracker wrapped it (SES's awstrack.me /L0/ form carries the
// original, escaped, as its first segment; SendGrid reports the original itself),
// with every parameter that names the person removed, every path segment holding
// an address replaced by "-", and no fragment (a page's fragment is the page's, and
// a sender may put anything there), so the event stream never carries an address.
func trueLink(link string) string {
	link = strings.TrimSpace(link)
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(u.Hostname()), ".awstrack.me") {
		if rest, ok := strings.CutPrefix(u.EscapedPath(), "/L0/"); ok {
			first, _, _ := strings.Cut(rest, "/")
			if orig, err := url.PathUnescape(first); err == nil {
				if inner, err := url.Parse(orig); err == nil && inner.Host != "" {
					u = inner
				}
			}
		}
	}
	u.User = nil
	u.Fragment, u.RawFragment = "", ""
	if strings.Contains(u.Path, "@") {
		segs := strings.Split(u.Path, "/")
		for i, s := range segs {
			if strings.Contains(s, "@") {
				segs[i] = "-"
			}
		}
		u.Path, u.RawPath = strings.Join(segs, "/"), ""
	}
	q := u.Query()
	for k, vs := range q {
		lk := strings.ToLower(k)
		named := false
		for _, p := range private {
			named = named || lk == p
		}
		for _, v := range vs {
			named = named || strings.Contains(v, "@")
		}
		if named {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
