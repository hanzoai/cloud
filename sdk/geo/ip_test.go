package geo

// The client-address rule, and the attack it exists to stop.
//
// THE DEFECT THIS REPLACES: IP returned the LEFT-MOST X-Forwarded-For entry
// with no check on who wrote it. That entry is whatever the client typed, so one
// host could present a million distinct "clients" — defeating the per-IP edge
// limit keyed on it, writing a chosen address into audit rows, and feeding the
// abuse sensor's address table without bound from an unauthenticated request.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/address"
	fiber "github.com/zap-proto/fiber/v3"
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
			if got := clientAddr(tc.peer, tc.fwd, nil, ourProxies); got != tc.want {
				t.Fatalf("clientAddr(%q, %q) = %q, want %q", tc.peer, tc.fwd, got, tc.want)
			}
		})
	}
}

// edge is a Cloudflare edge address, the one measured in cloud's own request log.
const edge = "104.22.100.153"

// The Cloudflare rule. The ingress (10.0.0.5) appends the hop it saw; when that hop
// is a Cloudflare edge, Cloudflare's CF-Connecting-IP names the client, and from any
// other hop the header is somebody's claim about themselves.
func TestClientAddr_Cloudflare(t *testing.T) {
	cases := []struct {
		name string
		peer string
		fwd  [][]byte
		cf   [][]byte
		want string
	}{
		{"a visitor behind Cloudflare is the visitor",
			"10.0.0.5", xff("198.51.100.23, " + edge), xff("198.51.100.23"), "198.51.100.23"},
		{"Cloudflare's header, not the chain, names the visitor",
			"10.0.0.5", xff("203.0.113.50, " + edge), xff("198.51.100.23"), "198.51.100.23"},
		{"a client-forged XFF prefix is ignored",
			"10.0.0.5", xff("1.2.3.4, 5.6.7.8, 198.51.100.23, " + edge), xff("198.51.100.23"), "198.51.100.23"},
		{"a client-forged XFF line is ignored",
			"10.0.0.5", xff("1.2.3.4", "198.51.100.23, "+edge), xff("198.51.100.23"), "198.51.100.23"},

		{"a forged CF-Connecting-IP from a direct non-Cloudflare peer is ignored",
			"203.0.113.9", nil, xff("1.2.3.4"), "203.0.113.9"},
		{"a forged CF-Connecting-IP through the ingress from a non-Cloudflare hop is ignored",
			"10.0.0.5", xff("203.0.113.9"), xff("1.2.3.4"), "203.0.113.9"},
		{"a Cloudflare address forged into the chain does not unlock the header",
			"10.0.0.5", xff(edge + ", 203.0.113.9"), xff("1.2.3.4"), "203.0.113.9"},

		{"an in-cluster caller has no address, whatever header it sends",
			"10.0.0.5", nil, xff("198.51.100.23"), ""},
		{"a chain of only our own hops has no address, whatever header it sends",
			"10.0.0.5", xff("10.0.0.6, 127.0.0.1"), xff("198.51.100.23"), ""},

		// A Workers subrequest: the script chose every XFF entry, and Cloudflare states
		// its own Workers address in the header. That address is the answer; the
		// header is never walked back into the script's chain.
		{"a Workers subrequest is its Cloudflare address, never a script-chosen XFF entry",
			"10.0.0.5", xff("6.6.6.6, 172.70.1.1"), xff("2a06:98c0:3600::103"), "2a06:98c0:3600::103"},

		{"an IPv6 visitor through a v6 edge is the v6 address",
			"10.0.0.5", xff("2001:db8:1::7, 2606:4700:10::1"), xff("2001:db8:1::7"), "2001:db8:1::7"},
		{"an IPv6 visitor through a v4 edge is the v6 address",
			"10.0.0.5", xff("2001:db8:1::7, " + edge), xff("2001:db8:1::7"), "2001:db8:1::7"},
		{"a Cloudflare peer with no ingress is read the same way",
			edge, nil, xff("198.51.100.23"), "198.51.100.23"},
		{"an IPv4-mapped statement is the IPv4 key",
			"10.0.0.5", xff("198.51.100.23, " + edge), xff("::ffff:198.51.100.23"), "198.51.100.23"},
		{"a zoned statement is the address without its zone",
			"10.0.0.5", xff("198.51.100.23, " + edge), xff("2001:db8:1::7%eth0"), "2001:db8:1::7"},
		{"a zoned Cloudflare hop is still a Cloudflare hop",
			"10.0.0.5", xff("198.51.100.23, 2606:4700::1%eth0"), xff("198.51.100.23"), "198.51.100.23"},

		// A Cloudflare hop whose statement is missing or malformed is the edge: one
		// shared bucket, never "", which would pass as an in-cluster caller.
		{"Cloudflare without its header is the edge",
			"10.0.0.5", xff("198.51.100.23, " + edge), nil, edge},
		{"an unparseable statement is the edge",
			"10.0.0.5", xff("198.51.100.23, " + edge), xff("not-an-ip"), edge},
		{"two statement lines are the edge",
			"10.0.0.5", xff("198.51.100.23, " + edge), xff("198.51.100.23", "1.2.3.4"), edge},
		{"a statement listing addresses is the edge",
			"10.0.0.5", xff("198.51.100.23, " + edge), xff("198.51.100.23, 1.2.3.4"), edge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientAddr(tc.peer, tc.fwd, tc.cf, ourProxies); got != tc.want {
				t.Fatalf("clientAddr(%q, %q, cf=%q) = %q, want %q", tc.peer, tc.fwd, tc.cf, got, tc.want)
			}
		})
	}
}

