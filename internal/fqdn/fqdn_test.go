package fqdn

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"example.com", "example.com"},
		{"  example.com  ", "example.com"},
		{"EXAMPLE.COM", "example.com"},
		// The trailing root dot is the divergence this package exists to remove:
		// the apps path stripped it, the sites path did not, so the same input
		// was canonical on one endpoint and malformed on the other.
		{"example.com.", "example.com"},
		{"  WWW.Example.COM.  ", "www.example.com"},
		{"", ""},
	} {
		if got := Clean(tc.in); got != tc.want {
			t.Errorf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCleanThenValid(t *testing.T) {
	// The pair is the contract: Clean's output is what Valid accepts. A trailing
	// dot must survive the round trip rather than being rejected as invalid.
	for _, raw := range []string{"example.com.", "EXAMPLE.COM", " deep.sub.example.org. "} {
		if h := Clean(raw); !Valid(h) {
			t.Errorf("Valid(Clean(%q)) = false, want true (got %q)", raw, h)
		}
	}
}

func TestValid(t *testing.T) {
	good := []string{
		"example.com", "www.example.com", "deep.sub.example.org",
		"a.co", "x-y.example.com", "1.example.com",
	}
	for _, h := range good {
		if !Valid(h) {
			t.Errorf("Valid(%q) = false, want true", h)
		}
	}
	bad := []string{
		"", "localhost", "example", ".com", "example..com",
		"-bad.example.com", "bad-.example.com",
		"example.com.", // NOT normalized — Valid does not clean, by contract
		"EXAMPLE.COM",  // uppercase is a caller bug: Clean first
		"http://example.com", "example.com/path", "example.com:8080",
		"exa mple.com", "example.123",
	}
	for _, h := range bad {
		if Valid(h) {
			t.Errorf("Valid(%q) = true, want false", h)
		}
	}
}

func TestValidLengthBound(t *testing.T) {
	// 253 is the presentation-form limit. Build a name that is syntactically
	// perfect but one byte too long, so only the bound can reject it.
	label := strings.Repeat("a", 63)
	long := label + "." + label + "." + label + "." + label + ".com" // 4*63+3+4 = 259
	if len(long) <= maxLen {
		t.Fatalf("fixture is not over the bound: %d", len(long))
	}
	if Valid(long) {
		t.Errorf("Valid(<%d bytes>) = true, want false", len(long))
	}
	fits := strings.Repeat("a.", 100) + "com" // 203 bytes
	if len(fits) > maxLen {
		t.Fatalf("fixture is over the bound: %d", len(fits))
	}
	if !Valid(fits) {
		t.Errorf("Valid(<%d bytes>) = false, want true", len(fits))
	}
}

func TestChallengeAndRecords(t *testing.T) {
	const h, token, target = "app.yourco.com", "tok123", "app.acme.hanzo.app"
	if got, want := Challenge(h), "_hanzo-challenge.app.yourco.com"; got != want {
		t.Fatalf("Challenge = %q, want %q", got, want)
	}
	recs := Records(h, token, target)
	if len(recs) != 2 {
		t.Fatalf("Records len = %d, want 2", len(recs))
	}
	if recs[0] != (Record{Type: "TXT", Name: Challenge(h), Value: token}) {
		t.Errorf("TXT record = %+v", recs[0])
	}
	if recs[1] != (Record{Type: "CNAME", Name: h, Value: target}) {
		t.Errorf("CNAME record = %+v", recs[1])
	}
}

func TestTokenIsFreshAndURLSafe(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		tok, err := Token()
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if seen[tok] {
			t.Fatalf("Token repeated a value: %q", tok)
		}
		seen[tok] = true
		if strings.ContainsAny(tok, "+/=") {
			t.Errorf("Token %q is not URL-safe (must survive a DNS zone file verbatim)", tok)
		}
	}
}

// TestProven exhausts the ownership RULE as a pure table. No context, no
// resolver, no fake — which is the point of Proven being a function of the DNS
// answer rather than of the network.
func TestProven(t *testing.T) {
	const tok = "tok123"
	for _, tc := range []struct {
		name string
		txts []string
		tok  string
		want bool
	}{
		{"exact match", []string{tok}, tok, true},
		// A real zone carries unrelated TXT records at the same name, and zone
		// files disagree about surrounding whitespace. Neither defeats the proof.
		{"among others", []string{"v=spf1 -all", "other=xyz", tok}, tok, true},
		{"surrounding space", []string{"  " + tok + "  "}, tok, true},

		{"no records", nil, tok, false},
		{"empty answer", []string{}, tok, false},
		{"wrong token", []string{"not-the-token"}, tok, false},
		// A substring must not pass: only an exact record value proves control.
		{"token as substring", []string{"x" + tok + "y"}, tok, false},
		{"case differs", []string{"TOK123"}, tok, false},
		// The zero value is never a proof, even against an empty TXT record.
		{"empty token vs empty record", []string{""}, "", false},
		{"empty token vs real record", []string{tok}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Proven(tc.txts, tc.tok); got != tc.want {
				t.Errorf("Proven(%q, %q) = %v, want %v", tc.txts, tc.tok, got, tc.want)
			}
		})
	}
}

// fakeResolver answers from a fixed table, or fails.
type fakeResolver struct {
	txt map[string][]string
	err error
}

func (f fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.txt[name], nil
}

func TestVerifyReadsTheChallengeName(t *testing.T) {
	const h, tok = "app.yourco.com", "tok123"
	r := fakeResolver{txt: map[string][]string{Challenge(h): {tok}}}
	if err := Verify(context.Background(), r, h, tok); err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	// The token must be at the CHALLENGE name, not merely somewhere in the zone.
	r = fakeResolver{txt: map[string][]string{h: {tok}}}
	if err := Verify(context.Background(), r, h, tok); err == nil {
		t.Fatal("Verify = nil for a token at the bare host, want unproven")
	}
}

func TestVerifyFailsClosedOnLookupError(t *testing.T) {
	// A SERVFAIL and an absent record are the same fact to a claimant: the proof
	// is not visible. Neither may read as proven.
	const h, tok = "app.yourco.com", "tok123"
	err := Verify(context.Background(), fakeResolver{err: errors.New("SERVFAIL")}, h, tok)
	if err == nil {
		t.Fatal("Verify = nil on resolver error, want unproven")
	}
	var unproven *ErrUnproven
	if !errors.As(err, &unproven) {
		t.Fatalf("Verify error = %T, want *ErrUnproven", err)
	}
	if unproven.Name != Challenge(h) || unproven.Token != tok {
		t.Errorf("ErrUnproven = %+v, want name %q token %q", unproven, Challenge(h), tok)
	}
	// The message is customer-facing: it must name what to publish.
	if !strings.Contains(err.Error(), Challenge(h)) || !strings.Contains(err.Error(), tok) {
		t.Errorf("message %q does not name the record to publish", err.Error())
	}
}
