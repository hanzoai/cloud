package email

import (
	"strings"
	"testing"
)

func TestLinks_EveryWebLinkSaysWhereTheVisitCameFrom(t *testing.T) {
	g := tag{message: "msg_00000000000000000000000d", provider: "ses", campaign: "fall"}
	const want = "utm_source=email&utm_medium=ses&utm_campaign=fall&utm_content=link"
	for in, out := range map[string]string{
		`<a href="https://hanzo.ai/pricing">Pricing</a>`: `<a href="https://hanzo.ai/pricing?` + amp(want) + `1&amp;from=msg_00000000000000000000000d">Pricing</a>`,
		// A sender's own parameters stay, in their order, and theirs win.
		`<a href='https://hanzo.ai/x?ref=7&amp;utm_campaign=spring'>x</a> <a href="https://hanzo.ai/y">y</a>`: `<a href='https://hanzo.ai/x?ref=7&amp;utm_campaign=spring&amp;utm_source=email&amp;utm_medium=ses&amp;utm_content=link1&amp;from=msg_00000000000000000000000d'>x</a> ` +
			`<a href="https://hanzo.ai/y?` + amp(want) + `2&amp;from=msg_00000000000000000000000d">y</a>`,
		// mailto:, a relative link and an anchor are not visits.
		`<a href="mailto:team@hanzo.ai">mail</a><a href="/x">x</a><a href="#top">top</a>`: `<a href="mailto:team@hanzo.ai">mail</a><a href="/x">x</a><a href="#top">top</a>`,
		// The fragment stays where it is.
		`<a href="https://hanzo.ai/docs#start">docs</a>`: `<a href="https://hanzo.ai/docs?` + amp(want) + `1&amp;from=msg_00000000000000000000000d#start">docs</a>`,
	} {
		if got := g.html(in); got != out {
			t.Errorf("html %s\n got %s\nwant %s", in, got, out)
		}
	}
	for in, out := range map[string]string{
		"See https://hanzo.ai/pricing.":       "See https://hanzo.ai/pricing?" + want + "1&from=msg_00000000000000000000000d.",
		"(https://hanzo.ai/a) or http://b.io": "(https://hanzo.ai/a?" + want + "1&from=msg_00000000000000000000000d) or http://b.io?" + want + "2&from=msg_00000000000000000000000d",
		"No links here.":                      "No links here.",
	} {
		if got := g.text(in); got != out {
			t.Errorf("text %q\n got %q\nwant %q", in, got, out)
		}
	}
	// No campaign, no utm_campaign.
	if got := (tag{message: "msg_00000000000000000000000d", provider: "sendgrid"}).text("https://hanzo.ai"); got != "https://hanzo.ai?utm_source=email&utm_medium=sendgrid&utm_content=link1&from=msg_00000000000000000000000d" {
		t.Errorf("no campaign: %s", got)
	}
}

// amp is s as it reads inside an HTML attribute.
func amp(s string) string { return strings.ReplaceAll(s, "&", "&amp;") }

func TestTrueLink_IsTheOriginalWithoutWhoClicked(t *testing.T) {
	for in, want := range map[string]string{
		"https://hanzo.ai/pricing?utm_source=email&from=msg_1": "https://hanzo.ai/pricing?from=msg_1&utm_source=email",
		// SES's click tracker carries the original, escaped, as its first segment.
		"https://abc.r.us-east-1.awstrack.me/L0/https:%2F%2Fhanzo.ai%2Fpricing%3Futm_source=email/1/0100018b-x/AbC=123": "https://hanzo.ai/pricing?utm_source=email",
		// The opt-out link names the address and its token; neither is kept.
		"https://api.hanzo.ai/v1/marketing/unsubscribe?org=acme&channel=email&address=ada%40acme.io&token=9f2a": "https://api.hanzo.ai/v1/marketing/unsubscribe?channel=email&org=acme",
		"https://ada:pw@hanzo.ai/x?who=ada@acme.io&em=abc&ok=1":                                                 "https://hanzo.ai/x?ok=1",
		"not a link": "",
		"":           "",
	} {
		if got := trueLink(in); got != want {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
}
