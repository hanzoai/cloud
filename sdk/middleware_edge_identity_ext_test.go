package cloud_test

// THE ADDRESS LIMIT IS FOR WHO HAS NOTHING ELSE TO BE LIMITED BY.
//
// The edge runs before identity and used to charge every request to one per-IP
// count, so a signed-in customer behind one address met the anonymous flood cap
// before its own key or org ceiling was asked. These drive the whole order a
// request meets — EdgeRateLimit, SanitizeIdentity against a stub IAM, then
// ScopeRateLimit — and pin each kind of caller to the limiter that is theirs:
// anonymous to the address, a credential that is nobody to the address on every
// bucket, a validated key to its own rate and its org's.

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/internal/host"
	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// chainApp is the edge at perIP a minute, the identity boundary over a stub IAM
// that knows the keys in scopes, and the scope ceiling with every org held to
// orgRPM (0 for the unconfigured default). It answers how many lookups IAM was asked.
func chainApp(t *testing.T, perIP, orgRPM int, scopes map[string]string) (*zip.App, *atomic.Int64) {
	t.Helper()
	stub := iamPolicyServer(t, scopes, map[string]string{"sk-revoked": "key_revoked"})
	ch := chainOf(t, perIP, orgRPM, stub.Config.Handler)
	return ch.app, ch.asked
}

// chain is the request path chainOf builds, with what a test reaches into: how many
// lookups IAM was asked, the key the JWKS publishes (to sign a real token), and the
// boundary (to read or seed its key cache).
type chain struct {
	app      *zip.App
	asked    *atomic.Int64
	key      *rsa.PrivateKey
	boundary *cloud.Boundary
}

// chainOf is chainApp over any IAM. Besides /v1/train/jobs it serves the billing
// surface ScopeRateLimit exempts and a live transcript's push address.
func chainOf(t *testing.T, perIP, orgRPM int, keys http.Handler) chain {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	v := cloud.BoundaryOf(&cloud.Config{IAMIssuer: testIssuer, Discovery: testIssuer + cloud.WellKnown, DiscoveryClient: oidctest.Client(testIssuer, jwksServer(t, &key.PublicKey).URL)})
	var asked atomic.Int64
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		keys.ServeHTTP(w, r)
	}))
	t.Cleanup(iam.Close)
	// Built as newIAMKeys builds it, refusals remembered for the same minute.
	v.FakeIAMKeys(iam.URL, iam.Client(), true)

	pol := staticPol(t, edge.Policy{PerIPRPM: perIP, WindowSec: 60, OrgRPM: orgRPM})
	app := zip.New(zip.Config{})
	app.Use(host.EdgeRateLimit(pol, v.Glance))
	app.Use(cloud.SanitizeIdentity(v))
	app.Use(host.ScopeRateLimit(nil, pol))
	ok := func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"org": c.Header("X-Org-Id")}) }
	app.Raw(http.MethodGet, "/v1/train/jobs", ok)
	app.Raw(http.MethodGet, "/v1/billing/balance", ok)
	app.Raw(http.MethodPost, "/v1/audio/transcript/:id", ok)
	return chain{app: app, asked: &asked, key: key, boundary: v}
}

// send1 is one request from addr on bearer, with any other headers in hdr.
func send1(t *testing.T, app *zip.App, method, path, addr, bearer string, hdr map[string]string) int {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-Forwarded-For", addr)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// from sends one request from addr, presenting bearer when it is not empty.
func from(t *testing.T, app *zip.App, addr, bearer string) *http.Response {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/train/jobs", nil)
	r.Header.Set("X-Forwarded-For", addr)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	return resp
}

// paced asserts resp states the ceiling limit — its limit, and a reset in unix
// seconds inside the window — and, refused, when to come back.
func paced(t *testing.T, what string, resp *http.Response, status, limit int) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("%s: status %d, want %d", what, resp.StatusCode, status)
	}
	if got := resp.Header.Get("X-RateLimit-Limit"); got != strconv.Itoa(limit) {
		t.Fatalf("%s: X-RateLimit-Limit %q, want %d — the header is not the ceiling that applies", what, got, limit)
	}
	reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil || reset < time.Now().Unix() || reset > time.Now().Add(61*time.Second).Unix() {
		t.Fatalf("%s: X-RateLimit-Reset %q, want unix seconds inside the minute", what, resp.Header.Get("X-RateLimit-Reset"))
	}
	if status == http.StatusTooManyRequests {
		if wait, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || wait < 1 || wait > 60 {
			t.Fatalf("%s: Retry-After %q, want 1..60", what, resp.Header.Get("Retry-After"))
		}
	}
}