// The Cloudflare list is a security gate, so it must be present, parse whole, and
// never overlap our own space.
func TestCloudflareRanges(t *testing.T) {
	if len(cloudflareRanges) == 0 {
		t.Fatal("the Cloudflare range list is empty: every visitor behind Cloudflare resolves to its edge")
	}
	if len(cloudflare.nets) != len(cloudflareRanges) {
		t.Fatalf("%d of %d Cloudflare ranges parse; a range that does not parse is silently absent",
			len(cloudflare.nets), len(cloudflareRanges))
	}
	for _, in := range []string{edge, "173.245.48.1", "172.70.1.1", "2606:4700::1", "2a06:98c0:3600::103"} {
		if a, _ := parseClientAddr(in); !cloudflare.has(a) {
			t.Errorf("%s is a Cloudflare address, the set says no", in)
		}
	}
	for _, out := range []string{"203.0.113.9", "198.51.100.23", "10.0.0.5", "2001:db8::1"} {
		if a, _ := parseClientAddr(out); cloudflare.has(a) {
			t.Errorf("%s is not a Cloudflare address, the set says yes", out)
		}
	}
	ours := parseProxySet(strings.Join(defaultTrustedProxies, ","))
	for _, c := range cloudflare.nets {
		for _, o := range ours.nets {
			if c.Overlaps(o) {
				t.Errorf("Cloudflare range %s overlaps our own %s", c, o)
			}
		}
	}
}

