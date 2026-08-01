package cloud

// The agency table IS the specification of the differentiator. It is a pure
// function of two values, so this table is total: there is no fifth input, no
// clock and no I/O that could make the classification depend on anything else.

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/zap-proto/zip"
)

func TestAgency(t *testing.T) {
	quiet := edge.Pattern{Requests: 3, Paths: 2}
	stuffing := edge.Pattern{Requests: 40, Peers: stuffPeers}
	guessing := edge.Pattern{Requests: 40, Failures: guessFailures}
	sweeping := edge.Pattern{Requests: 40, Paths: sweepPaths}

	cases := []struct {
		class string
		p     edge.Pattern
		want  string
		why   string
	}{
		{CredSecret, quiet, AgencyAgent, "a machine credential we issued is attributable — that is the agent lane"},
		{CredSecret, sweeping, AgencyAgent, "an agent walking many endpoints is an agent doing its job, not a scraper"},
		{CredSecret, guessing, AgencyAgent, "a failing agent key is still an agent key; the pattern is the scorer's to weigh"},
		{CredSession, quiet, AgencyHuman, "a browser session is a person"},
		{CredSession, sweeping, AgencyHuman, "a busy person is still a person"},
		{CredAnonymous, quiet, AgencyUnknown, "anonymous is not malicious — every new integration starts here"},
		{CredPublishable, quiet, AgencyUnknown, "a publishable key names a tenant, not a caller; unremarkable use is unremarkable"},
		{CredAnonymous, stuffing, AgencyBot, "one address, many credentials, no attribution: stuffing"},
		{CredAnonymous, guessing, AgencyBot, "a wall of refusals from an unattributable caller: guessing"},
		{CredAnonymous, sweeping, AgencyBot, "an unattributable caller walking the map: scraping"},
		{CredPublishable, stuffing, AgencyBot, "a copied publishable key behaves the same way and is classed the same way"},
		{"", quiet, AgencyUnknown, "an unrecognised class is unknown, never a lane with consequences"},
	}
	for _, tc := range cases {
		if got := agency(tc.class, tc.p); got != tc.want {
			t.Errorf("agency(%q, %+v) = %q, want %q — %s", tc.class, tc.p, got, tc.want, tc.why)
		}
	}
}

// The whole claim of the differentiator is that the classification reads OUR
// issuance and not the client's self-description. If a user-agent string could
// move a caller between lanes, the classification would be worth nothing.
func TestCredentialClass_ReadsTheCredentialNotTheClient(t *testing.T) {
	cases := []struct {
		name   string
		org    string
		auth   string
		apiKey string
		ua     string
		want   string
	}{
		{"validated secret key", "acme", "Bearer sk-live-1", "", "curl/8", CredSecret},
		{"validated hanzo key", "acme", "Bearer hk-live-1", "", "", CredSecret},
		{"validated session bearer", "acme", "Bearer eyJhbGciOi.payload.sig", "", "Mozilla/5.0", CredSession},
		{"publishable key, org resolved", "acme", "Bearer pk-live-1", "", "", CredPublishable},
		{"publishable key, no org", "", "Bearer pk-live-1", "", "", CredPublishable},
		{"secret-shaped but unvalidated", "", "Bearer sk-live-1", "", "", CredAnonymous},
		{"session-shaped but unvalidated", "", "Bearer eyJhbGciOi.payload.sig", "", "", CredAnonymous},
		{"no credential at all", "", "", "", "Mozilla/5.0", CredAnonymous},
		{"key in the X-Api-Key header", "acme", "", "sk-live-1", "", CredSecret},
		{"a browser user-agent cannot make a key a session", "acme", "Bearer sk-live-1", "", "Mozilla/5.0 Chrome/126", CredSecret},
		{"a curl user-agent cannot make a session a key", "acme", "Bearer eyJhbGciOi.p.s", "", "curl/8.7", CredSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			app := zip.New(zip.Config{})
			app.Get("/probe", func(c *zip.Ctx) error {
				got = credentialClass(c)
				return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
			})
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			if tc.org != "" {
				req.Header.Set("X-Org-Id", tc.org)
			}
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			if tc.apiKey != "" {
				req.Header.Set("X-Api-Key", tc.apiKey)
			}
			if tc.ua != "" {
				req.Header.Set("User-Agent", tc.ua)
			}
			if _, err := app.Fiber().Test(req); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("credentialClass = %q, want %q", got, tc.want)
			}
		})
	}
}

// A fingerprint must identify a caller within a process and be useless outside
// one. The salt is per-process and keyed, so a published fingerprint cannot be
// tested against a candidate key anywhere else.
func TestFingerprint(t *testing.T) {
	if Fingerprint("") != "" {
		t.Fatal("no credential must fingerprint to nothing, not to a constant every anonymous caller shares")
	}
	a, b := Fingerprint("sk-live-1"), Fingerprint("sk-live-1")
	if a != b {
		t.Fatal("a fingerprint must be stable within a process")
	}
	if a == Fingerprint("sk-live-2") {
		t.Fatal("two credentials must not share a fingerprint")
	}
	if len(a) != fingerprintLen {
		t.Fatalf("fingerprint length = %d, want %d", len(a), fingerprintLen)
	}
	// It must not be a bare digest of the credential: with a bare digest anyone
	// holding a candidate key could confirm it against a published fingerprint.
	if a == unsaltedDigest("sk-live-1") {
		t.Fatal("the fingerprint is an unsalted digest — a published one would be brute-forceable")
	}
	// Whitespace is not a second identity for the same key.
	if Fingerprint("  sk-live-1  ") != a {
		t.Fatal("a padded credential must fingerprint to the same caller")
	}
}

// unsaltedDigest is what a NAIVE implementation would produce. It exists only so
// the test above can assert we did not write that one.
func unsaltedDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:fingerprintLen]
}