// Anonymous traffic is exactly what it was: the per-IP limit, refused past it, with
// the per-IP ceiling in the headers.
func TestAnonymousMeetsTheAddressLimit(t *testing.T) {
	app, _ := chainApp(t, 3, 0, nil)
	for i := range 3 {
		paced(t, fmt.Sprintf("anonymous request %d", i+1), from(t, app, "203.0.113.7", ""), http.StatusOK, 3)
	}
	paced(t, "anonymous request 4", from(t, app, "203.0.113.7", ""), http.StatusTooManyRequests, 3)
}

// A SIGNED-IN CUSTOMER IS NOT AN ADDRESS. A valid key sends five times the per-IP
// limit from one address, well under its own rate, and every request is served —
// paced by its own ceiling, never the address's. Past its own rate it is refused,
// naming its own limit.
//
// MUTATION: charge a credentialed request the per-IP limit at the edge again and
// the 4th request is a 429 naming 3.
func TestAValidKeyIsLimitedByItselfNotItsAddress(t *testing.T) {
	// The org carries its default cap (30 a minute here), above the key's 20, so the
	// key's own rate is the ceiling that applies.
	app, _ := chainApp(t, 3, 0, map[string]string{"sk-fast": "rate:minute:20", "sk-slow": "rate:minute:2"})
	for i := range 15 {
		paced(t, fmt.Sprintf("valid key request %d from one address", i+1), from(t, app, "198.51.100.20", "sk-fast"), http.StatusOK, 20)
	}

	paced(t, "slow key request 1", from(t, app, "198.51.100.21", "sk-slow"), http.StatusOK, 2)
	paced(t, "slow key request 2", from(t, app, "198.51.100.21", "sk-slow"), http.StatusOK, 2)
	paced(t, "slow key request 3", from(t, app, "198.51.100.21", "sk-slow"), http.StatusTooManyRequests, 2)
	// The address was never spent: anonymous traffic from it has its whole limit.
	paced(t, "anonymous beside the spent key", from(t, app, "198.51.100.21", ""), http.StatusOK, 3)
}

// The headers describe the ceiling with the least room, whichever ran last: a key
// held to 5 inside an org held to 100 is shown its 5, and a key held to 100 inside
// an org held to 4 is shown the org's 4 — and refused by it.
func TestTheTightestCeilingSpeaks(t *testing.T) {
	app, _ := chainApp(t, 3, 100, map[string]string{"sk-five": "rate:minute:5"})
	paced(t, "a 5/min key in a 100/min org", from(t, app, "198.51.100.30", "sk-five"), http.StatusOK, 5)

	app, _ = chainApp(t, 3, 4, map[string]string{"sk-wide": "rate:minute:100"})
	for i := range 4 {
		paced(t, fmt.Sprintf("a 100/min key in a 4/min org, request %d", i+1), from(t, app, "198.51.100.31", "sk-wide"), http.StatusOK, 4)
	}
	paced(t, "the org's 5th", from(t, app, "198.51.100.31", "sk-wide"), http.StatusTooManyRequests, 4)
}

