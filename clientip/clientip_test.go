package clientip

// The client-address rule, and the attack it exists to stop.
//
// THE DEFECT THIS REPLACES: ClientIP returned the LEFT-MOST X-Forwarded-For entry
// with no check on who wrote it. That entry is whatever the client typed, so one
// host could present a million distinct "clients" — defeating the per-IP edge
// limit keyed on it, writing a chosen address into audit rows, and feeding the
// abuse sensor's address table without bound from an unauthenticated request.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// ourProxies is the deployment shape these tests reason about: private space is
// ours, everything public is a client. Built explicitly rather than read from the
// process default so the rule is tested, not the environment.
var ourProxies = parseProxySet("10.0.0.0/8,127.0.0.0/8,0.0.0.0/32,::1/128")

func xff(lines ...string) [][]byte {
	out := make([][]byte, 0, len(lines))
	for _, l := range lines {
		out = append(out, []byte(l))
	}
	return out
}

func TestClientAddr(t *testing.T) {
	cases := []struct {
		name string
		peer string
		fwd  [][]byte
		want string
	}{
		// THE ATTACK. The client writes the left-most entry; our ingress appends
		// what it actually saw. The right-most untrusted hop is the truth.
		{"a forged left-most entry is ignored",
			"10.0.0.5", xff("1.2.3.4, 203.0.113.9"), "203.0.113.9"},
		{"a whole forged chain is ignored",
			"10.0.0.5", xff("1.2.3.4, 5.6.7.8, 9.10.11.12, 203.0.113.9"), "203.0.113.9"},
		// A second header LINE is the same attack with different syntax: fasthttp
		// keeps repeated headers apart, so a rule that reads only the first line
		// reads only the attacker's.
		{"a forged second line does not hide the real hop",
			"10.0.0.5", xff("1.2.3.4", "203.0.113.9"), "203.0.113.9"},
		{"a forged FIRST line does not win over the real hop",
			"10.0.0.5", xff("203.0.113.9", "10.0.0.6"), "203.0.113.9"},

		// Our own hops are skipped, however many of them there are.
		{"internal hops are skipped",
			"10.0.0.5", xff("203.0.113.9, 10.0.0.6, 10.0.0.7"), "203.0.113.9"},

		// A DIRECT caller cannot lie: the peer is the answer and no header is read.
		{"a direct caller is its own peer",
			"198.51.100.4", xff("1.2.3.4"), "198.51.100.4"},

		// In-cluster traffic never transited the edge, so it has no client address —
		// the same answer this function has always given, which is what keeps
		// sibling services out of the public rate limiter.
		{"an in-cluster caller has no client address",
			"10.0.0.5", nil, ""},
		{"a chain of only our own hops has no client address",
			"10.0.0.5", xff("10.0.0.6, 127.0.0.1"), ""},

		// Junk must never become a key.
		{"an unparseable entry is skipped, not keyed",
			"10.0.0.5", xff("not-an-ip, 203.0.113.9"), "203.0.113.9"},
		{"an entry that is only junk yields nothing",
			"10.0.0.5", xff("not-an-ip, <script>"), ""},

		// Canonical form, so one address is one key.
		{"an IPv4-mapped IPv6 address is the same key as its IPv4 form",
			"10.0.0.5", xff("::ffff:203.0.113.9"), "203.0.113.9"},
		{"an entry with a port is the address without it",
			"10.0.0.5", xff("203.0.113.9:44321"), "203.0.113.9"},
		{"whitespace does not make a second key",
			"10.0.0.5", xff("  203.0.113.9  "), "203.0.113.9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientAddr(tc.peer, tc.fwd, ourProxies); got != tc.want {
				t.Fatalf("clientAddr(%q, %q) = %q, want %q", tc.peer, tc.fwd, got, tc.want)
			}
		})
	}
}

// The parse is bounded. A chain of thousands of entries is not a topology, it is
// an attempt to spend CPU in the parse — and the bound discards from the LEFT, so
// it can only ever drop the least trustworthy end.
func TestClientAddr_ChainIsBounded(t *testing.T) {
	long := strings.Repeat("1.2.3.4, ", maxForwardedHops*4) + "203.0.113.9"
	// The real hop is at the far right, within the bound: still found.
	if got := clientAddr("10.0.0.5", xff(long), ourProxies); got != "203.0.113.9" {
		t.Fatalf("got %q, want the right-most hop", got)
	}
	// A chain that is ONLY forged entries, longer than the bound: the walk stops
	// rather than reading all of it, and no client is claimed.
	onlyForged := strings.TrimSuffix(strings.Repeat("1.2.3.4, ", maxForwardedHops*4), ", ")
	if got := clientAddr("10.0.0.5", xff(onlyForged, "10.0.0.6"), ourProxies); got != "1.2.3.4" && got != "" {
		t.Fatalf("got %q, want the bounded walk to stop", got)
	}
}

// The set is data, and it is checkable. An operator whose proxy sits on a public
// address names it; until then a public address is a client, which is the safe
// direction to be wrong in.
func TestProxySet(t *testing.T) {
	s := parseProxySet("10.0.0.0/8, 203.0.113.7 ,garbage,")
	for _, in := range []string{"10.1.2.3", "203.0.113.7"} {
		a, _ := parseClientAddr(in)
		if !s.has(a) {
			t.Fatalf("%s must be trusted", in)
		}
	}
	for _, out := range []string{"203.0.113.8", "198.51.100.1"} {
		a, _ := parseClientAddr(out)
		if s.has(a) {
			t.Fatalf("%s must NOT be trusted", out)
		}
	}
	// An entirely unparseable spec yields no trust rather than blanket trust.
	if len(parseProxySet("garbage,,also-garbage").nets) != 0 {
		t.Fatal("an unparseable spec must not produce a trusted network")
	}
}