// The parse is bounded. A chain of thousands of entries is not a topology, it is
// an attempt to spend CPU in the parse — and the bound discards from the LEFT, so
// it can only ever drop the least trustworthy end.
func TestClientAddr_ChainIsBounded(t *testing.T) {
	long := strings.Repeat("1.2.3.4, ", maxForwardedHops*4) + "203.0.113.9"
	// The real hop is at the far right, within the bound: still found.
	if got := clientAddr("10.0.0.5", xff(long), nil, ourProxies); got != "203.0.113.9" {
		t.Fatalf("got %q, want the right-most hop", got)
	}
	// A chain that is ONLY forged entries, longer than the bound: the walk stops
	// rather than reading all of it, and no client is claimed.
	onlyForged := strings.TrimSuffix(strings.Repeat("1.2.3.4, ", maxForwardedHops*4), ", ")
	if got := clientAddr("10.0.0.5", xff(onlyForged, "10.0.0.6"), nil, ourProxies); got != "1.2.3.4" && got != "" {
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
func TestClientIP_AppliesTheRuleAtTheEdge(t *testing.T) {
	// THE RULE, driven through a real app. The harness connection reports the
	// unspecified peer, which is our own space, so the chain is readable and the
	// answer is its right-most entry that is not one of our proxies.
	//
	// This briefly asserted the opposite — that IP is whatever the
	// framework resolved — but the framework honours a forwarded header only
	// under proxy-trust config no binary wires, so that doctrine collapsed every
	// caller into the socket peer: one bucket behind the ingress, an empty
	// address in every audit row. The rule lives here, reading the framework's
	// raw facts, and every consumer (the edge limiter, the abuse sensor, the
	// audit trail) shares this one answer through its own path.
	check := func(t *testing.T, name, want string, mut func(*http.Request)) {
		t.Helper()
		var got string
		app := zip.New(zip.Config{})
		app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
			got = IP(c)
			return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
		})
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		mut(req)
		if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s: IP = %q, want %q", name, got, want)
		}
	}

	// No chain, in-cluster peer: no client transited the edge.
	check(t, "bare request", "", func(*http.Request) {})
	// Across lines and within a line, later is nearer and truer. The client
	// wrote the first line before our infrastructure saw the request; the hop
	// our own proxy appended is the one that counts, so the forged 1.2.3.4 is
	// never reached.
	check(t, "with a forwarded chain", "203.0.113.9", func(r *http.Request) {
		r.Header.Add("X-Forwarded-For", "1.2.3.4")
		r.Header.Add("X-Forwarded-For", "203.0.113.9, 10.0.0.6")
	})
	// One public hop from a trusted peer is the client itself.
	check(t, "with a single forged hop", "198.51.100.255", func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "198.51.100.255")
	})

	// Two headers read side by side. fasthttp's PeekAll reuses one buffer, so read
	// without a copy the chain BECAME Cloudflare's header: a caller that reached the
	// ingress around Cloudflare named its own address by sending one.
	check(t, "a forged CF-Connecting-IP behind a non-Cloudflare hop", "203.0.113.9", func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("CF-Connecting-IP", "1.2.3.4")
	})
	check(t, "two Cloudflare statements are the edge", edge, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "198.51.100.23, "+edge)
		r.Header.Add("CF-Connecting-IP", "198.51.100.23")
		r.Header.Add("CF-Connecting-IP", "1.2.3.4")
	})
	check(t, "Cloudflare names the visitor", "198.51.100.23", func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.23, "+edge)
		r.Header.Set("CF-Connecting-IP", "198.51.100.23")
	})
}

// The peer is the socket, never the framework's resolution. The host runs with
// zip.Config TrustProxy (cmd/cloud endpointConfig), under which c.Fiber().IP() is the
// raw X-Forwarded-For value — for the two-hop chain every Cloudflare visitor arrives
// with, not an address at all. Both configurations must give one answer.
func TestClientIP_ReadsTheSocketNotTheFramework(t *testing.T) {
	for _, cfg := range []zip.Config{
		{},
		{TrustProxy: true, TrustedProxies: Proxies(), ProxyHeader: "X-Forwarded-For"},
	} {
		var got string
		app := zip.New(cfg)
		app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
			got = IP(c)
			return c.NoContent(http.StatusNoContent)
		})
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.23, "+edge)
		req.Header.Set("CF-Connecting-IP", "198.51.100.23")
		if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatal(err)
		}
		if got != "198.51.100.23" {
			t.Fatalf("TrustProxy=%v: IP = %q, want the visitor Cloudflare named", cfg.TrustProxy, got)
		}
	}
}

// Trusted is the readable form of the same set — for a health report, never
// as a gate.
func TestTrustedProxy(t *testing.T) {
	if !Trusted("127.0.0.1") {
		t.Fatal("loopback must be one of ours by default")
	}
	if Trusted("203.0.113.9") {
		t.Fatal("a public address must not be trusted by default")
	}
	if Trusted("not-an-ip") {
		t.Fatal("a non-address is not a proxy")
	}
}