// A CREDENTIAL THAT IS NOBODY IS AN ADDRESS, AND A STRIKE. Unknown keys, a revoked
// key and a forged JWT from one address meet the per-IP limit exactly as anonymous
// traffic does — in the same count — and each is a strike against the address. Once
// it has spent its limit of strikes, every credential it presents is refused at the
// edge before identity asks IAM anything, valid or not, until the window resets: a
// header buys no way past the limit, and no more lookups either.
//
// MUTATION: drop the charge from SanitizeIdentity's no-principal exits and the
// flood runs to the edge's backstop, ten times the limit; drop the strike bar and
// IAM is asked for every junk key up to the backstop.
func TestAnInvalidCredentialFloodMeetsTheAddressLimit(t *testing.T) {
	app, asked := chainApp(t, 3, 0, map[string]string{"sk-good": ""})
	junk := []string{"sk-unknown-1", "sk-revoked", "eyJhbGciOiJSUzI1NiJ9.e30.sig"}
	for i, cred := range junk {
		resp := from(t, app, "192.0.2.50", cred)
		want := http.StatusOK
		if cred == "sk-revoked" {
			want = http.StatusUnauthorized // IAM's own refusal, still charged
		}
		paced(t, fmt.Sprintf("invalid credential %d", i+1), resp, want, 3)
	}
	if n := asked.Load(); n != 2 {
		t.Fatalf("IAM was asked %d times for two keys (the JWT is checked here)", n)
	}
	paced(t, "the 4th invalid credential", from(t, app, "192.0.2.50", "sk-unknown-2"), http.StatusTooManyRequests, 3)
	paced(t, "anonymous after the flood, same count", from(t, app, "192.0.2.50", ""), http.StatusTooManyRequests, 3)
	// The address is struck: even a valid key from it waits, because finding out it is
	// valid is the lookup the bar stops.
	if code := from(t, app, "192.0.2.50", "sk-good").StatusCode; code != http.StatusTooManyRequests {
		t.Fatalf("a credential from a struck address: %d, want 429 before identity", code)
	}
	if n := asked.Load(); n != 2 {
		t.Fatalf("a struck address still reached IAM: asked %d times, want 2", n)
	}
	if code := from(t, app, "192.0.2.51", "sk-good").StatusCode; code != http.StatusOK {
		t.Fatalf("a valid key from another address: %d, want 200", code)
	}
}

// An invalid-credential flood is struck on its /48 too: sixteen /64s spending their
// own limits on junk strike the site at sixteen limits, and from then on nothing
// presented from the site reaches IAM — not a 17th /64's junk, and not a valid key —
// while the next /48 over is untouched.
func TestAnInvalidCredentialFloodIsStruckOnItsSlash48(t *testing.T) {
	app, asked := chainApp(t, 2, 0, map[string]string{"sk-good": ""})
	for i := range 16 {
		for n := range 2 {
			addr := fmt.Sprintf("2001:470:1f00:%x::1", i)
			if code := from(t, app, addr, fmt.Sprintf("sk-junk-%d-%d", i, n)).StatusCode; code != http.StatusOK {
				t.Fatalf("/64 %d junk request %d: %d, want 200 inside its own limit", i+1, n+1, code)
			}
		}
	}
	if n := asked.Load(); n != 32 {
		t.Fatalf("IAM asked %d times for 32 junk keys", n)
	}
	paced(t, "junk from a 17th /64 of a struck /48", from(t, app, "2001:470:1f00:ffff::1", "sk-junk-17"), http.StatusTooManyRequests, 32)
	if code := from(t, app, "2001:470:1f00:fffe::1", "sk-good").StatusCode; code != http.StatusTooManyRequests {
		t.Fatalf("a credential from the struck /48: %d, want 429 before identity", code)
	}
	if n := asked.Load(); n != 32 {
		t.Fatalf("the struck /48 still reached IAM: %d lookups, want 32", n)
	}
	if code := from(t, app, "2001:470:1f01::1", "sk-good").StatusCode; code != http.StatusOK {
		t.Fatalf("a valid key from the next /48: %d, want 200", code)
	}
}

// ONE ADDRESS, FIFTY JUNK KEYS, AND IAM IS ASKED THE LIMIT. Every unknown key is a
// lookup and a cache entry; the address is struck at the per-IP limit, so the rest
// are refused before identity and cost neither.
func TestJunkKeysCostIAMOnlyTheLimit(t *testing.T) {
	app, asked := chainApp(t, 3, 0, nil)
	codes := map[int]int{}
	for i := range 50 {
		codes[from(t, app, "203.0.113.9", fmt.Sprintf("sk-junk-%d", i)).StatusCode]++
	}
	if n := asked.Load(); n != 3 {
		t.Fatalf("50 junk keys from one address at a limit of 3 asked IAM %d times, want 3 (statuses %v)", n, codes)
	}
	if codes[http.StatusOK] != 3 || codes[http.StatusTooManyRequests] != 47 {
		t.Fatalf("statuses %v, want 3 served anonymous and 47 refused", codes)
	}
}