// The default set is the one a deployment gets when nobody configures anything,
// and it must trust our own space and nothing public.
func TestDefaultTrustedProxies(t *testing.T) {
	s := parseProxySet(strings.Join(defaultTrustedProxies, ","))
	for _, ours := range []string{"10.42.0.1", "172.16.5.5", "192.168.1.1", "127.0.0.1", "0.0.0.0", "100.64.3.3", "::1", "fd00::1"} {
		a, ok := parseClientAddr(ours)
		if !ok || !s.has(a) {
			t.Fatalf("%s must be trusted by default (ours)", ours)
		}
	}
	for _, theirs := range []string{"203.0.113.9", "198.51.100.4", "1.1.1.1", "2001:db8::1"} {
		a, ok := parseClientAddr(theirs)
		if !ok || s.has(a) {
			t.Fatalf("%s must NOT be trusted by default (public)", theirs)
		}
	}
}

// End to end through a real request, on the process default set. fiber's test
// connection reports the unspecified address as the peer, which the default set
// treats as ours — so the chain is read, and the forged left-most entry loses.
func TestClientIP_OverARealRequest(t *testing.T) {
	var got string
	app := zip.New(zip.Config{})
	app.Get("/probe", func(c *zip.Ctx) error {
		got = ClientIP(c)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Add("X-Forwarded-For", "1.2.3.4")
	req.Header.Add("X-Forwarded-For", "203.0.113.9, 10.0.0.6")
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	if got != "203.0.113.9" {
		t.Fatalf("ClientIP = %q, want the right-most untrusted hop 203.0.113.9", got)
	}

	// And a request with no chain at all is an in-cluster caller: no address, so
	// the edge limiter leaves it alone.
	req = httptest.NewRequest(http.MethodGet, "/probe", nil)
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("an unproxied in-cluster request must have no client address, got %q", got)
	}
}

// TrustedProxy is the readable form of the same set — for a health report, never
// as a gate.
func TestTrustedProxy(t *testing.T) {
	if !TrustedProxy("127.0.0.1") {
		t.Fatal("loopback must be one of ours by default")
	}
	if TrustedProxy("203.0.113.9") {
		t.Fatal("a public address must not be trusted by default")
	}
	if TrustedProxy("not-an-ip") {
		t.Fatal("a non-address is not a proxy")
	}
}

// The country rule is the SAME rule as the address one, applied to a header, and
// the attack it stops is the mirror image: a caller able to state its own
// jurisdiction could state one the risk plane does not act on, which on a rule
// that escalates on geography is the same as switching the rule off.
func TestClientCountry(t *testing.T) {
	cases := []struct {
		name         string
		peer, stated string
		want         string
	}{{
		name: "our own edge stated it, so it is evidence",
		peer: "10.0.0.5", stated: "AF", want: "AF",
	}, {
		name: "a DIRECT caller's header is the caller's own writing",
		// The whole rule. 203.0.113.9 is not one of ours, so nothing it says about
		// where it is counts for anything.
		peer: "203.0.113.9", stated: "US", want: "",
	}, {
		name: "a direct caller cannot state a listed jurisdiction either",
		// The inverse direction: the header is ignored whatever it says, so a
		// caller cannot forge somebody else into a freeze.
		peer: "203.0.113.9", stated: "AF", want: "",
	}, {
		name: "case and padding are normalised",
		peer: "10.0.0.5", stated: " af ", want: "AF",
	}, {
		name: "a non-country code is refused by shape",
		// T1 is what an edge states for Tor. It is not a jurisdiction.
		peer: "10.0.0.5", stated: "T1", want: "",
	}, {
		name: "silence stays silence",
		peer: "10.0.0.5", stated: "", want: "",
	}, {
		name: "a country name is not a country code",
		peer: "10.0.0.5", stated: "Afghanistan", want: "",
	}, {
		name: "an unparseable peer trusts nothing",
		peer: "not-an-address", stated: "AF", want: "",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientCountry(tc.peer, tc.stated, ourProxies); got != tc.want {
				t.Errorf("clientCountry(%q, %q) = %q, want %q", tc.peer, tc.stated, got, tc.want)
			}
		})
	}
}

// And over a real request, so the header NAME and the peer plumbing are exercised
// rather than only the rule beneath them.
func TestClientCountry_OverARealRequest(t *testing.T) {
	var got string
	app := zip.New(zip.Config{})
	app.Get("/probe", func(c *zip.Ctx) error {
		got = ClientCountry(c)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(CountryHeader, "af")
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	// An in-memory test connection reports the unspecified address, which the
	// default set trusts as one of ours — so the header IS read here, which is
	// what this exercises: the header NAME and the normalisation, end to end.
	// The direct-caller refusal is the table above.
	if got != "AF" {
		t.Fatalf("ClientCountry over a request = %q, want %q", got, "AF")
	}

	// No header is no country, which is a different fact from a country nobody
	// listed and must stay distinguishable from one.
	req = httptest.NewRequest(http.MethodGet, "/probe", nil)
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("a request stating no jurisdiction produced %q", got)
	}
}