// The country is read by IP's header rule and by nothing looser: Cloudflare's
// header, only when the hop the chain resolves to is a Cloudflare edge. The attack it
// stops is a caller stating its own jurisdiction, which on a rule that escalates on
// geography, or decides consent, is the same as switching the rule off.
func TestClientCountry(t *testing.T) {
	cases := []struct {
		name string
		peer string
		fwd  [][]byte
		cc   [][]byte
		want string
	}{
		{"Cloudflare stated it, so it is evidence",
			"10.0.0.5", xff("198.51.100.23, " + edge), xff("AF"), "AF"},
		{"a Cloudflare peer with no ingress is read the same way",
			edge, nil, xff("AF"), "AF"},
		{"an IPv6 Cloudflare hop is a Cloudflare hop",
			"10.0.0.5", xff("2001:db8:1::7, 2606:4700:10::1"), xff("DE"), "DE"},

		// THE DEFECT. The peer is our ingress, so the old rule read the header; the
		// hop is the caller itself, reaching the origin around Cloudflare, so the
		// header is its own writing.
		{"a non-Cloudflare hop behind our ingress states nothing",
			"10.0.0.5", xff("203.0.113.9"), xff("US"), ""},
		{"a non-Cloudflare hop cannot state a listed jurisdiction either",
			"10.0.0.5", xff("203.0.113.9"), xff("AF"), ""},
		{"a Cloudflare address forged into the chain does not unlock the header",
			"10.0.0.5", xff(edge + ", 203.0.113.9"), xff("AF"), ""},
		{"an in-cluster caller has no country, whatever header it sends",
			"10.0.0.5", nil, xff("AF"), ""},
		{"a chain of only our own hops has no country",
			"10.0.0.5", xff("10.0.0.6, 127.0.0.1"), xff("AF"), ""},
		{"a DIRECT caller's header is the caller's own writing",
			"203.0.113.9", nil, xff("US"), ""},
		{"an unparseable peer trusts nothing",
			"not-an-address", xff("198.51.100.23, " + edge), xff("AF"), ""},

		// Cloudflare's own statement, shaped.
		{"case and padding are normalised",
			"10.0.0.5", xff("198.51.100.23, " + edge), xff(" af "), "AF"},
		{"a non-country code is refused by shape",
			// T1 is what Cloudflare states for Tor. It is not a jurisdiction.
			"10.0.0.5", xff("198.51.100.23, " + edge), xff("T1"), ""},
		{"silence stays silence",
			"10.0.0.5", xff("198.51.100.23, " + edge), nil, ""},
		{"two statement lines are no statement",
			"10.0.0.5", xff("198.51.100.23, " + edge), xff("AF", "US"), ""},
		{"a country name is not a country code",
			"10.0.0.5", xff("198.51.100.23, " + edge), xff("Afghanistan"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientCountry(tc.peer, tc.fwd, tc.cc, ourProxies); got != tc.want {
				t.Errorf("clientCountry(%q, %q, %q) = %q, want %q", tc.peer, tc.fwd, tc.cc, got, tc.want)
			}
		})
	}
}