// A MEMBER'S KEY PAST ITS OWN RATE COSTS NO LOOKUP. A member's key is asked of IAM
// on every request, so that removing the membership ends it at once; once its own
// ceiling refuses it, the edge refuses it for the rest of the window, and IAM is
// asked for the requests served and the one refused, not for every one sent.
func TestAMemberKeyPastItsRateCostsNoLookups(t *testing.T) {
	var asked atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		row := r.URL.Query().Get("accessKey") + "-row"
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"home","name":"bob","org":"acme","scope":"rate:minute:2","key":"` + row + `"}}`))
	}))
	t.Cleanup(srv.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	v := cloud.BoundaryOf(&cloud.Config{IAMIssuer: testIssuer, Discovery: testIssuer + cloud.WellKnown, DiscoveryClient: oidctest.Client(testIssuer, jwksServer(t, &key.PublicKey).URL)})
	v.FakeIAMKeys(srv.URL, srv.Client(), false)
	pol := staticPol(t, edge.Policy{PerIPRPM: 3, WindowSec: 60})
	app := zip.New(zip.Config{})
	app.Use(host.EdgeRateLimit(pol, v.Glance))
	app.Use(cloud.SanitizeIdentity(v))
	app.Use(host.ScopeRateLimit(nil, pol))
	app.Raw(http.MethodGet, "/v1/train/jobs", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) })
	codes := map[int]int{}
	for range 50 {
		codes[from(t, app, "203.0.113.40", "sk-member").StatusCode]++
	}
	if n := asked.Load(); n != 3 {
		t.Fatalf("a member key held to 2 a minute, 50 requests: IAM asked %d times, want 3 (statuses %v)", n, codes)
	}
	if codes[http.StatusOK] != 2 || codes[http.StatusTooManyRequests] != 48 {
		t.Fatalf("statuses %v, want 2 served and 48 refused", codes)
	}
	// The bar is the credential's own: another member key from the same address is
	// asked about and served.
	if code := from(t, app, "203.0.113.40", "sk-member-2").StatusCode; code != http.StatusOK {
		t.Fatalf("another key from the same address: %d, want 200", code)
	}
}

// A STRUCK ADDRESS STILL SERVES WHOEVER IT CAN TELL WITHOUT ASKING. The strike bar
// exists to stop lookups, so it refuses only what would need one: an uncached key,
// a forged token. A key whose answer is cached and a JWT verified against the cached
// JWKS cost no lookup and are served — the signed-in neighbours of a junk flood are
// not its casualties.
//
// MUTATION: refuse every credential while struck and the cached key and the JWT are
// 429s.
func TestAStruckAddressServesWhoItNeedNotAsk(t *testing.T) {
	stub := iamPolicyServer(t, map[string]string{"sk-good": "", "sk-cold": ""}, nil)
	ch := chainOf(t, 3, 1000, stub.Config.Handler)
	app, asked, key := ch.app, ch.asked, ch.key
	if code := from(t, app, "198.51.100.60", "sk-good").StatusCode; code != http.StatusOK {
		t.Fatalf("sk-good from a clean address: %d", code)
	}
	for i := range 3 {
		from(t, app, "192.0.2.80", fmt.Sprintf("sk-junk-%d", i))
	}
	before := asked.Load()
	if code := from(t, app, "192.0.2.80", "sk-good").StatusCode; code != http.StatusOK {
		t.Fatalf("a key whose answer is cached, from a struck address: %d, want 200", code)
	}
	if code := from(t, app, "192.0.2.80", e2eToken(t, key, "acme", "dana")).StatusCode; code != http.StatusOK {
		t.Fatalf("a valid JWT from a struck address: %d, want 200", code)
	}
	if code := from(t, app, "192.0.2.80", "sk-cold").StatusCode; code != http.StatusTooManyRequests {
		t.Fatalf("a key IAM would have to be asked about, from a struck address: %d, want 429", code)
	}
	if code := from(t, app, "192.0.2.80", "eyJhbGciOiJSUzI1NiJ9.e30.sig").StatusCode; code != http.StatusTooManyRequests {
		t.Fatalf("a forged JWT from a struck address: %d, want 429", code)
	}
	if n := asked.Load(); n != before {
		t.Fatalf("a struck address reached IAM: %d lookups after the bar, want 0", n-before)
	}
}

// AN IAM THAT DOES NOT ANSWER IS NO STRIKE. Keys presented while IAM is answering
// 503 resolve to nobody — and are counted as the address's anonymous traffic — but
// they say nothing about the address, so it is not struck: once IAM is back, a key
// it has never seen from that address is asked about and served.
//
// MUTATION: strike on every no-principal exit and the key after the outage is a 429.
func TestAnUnansweringIAMIsNoStrike(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	stub := iamPolicyServer(t, map[string]string{"sk-good": ""}, nil)
	app := chainOf(t, 3, 1000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		stub.Config.Handler.ServeHTTP(w, r)
	})).app
	for i := range 10 {
		from(t, app, "192.0.2.90", fmt.Sprintf("sk-during-outage-%d", i))
	}
	down.Store(false)
	if code := from(t, app, "192.0.2.90", "sk-good").StatusCode; code != http.StatusOK {
		t.Fatalf("a valid key after an IAM outage, from an address that sent keys during it: %d, want 200", code)
	}
}

// AN ORG'S CEILING DOES NOT BAR THE KEY. Only a key's own rate binds every request
// the key makes; its org's ceiling does not bind the billing surface, so a refusal
// by the org's ceiling leaves the key free there — and nothing about it is held at
// the edge. A member's key, whose answer is never cached, is the one a bar would
// have held: the edge cannot see its rate to know better.
//
// MUTATION: hold the credential on any refusal and the billing read is a 429.
func TestAnOrgRefusalDoesNotBarTheKey(t *testing.T) {
	member := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"home","name":"bob","org":"acme","scope":"rate:minute:100","key":"m-row"}}`))
	})
	app := chainOf(t, 3, 2, member).app
	for i := range 2 {
		if code := send1(t, app, http.MethodGet, "/v1/train/jobs", "198.51.100.70", "sk-member", nil); code != http.StatusOK {
			t.Fatalf("request %d of the org's 2: %d", i+1, code)
		}
	}
	if code := send1(t, app, http.MethodGet, "/v1/train/jobs", "198.51.100.70", "sk-member", nil); code != http.StatusTooManyRequests {
		t.Fatalf("the org's 3rd: %d, want 429", code)
	}
	if code := send1(t, app, http.MethodGet, "/v1/billing/balance", "198.51.100.70", "sk-member", nil); code != http.StatusOK {
		t.Fatalf("the billing surface, which the org's ceiling does not bind, after the org's refusal: %d, want 200", code)
	}
}

