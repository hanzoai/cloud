package email

import (
	"html"
	"strings"
)

// Footer is the opt-out at the foot of every commercial message, wherever it is
// sent from: the signed unsubscribe link, and for an address at a company's own
// domain, the same link opting out everyone at that domain. An HTML body gets
// links, anything else text. An empty link leaves the body as it is.
func Footer(body, unsubscribe, address string) string {
	if unsubscribe == "" {
		return body
	}
	company := ""
	if at := strings.LastIndexByte(address, '@'); at > 0 {
		if d := strings.ToLower(strings.TrimSpace(address[at+1:])); d != "" && !Free(d) {
			company = d
		}
	}
	whole := unsubscribe + "&company=true"
	if htmlish(body) {
		f := `<p style="font-size:12px;color:#666">—<br><a href="` + html.EscapeString(unsubscribe) + `">Unsubscribe</a>`
		if company != "" {
			f += ` · <a href="` + html.EscapeString(whole) + `">Unsubscribe everyone at ` + html.EscapeString(company) + `</a>`
		}
		return body + "\n" + f + "</p>\n"
	}
	f := "\n\n—\nUnsubscribe: " + unsubscribe
	if company != "" {
		f += "\nUnsubscribe everyone at " + company + ": " + whole
	}
	return body + f
}

// Unsubscribe is the two headers a commercial message carries to its signed
// link: List-Unsubscribe, and List-Unsubscribe-Post for the one-click POST a mail
// client sends from it (RFC 8058).
func Unsubscribe(link string) map[string]string {
	return map[string]string{"List-Unsubscribe": "<" + link + ">", "List-Unsubscribe-Post": "List-Unsubscribe=One-Click"}
}

// htmlish is whether a body reads as HTML.
func htmlish(body string) bool {
	b := strings.TrimSpace(body)
	return strings.HasPrefix(b, "<") || strings.Contains(b, "</")
}