// And over a real request, so the header NAMES and the peer plumbing are exercised
// rather than only the rule beneath them. An in-memory test connection reports the
// unspecified address, which the default set trusts as one of ours — the ingress's
// position — so what decides each answer here is the chain.
func TestClientCountry_OverARealRequest(t *testing.T) {
	var got string
	app := zip.New(zip.Config{})
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
		got = Country(c)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	ask := func(chain, country string) string {
		t.Helper()
		got = "unset"
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		if chain != "" {
			req.Header.Set("X-Forwarded-For", chain)
		}
		if country != "" {
			req.Header.Set(CountryHeader, country)
		}
		if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if c := ask("198.51.100.23, "+edge, "af"); c != "AF" {
		t.Fatalf("behind Cloudflare: Country = %q, want AF", c)
	}
	if c := ask("203.0.113.9", "AF"); c != "" {
		t.Fatalf("around Cloudflare, through our ingress: Country = %q; the caller wrote it", c)
	}
	if c := ask("", "AF"); c != "" {
		t.Fatalf("an in-cluster caller: Country = %q; no edge stated anything", c)
	}
	// No header is no country, which is a different fact from a country nobody
	// listed and must stay distinguishable from one.
	if c := ask("198.51.100.23, "+edge, ""); c != "" {
		t.Fatalf("a request stating no jurisdiction produced %q", c)
	}
}

// TestTheNetHTTPFormRefusesAChosenAddress pins the defect that produced IPOf.
//
// apps/metering had its own body, and it returned the LEFT-MOST X-Forwarded-For
// entry — the one value in the chain a caller writes for itself — into
// Usage.IP, the billing and audit column. This asserts the shared rule does
// not, which is the whole reason the copy is gone.
func TestTheNetHTTPFormRefusesAChosenAddress(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/anything", nil)
	r.RemoteAddr = "203.0.113.7:44321" // a DIRECT caller: the peer is not our proxy
	r.Header.Set("X-Forwarded-For", "1.2.3.4")

	got := IPOf(r)
	if got == "1.2.3.4" {
		t.Errorf("IPOf = %q — it read the address the caller wrote for itself, "+
			"which is what put a chosen value in the billing and audit column", got)
	}
	if got != "203.0.113.7" {
		t.Errorf("IPOf = %q, want the socket peer 203.0.113.7 — a direct caller's "+
			"TCP source cannot be forged, so it is the truth", got)
	}
}

// TestTheNetHTTPFormAgreesWithTheZipForm: one rule, two callers. If these two ever
// disagree there are two rules again, which is the thing IPOf was added to
// prevent rather than to duplicate.
func TestTheNetHTTPFormAgreesWithTheZipForm(t *testing.T) {
	for _, tc := range []struct{ peer, xff, cf string }{
		{"203.0.113.7:1", "", ""},
		{"203.0.113.7:1", "1.2.3.4", ""},
		{"203.0.113.7:1", "1.2.3.4, 5.6.7.8", ""},
		{"[2001:db8::1]:1", "1.2.3.4", ""},
		{"203.0.113.7:1", "", "1.2.3.4"},
		{"10.0.0.5:1", "198.51.100.23, " + edge, "198.51.100.23"},
		{"10.0.0.5:1", "198.51.100.23, " + edge, ""},
		{"10.0.0.5:1", "", "198.51.100.23"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		var chain, cf [][]byte
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
			chain = [][]byte{[]byte(tc.xff)}
		}
		if tc.cf != "" {
			r.Header.Set("CF-Connecting-IP", tc.cf)
			cf = [][]byte{[]byte(tc.cf)}
		}
		if got, want := IPOf(r), clientAddr(tc.peer, chain, cf, trustedProxies()); got != want {
			t.Errorf("peer=%q xff=%q cf=%q: IPOf=%q, clientAddr=%q — two rules again",
				tc.peer, tc.xff, tc.cf, got, want)
		}
	}
	// And the net/http form reaches Cloudflare's header at all.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:1"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.23, "+edge)
	r.Header.Set("CF-Connecting-IP", "198.51.100.23")
	if got := IPOf(r); got != "198.51.100.23" {
		t.Errorf("IPOf behind Cloudflare = %q, want the visitor", got)
	}
}

// INSIDE A SUBSYSTEM THE SOCKET IS NOBODY. The host reaches each child over a unix
// socket, so the child's socket peer is one value for every caller — the key the kms
// login, account and node limiters used to count by. IP read in the child is the
// caller: the host resolves it where the connection is and stamps it, and the child
// reads the stamp.
func TestTheChildSeesTheCallerNotTheSocket(t *testing.T) {
	type seen struct{ peer, caller string }
	got := make(chan seen, 4)
	child := zip.New(zip.Config{DisableStartupMessage: true})
	child.Raw(zip.MethodAll, "/v1/*", func(c *zip.Ctx) error {
		got <- seen{peer: Peer(c), caller: IP(c)}
		return c.NoContent(http.StatusNoContent)
	})
	dir, err := os.MkdirTemp("", "zc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	go func() { _ = child.Listen(sock) }()
	t.Cleanup(func() { _ = child.Shutdown() })
	for i := 0; ; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			break
		}
		if i > 100 {
			t.Fatalf("child never listened on %s", sock)
		}
		time.Sleep(20 * time.Millisecond)
	}

	remote, err := zip.Proxy("/v1", sock)
	if err != nil {
		t.Fatal(err)
	}
	host := zip.New(zip.Config{DisableStartupMessage: true})
	host.Use(zip.H(Stamp))
	host.Use(remote)

	call := func(chain, cf string) seen {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/kms/auth/login", nil)
		req.Header.Set("X-Forwarded-For", chain)
		if cf != "" {
			req.Header.Set(connectingHeader, cf)
		}
		resp, err := host.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		select {
		case s := <-got:
			return s
		case <-time.After(5 * time.Second):
			t.Fatal("the request never reached the child")
			return seen{}
		}
	}

	a := call("1.2.3.4, 203.0.113.10", "")
	b := call("203.0.113.11", "")
	v := call("1.2.3.4, 198.51.100.23, "+edge, "198.51.100.23")
	t.Logf("the child's socket peer for every caller: %q", a.peer)
	if a.peer != b.peer || a.peer != v.peer {
		t.Fatalf("the child's socket peer differs per caller (%q, %q, %q); the premise is gone", a.peer, b.peer, v.peer)
	}
	if a.caller != "203.0.113.10" || b.caller != "203.0.113.11" || v.caller != "198.51.100.23" {
		t.Fatalf("IP in the child = %q, %q, %q; want each caller", a.caller, b.caller, v.caller)
	}
}