// A KEY'S BAR IS THE CREDENTIAL'S ALONE. Naming another org on each request does
// not reopen it: a member's key past its own rate costs IAM nothing more, whatever
// X-Org-Id says.
//
// MUTATION: key the bar on the credential and X-Org-Id and each new org is a lookup.
func TestAKeysBarIsTheCredentialsAlone(t *testing.T) {
	var asked atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"home","name":"bob","org":"acme","scope":"rate:minute:2","key":"m-row"}}`))
	}))
	t.Cleanup(srv.Close)
	app := chainOf(t, 3, 1000, srv.Config.Handler).app
	for range 3 {
		send1(t, app, http.MethodGet, "/v1/train/jobs", "203.0.113.41", "sk-member", nil)
	}
	before := asked.Load()
	for i := range 20 {
		hdr := map[string]string{"X-Org-Id": fmt.Sprintf("org-%d", i)}
		if code := send1(t, app, http.MethodGet, "/v1/train/jobs", "203.0.113.41", "sk-member", hdr); code != http.StatusTooManyRequests {
			t.Fatalf("a barred key naming org-%d: %d, want 429", i, code)
		}
	}
	if n := asked.Load() - before; n != 0 {
		t.Fatalf("rotating X-Org-Id reopened the bar: %d lookups", n)
	}
}

// A LIVE TRANSCRIPT'S PUSHES ARE ONE REQUEST, for the key's own ceiling too. A push
// or close on a session the key opened is counted by no ceiling (SetAdmitted) — so
// pushes spend nothing of the key's rate, and a key its rate has barred still
// finishes the dictation it is in.
//
// MUTATION: charge the key before asking whether the request continues a session,
// and the pushes spend the rate; skip continues at the edge and the last push is a
// 429.
func TestATranscriptPushIsNeverBarred(t *testing.T) {
	host.SetAdmitted(func(method, path, auth string) bool {
		return method == http.MethodPost && path == "/v1/audio/transcript/live" && auth == "Bearer sk-slow"
	})
	t.Cleanup(func() { host.SetAdmitted(nil) })
	app, _ := chainApp(t, 3, 1000, map[string]string{"sk-slow": "rate:minute:2"})
	push := func() int {
		return send1(t, app, http.MethodPost, "/v1/audio/transcript/live", "198.51.100.71", "sk-slow", nil)
	}
	for i := range 5 {
		if code := push(); code != http.StatusOK {
			t.Fatalf("push %d: %d", i+1, code)
		}
	}
	for i := range 2 {
		if code := send1(t, app, http.MethodGet, "/v1/train/jobs", "198.51.100.71", "sk-slow", nil); code != http.StatusOK {
			t.Fatalf("request %d of the key's 2 after five pushes: %d — the pushes spent the rate", i+1, code)
		}
	}
	if code := send1(t, app, http.MethodGet, "/v1/train/jobs", "198.51.100.71", "sk-slow", nil); code != http.StatusTooManyRequests {
		t.Fatalf("the key's 3rd: %d, want 429", code)
	}
	if code := push(); code != http.StatusOK {
		t.Fatalf("a push on the barred key's live session: %d, want 200", code)
	}
}

// A RAISED LIMIT LIFTS A REMEMBERED REFUSAL AT ONCE. A refusal is remembered with
// the limit it was counted at, and only while that limit stands: the edge's bar on a
// key lifts as soon as the boundary's answer for the key carries a higher rate, and
// the limiter asks the count again once an org's ceiling is raised.
//
// MUTATION: drop either limit comparison and the request after the raise is a 429.
func TestARaisedLimitLiftsARememberedRefusal(t *testing.T) {
	t.Run("a key's rate, at the edge", func(t *testing.T) {
		stub := iamPolicyServer(t, map[string]string{"sk-slow": "rate:minute:2"}, nil)
		ch := chainOf(t, 3, 1000, stub.Config.Handler)
		for range 2 {
			send1(t, ch.app, http.MethodGet, "/v1/train/jobs", "198.51.100.72", "sk-slow", nil)
		}
		if code := send1(t, ch.app, http.MethodGet, "/v1/train/jobs", "198.51.100.72", "sk-slow", nil); code != http.StatusTooManyRequests {
			t.Fatalf("the key's 3rd: %d, want 429", code)
		}
		// The key's rate is raised to 5, and the boundary has read the new answer.
		ch.boundary.RaiseKeyRate("sk-slow", 5)
		if code := send1(t, ch.app, http.MethodGet, "/v1/train/jobs", "198.51.100.72", "sk-slow", nil); code != http.StatusOK {
			t.Fatalf("the key after its rate was raised to 5: %d, want 200", code)
		}
	})
	t.Run("an org's ceiling, in the limiter", func(t *testing.T) {
		st, err := edge.New(t.TempDir(), "admin", edge.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if _, err := st.Put(t.Context(), "acme", edge.Policy{OrgRPM: 2}); err != nil {
			t.Fatal(err)
		}
		p := limiterProcess(t, coordtest.Memory(t)("cloud-0"), st)
		for range 2 {
			p.send(t, "acme", "", "")
		}
		if code := p.send(t, "acme", "", "").StatusCode; code != http.StatusTooManyRequests {
			t.Fatalf("the org's 3rd: %d, want 429", code)
		}
		if _, err := st.Put(t.Context(), "acme", edge.Policy{OrgRPM: 5}); err != nil {
			t.Fatal(err)
		}
		if code := p.send(t, "acme", "", "").StatusCode; code != http.StatusOK {
			t.Fatalf("the org after its ceiling was raised to 5: %d, want 200", code)
		}
	})
}

// ORDINARY TRAFFIC IS NO STRIKE. A strike means an address presented a credential
// that was checked and found to be nobody. A publishable key (shipped in browser
// bundles), a key on Api-Key or ?api_key= (event ingest's spellings, which identity
// never reads) and an expired session (a browser holding yesterday's cookie) are
// none of that, so they never bar the address: a valid key from it, one IAM has
// never been asked about, is served.
//
// MUTATION: strike on every no-principal exit, or count pk-/Api-Key/?api_key= as
// presented credentials, and the last request is a 429 that never reached IAM.
func TestOrdinaryTrafficIsNoStrike(t *testing.T) {
	stub := iamPolicyServer(t, map[string]string{"sk-cold": ""}, nil)
	ch := chainOf(t, 3, 1000, stub.Config.Handler)
	expired := signWith(t, ch.key, tokenClaims("hanzo-console", "acme", "eve@acme.io", false, time.Now().Add(-time.Hour)))
	const addr = "198.51.100.200"
	for range 3 {
		send1(t, ch.app, http.MethodGet, "/v1/train/jobs", addr, "pk-site-beacon", nil)
		send1(t, ch.app, http.MethodGet, "/v1/train/jobs", addr, "", map[string]string{"Api-Key": "proj-key"})
		send1(t, ch.app, http.MethodGet, "/v1/train/jobs?api_key=proj-key", addr, "", nil)
		send1(t, ch.app, http.MethodGet, "/v1/train/jobs", addr, "", map[string]string{"Cookie": "hanzo_iam_token=" + expired})
	}
	before := ch.asked.Load()
	if code := from(t, ch.app, addr, "sk-cold").StatusCode; code != http.StatusOK {
		t.Fatalf("a valid uncached key after ordinary traffic from its address: %d, want 200", code)
	}
	if n := ch.asked.Load() - before; n != 1 {
		t.Fatalf("IAM asked %d times about the valid key, want 1", n)
	}
}

// AN IAM THAT THROTTLES IS NO ANSWER. A 4xx IAM gives about cloud rather than the
// key — a throttle, a refusal of cloud's own credential — carries no key_* reason,
// so it is neither remembered as the key's answer nor held against the address:
// once IAM is back, the same key and a fresh one from the same address resolve.
//
// MUTATION: read any IAM refusal as the key's and both are refused for the window.
func TestAnIAMThatThrottlesIsNoAnswer(t *testing.T) {
	var throttled atomic.Bool
	throttled.Store(true)
	stub := iamPolicyServer(t, map[string]string{"sk-good": "", "sk-fresh": ""}, nil)
	ch := chainOf(t, 3, 1000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if throttled.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"status":"error","msg":"too many requests"}`))
			return
		}
		stub.Config.Handler.ServeHTTP(w, r)
	}))
	from(t, ch.app, "203.0.113.50", "sk-good")
	for i := range 3 {
		from(t, ch.app, "203.0.113.51", fmt.Sprintf("sk-good-%d", i))
	}
	if known, _ := ch.boundary.KnownKey("sk-good"); known {
		t.Fatal("a throttle was remembered as the key's answer")
	}
	throttled.Store(false)
	if code := from(t, ch.app, "203.0.113.50", "sk-good").StatusCode; code != http.StatusOK {
		t.Fatalf("a valid key after IAM stopped throttling: %d, want 200", code)
	}
	if _, principal := ch.boundary.KnownKey("sk-good"); !principal {
		t.Fatal("the valid key did not resolve once IAM stopped throttling")
	}
	if code := from(t, ch.app, "203.0.113.51", "sk-fresh").StatusCode; code != http.StatusOK {
		t.Fatalf("a fresh valid key from the address that met the throttle: %d, want 200 — the throttle struck it", code)
	}
}