// rig is the topology that ships — a host stamping in front of a child over a unix
// socket — and returns a call through it. peer, when set, is the address the host's
// connection reports: a caller that reached the host directly rather than through one
// of our proxies. read is what the child reports about the request it was handed.
func rig(t *testing.T, peer string, read func(*zip.Ctx) any) func(*http.Request) any {
	t.Helper()
	got := make(chan any, 1)
	child := zip.New(zip.Config{DisableStartupMessage: true})
	child.Raw(zip.MethodAll, "/v1/*", func(c *zip.Ctx) error {
		got <- read(c)
		return c.NoContent(http.StatusNoContent)
	})
	dir, err := os.MkdirTemp("", "zr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "r.sock")
	go func() { _ = child.Listen(sock) }()
	t.Cleanup(func() { _ = child.Shutdown() })
	for i := 0; ; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			break
		}
		if i > 100 {
			t.Fatalf("child never listened on %s", sock)
		}
		time.Sleep(20 * time.Millisecond)
	}
	remote, err := zip.Proxy("/v1", sock)
	if err != nil {
		t.Fatal(err)
	}
	host := zip.New(zip.Config{DisableStartupMessage: true})
	if peer != "" {
		addr := &net.TCPAddr{IP: net.ParseIP(peer), Port: 41000}
		host.Use(zip.H(func(c *zip.Ctx) error {
			c.Fiber().RequestCtx().SetRemoteAddr(addr)
			return c.Continue()
		}))
	}
	host.Use(zip.H(Stamp))
	host.Use(remote)
	return func(req *http.Request) any {
		t.Helper()
		resp, err := host.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		select {
		case v := <-got:
			return v
		case <-time.After(5 * time.Second):
			t.Fatal("the request never reached the child")
			return nil
		}
	}
}

// every is each line of one header, as the child holds them.
func every(c *zip.Ctx, name string) []string {
	var out []string
	for _, l := range c.Fiber().Request().Header.PeekAll(name) {
		out = append(out, string(l))
	}
	return out
}

// THE HOST'S STAMP IS THE ONLY LINE THAT CROSSES, for the address and the country.
//
// Header.Set replaces the first line under a name and keeps the rest, so a caller that
// sent two kept its second; and the country was never stamped and never removed, so a
// caller's own X-Client-Country reached ai as its jurisdiction — the EU training-data
// guard and the globe read it. Every caller line goes; the host writes one.
func TestTheStampIsTheOnlyLineThatCrosses(t *testing.T) {
	call := rig(t, "", func(c *zip.Ctx) any {
		return [2][]string{every(c, address.Header), every(c, address.Country)}
	})
	ask := func(chain, cf, country string, claims ...string) [2][]string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/public", nil)
		req.Header.Set("X-Forwarded-For", chain)
		if cf != "" {
			req.Header.Set(connectingHeader, cf)
			req.Header.Set(CountryHeader, country)
		}
		for _, c := range claims {
			req.Header.Add(address.Header, c)
			req.Header.Add(address.Country, "US")
		}
		return call(req).([2][]string)
	}

	got := ask("203.0.113.10", "", "", "198.51.100.1", "198.51.100.2")
	if len(got[0]) != 1 || got[0][0] != "203.0.113.10" {
		t.Errorf("the child held X-Client-Ip lines %q; want the host's one line, 203.0.113.10", got[0])
	}
	if len(got[1]) != 0 {
		t.Errorf("the child held X-Client-Country %q from a caller no edge placed; want none", got[1])
	}

	got = ask("198.51.100.23, "+edge, "198.51.100.23", "af", "198.51.100.1")
	if len(got[0]) != 1 || got[0][0] != "198.51.100.23" {
		t.Errorf("behind Cloudflare the child held X-Client-Ip %q; want 198.51.100.23", got[0])
	}
	if len(got[1]) != 1 || got[1][0] != "AF" {
		t.Errorf("behind Cloudflare the child held X-Client-Country %q; want the edge's one line, AF", got[1])
	}

	got = ask("", "", "", "198.51.100.1")
	if len(got[0]) != 1 || got[0][0] != "" || len(got[1]) != 0 {
		t.Errorf("an in-cluster caller's claims reached the child as %q; want one empty address and no country", got)
	}
}

// A CHILD READS THE HOST'S ANSWER, NEVER THE HEADERS THE HOST SAW.
//
// The child's peer is the host's socket, so re-reading the chain there answered for
// whatever reached the host. A caller that reached the host directly is the host's
// peer — the host names it exactly — but the child read its headers instead: no chain
// made it "", the in-cluster bucket the kms-operator logs in from, a forged chain named
// its own bucket per request, and a forged Cloudflare chain chose its own country.
func TestTheChildReadsTheHostsAnswer(t *testing.T) {
	call := rig(t, "203.0.113.50", func(c *zip.Ctx) any { return [2]string{IP(c), Country(c)} })
	for _, tc := range []struct{ name, chain, cf, country string }{
		{"no chain", "", "", ""},
		{"a forged chain", "198.51.100.9", "", ""},
		{"a forged Cloudflare chain", "198.51.100.23, " + edge, "198.51.100.23", "AF"},
	} {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/kms/auth/login", nil)
		if tc.chain != "" {
			req.Header.Set("X-Forwarded-For", tc.chain)
		}
		if tc.cf != "" {
			req.Header.Set(connectingHeader, tc.cf)
			req.Header.Set(CountryHeader, tc.country)
		}
		if got := call(req).([2]string); got != [2]string{"203.0.113.50", ""} {
			t.Errorf("%s: the child read caller %q country %q; want the host's answer, 203.0.113.50 and none",
				tc.name, got[0], got[1])
		}
	}
}

// The net/http form agrees: over a socket it is the host's stamp, one line or nothing.
func TestTheNetHTTPFormReadsTheHostsStampOverASocket(t *testing.T) {
	for _, tc := range []struct {
		lines []string
		want  string
	}{
		{[]string{"203.0.113.50"}, "203.0.113.50"},
		{[]string{""}, ""},
		{nil, ""},
		{[]string{"203.0.113.50", "198.51.100.9"}, ""},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "@"
		r.Header.Set("X-Forwarded-For", "198.51.100.9")
		for _, l := range tc.lines {
			r.Header.Add(address.Header, l)
		}
		if got := IPOf(r); got != tc.want {
			t.Errorf("stamp %q over a socket: IPOf = %q, want %q", tc.lines, got, tc.want)
		}
	}
}

// THE SUBDIVISION CROSSES ON THE COUNTRY'S RULE. Cloudflare's region code is read only
// from a Cloudflare hop, normalised, and the child reads the host's stamp alone: a
// caller's own X-Client-Region never crosses, since it would choose its consent regime.
func TestClientRegion(t *testing.T) {
	for _, tc := range []struct{ said, want string }{{"ca", "CA"}, {" NSW ", "NSW"}, {"75", "75"}, {"", ""}, {"CALI", ""}, {"C-A", ""}} {
		if got := regionCode(tc.said); got != tc.want {
			t.Errorf("regionCode(%q) = %q, want %q", tc.said, got, tc.want)
		}
	}
	call := rig(t, "", func(c *zip.Ctx) any { return every(c, RegionStamp) })
	ask := func(chain, cf, region string, claim string) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/project/tags", nil)
		req.Header.Set("X-Forwarded-For", chain)
		if cf != "" {
			req.Header.Set(connectingHeader, cf)
			req.Header.Set(RegionHeader, region)
		}
		if claim != "" {
			req.Header.Add(RegionStamp, claim)
		}
		got, _ := call(req).([]string)
		return got
	}
	if got := ask("198.51.100.23, "+edge, "198.51.100.23", "ca", "TX"); len(got) != 1 || got[0] != "CA" {
		t.Errorf("behind Cloudflare the child held X-Client-Region %q; want the edge's one line, CA", got)
	}
	if got := ask("203.0.113.10", "", "", "TX"); len(got) != 0 {
		t.Errorf("around Cloudflare the child held X-Client-Region %q from the caller; want none", got)
	}
	if got := ask("203.0.113.10", "203.0.113.10", "CA", ""); len(got) != 0 {
		t.Errorf("a caller's own CF-Region-Code crossed as %q; want none", got)
	}
}