// staticPol is an edge policy store with no file behind it.
func staticPol(t *testing.T, p edge.Policy) *edge.Store {
	t.Helper()
	s, _ := edge.New("", "admin", p)
	return s
}

const testIssuer = "https://test.iam"

// jwksServer serves a single-key JWKS (kid=test-key) for pub.
func jwksServer(t *testing.T, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// signWith signs claims with key, stamping kid=test-key.
func signWith(t *testing.T, key *rsa.PrivateKey, c idClaims) string {
	t.Helper()
	// golang-jwt, the library IAM SIGNS with and hanzoai/authz verifies with. Minting
	// with a different one certifies a reader nobody runs.
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c.Claims)
	tok.Header["kid"] = "test-key"
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

// tokenClaims builds an IAM-shaped claim set for the ORDINARY case: the minting
// app's org and the user's own org AGREE, so `owner` and orgs[0] carry the same
// value.
//
// The `orgs` seed is not decoration — it is what makes this fixture resemble a real
// token. IAM has minted the claim since v1.33.0, and cloud reads the USER's org from
// it (idClaims.homeOrg), never from `owner`, which carries the APPLICATION's org.
// Every fixture here previously set `owner` alone, so the whole suite exercised a
// token shape production no longer emits — and, because the two values were always
// equal in the fixtures, no assertion could distinguish them. That is exactly how a
// caller-selectable tenant and a caller-selectable SuperAdmin gate survived.
//
// Tests that need the two to DISAGREE (the cross-app cases in
// middleware_identity_homeorg_test.go) override Orgs explicitly, as does any test
// modelling a pre-claim legacy token by clearing it.
func tokenClaims(aud, owner, email string, isAdmin bool, exp time.Time) idClaims {
	// The home's role is IAM's (store.HomeRole): admin for an account that
	// administers its own org, member otherwise.
	role := authz.Member
	if isAdmin {
		role = authz.Admin
	}
	c := idClaims{
		Owner:     owner,
		Email:     email,
		IsAdmin:   isAdmin,
		Orgs:      []authz.Membership{{Org: owner, Role: role}},
		Issuer:    testIssuer,
		Subject:   "u-" + owner,
		Audience:  jwt.ClaimStrings{aud},
		ExpiresAt: jwt.NewNumericDate(exp),
		IssuedAt:  jwt.NewNumericDate(time.Now())}
	return c
}

// e2eToken mints a real RS256 bearer for one person in one org.
func e2eToken(t *testing.T, key *rsa.PrivateKey, owner, name string, orgs ...string) string {
	t.Helper()
	c := tokenClaims("hanzo-console", owner, name+"@"+owner+".io", false, time.Now().Add(time.Hour))
	c.Name = name
	if len(orgs) > 0 {
		// home first, as IAM signs it; every org after it the person founded.
		c.Orgs = []authz.Membership{{Org: orgs[0], Role: authz.Member}}
		for _, o := range orgs[1:] {
			c.Orgs = append(c.Orgs, authz.Membership{Org: o, Role: authz.Owner})
		}
	}
	return signWith(t, key, c)
}

// iamPolicyServer answers keys/principal per presented key: a live key resolves
// to acme/alice with its scope, a dead one is refused with IAM's reason code.
func iamPolicyServer(t *testing.T, scopes map[string]string, dead map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key := r.URL.Query().Get("accessKey")
		if why, ok := dead[key]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist","code":"` + why + `"}`))
			return
		}
		scope, ok := scopes[key]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist","code":"key_unknown"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"acme","name":"alice","org":"acme","scope":"` + scope + `","key":"` + key + `-row"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// limiterProcess is a ScopeRateLimit over gp in front of /v1/train/jobs. A request
// carrying X-Test-Key arrives on that key, held to rate:minute:<X-Test-Rate>.
func limiterProcess(t *testing.T, co *coord.Coord, gp *edge.Store) process {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(zip.H(func(c *zip.Ctx) error {
		p := principal.Principal{Org: c.Header("X-Org-Id"), User: c.Header("X-User-Id")}
		if k := c.Header("X-Test-Key"); k != "" {
			rate, _ := strconv.Atoi(c.Header("X-Test-Rate"))
			p.Kind, p.Key, p.Limit = principal.Key, k, cloud.Grant(nil).Policy(0, 0, rate)
		}
		principal.Mint(c, p)
		return c.Continue()
	}))
	app.Use(host.ScopeRateLimit(nil, gp))
	app.Raw(http.MethodGet, "/v1/train/jobs", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) })
	return process{app: app, co: co}
}

// process is one process's ScopeRateLimit and the Coord it counts through.
type process struct {
	app *zip.App
	co  *coord.Coord
}

// send is one request through p, counted through p's Coord.
func (p process) send(t *testing.T, org, key, rate string) *http.Response {
	t.Helper()
	was := coord.Default()
	coord.SetDefault(p.co)
	defer coord.SetDefault(was)
	r := httptest.NewRequest(http.MethodGet, "/v1/train/jobs", nil)
	r.Header.Set("X-Org-Id", org)
	r.Header.Set("X-User-Id", org+"/ann")
	if key != "" {
		r.Header.Set("X-Test-Key", key)
		r.Header.Set("X-Test-Rate", rate)
	}
	resp, err := p.app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	return resp
}

// idClaims is what the tests sign: IAM's own claim set.
type idClaims struct{ authz.Claims }
