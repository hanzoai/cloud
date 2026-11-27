package cloud

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// idp is a fake identity provider on a loopback port: its discovery document and its
// key set are whatever the test last set, and it counts what it was asked.
type idp struct {
	srv *httptest.Server

	mu       sync.Mutex
	issuer   string   // what the document claims; empty is the server's own URL
	jwks     string   // jwks_uri the document names; empty is this server's /jwks
	keys     []jwkKey // what /jwks serves
	down     bool     // answer 503
	docHits  atomic.Int64
	keyHits  atomic.Int64
	jwksPath string
}

type jwkKey struct {
	kid string
	pub *rsa.PublicKey
}

func newIdP(t *testing.T) *idp {
	t.Helper()
	p := &idp{jwksPath: "/jwks"}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case WellKnown:
			p.docHits.Add(1)
			iss, jwks := p.issuer, p.jwks
			if iss == "" {
				iss = p.srv.URL
			}
			if jwks == "" {
				jwks = p.srv.URL + p.jwksPath
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": iss, "jwks_uri": jwks})
		case p.jwksPath:
			p.keyHits.Add(1)
			var ks []map[string]any
			for _, k := range p.keys {
				ks = append(ks, map[string]any{
					"kty": "RSA", "kid": k.kid, "use": "sig", "alg": "RS256",
					"n": base64.RawURLEncoding.EncodeToString(k.pub.N.Bytes()),
					"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.pub.E)).Bytes()),
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": ks})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *idp) set(f func(p *idp)) { p.mu.Lock(); f(p); p.mu.Unlock() }
func (p *idp) url() string        { return p.srv.URL + WellKnown }

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// mint signs a token naming issuer, under kid.
func mint(t *testing.T, key *rsa.PrivateKey, kid, issuer string) string {
	t.Helper()
	c := tokenClaims("hanzo-console", "acme", "", false, time.Now().Add(time.Hour))
	c.Issuer = issuer
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c.Claims)
	tok.Header["kid"] = kid
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDiscoveryIssuerIsTheURLWithoutTheWellKnownPath(t *testing.T) {
	for url, want := range map[string]string{
		"https://hanzo.ai/.well-known/openid-configuration":          "https://hanzo.ai",
		"https://id.example/tenant/.well-known/openid-configuration": "https://id.example/tenant",
	} {
		if got, err := issuerOf(url); err != nil || got != want {
			t.Errorf("issuerOf(%q) = %q, %v; want %q", url, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "https://hanzo.ai", "https://hanzo.ai/openid-configuration", "http://hanzo.ai/.well-known/openid-configuration",
		"https://hanzo.ai/.well-known/openid-configuration?x=1", "https://u@hanzo.ai/.well-known/openid-configuration",
	} {
		if got, err := issuerOf(bad); err == nil {
			t.Errorf("issuerOf(%q) = %q, want an error", bad, got)
		}
	}
}

// The issuer and keys come from the document, which names an issuer no default has.
func TestBoundaryTrustsWhatDiscoveryNames(t *testing.T) {
	p := newIdP(t)
	key := newKey(t)
	p.set(func(p *idp) { p.keys = []jwkKey{{"k1", &key.PublicKey}} })
	b, err := newBoundary([]string{p.url()}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.validate(mint(t, key, "k1", p.srv.URL)); err != nil {
		t.Fatalf("a token from the discovered issuer: %v", err)
	}
	// The same key under another issuer is refused: the issuer is the provider's, not a label.
	if _, err := b.validate(mint(t, key, "k1", "https://hanzo.id")); err == nil {
		t.Error("a token from another issuer was accepted")
	}
	if _, err := b.validate(mint(t, key, "k1", "")); err == nil {
		t.Error("a token naming no issuer was accepted")
	}
}

// A document that names an issuer other than the one its URL implies is refused whole.
func TestDiscoveryRefusesAForeignIssuer(t *testing.T) {
	p := newIdP(t)
	key := newKey(t)
	p.set(func(p *idp) {
		p.keys = []jwkKey{{"k1", &key.PublicKey}}
		p.issuer = "https://hanzo.ai" // another host's identity, said from this one
	})
	b, err := newBoundary([]string{p.url()}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, iss := range []string{p.srv.URL, "https://hanzo.ai"} {
		if _, err := b.validate(mint(t, key, "k1", iss)); err == nil {
			t.Errorf("a token under %q passed against a document that lied about its issuer", iss)
		}
	}
	// And a jwks_uri that is not https (and not loopback) is refused.
	p.set(func(p *idp) { p.issuer, p.jwks = "", "http://evil.example/jwks" })
	d, _ := newDiscovery(p.url(), nil, time.Hour)
	if _, err := d.Document(); err == nil {
		t.Error("a plain-http jwks_uri was accepted")
	}
}

// A provider that has never answered fails closed, and is not asked on every request.
func TestDiscoveryNeverGoodFailsClosed(t *testing.T) {
	p := newIdP(t)
	key := newKey(t)
	p.set(func(p *idp) { p.keys = []jwkKey{{"k1", &key.PublicKey}}; p.down = true })
	b, err := newBoundary([]string{p.url()}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	tok := mint(t, key, "k1", p.srv.URL)
	for range 20 {
		if _, err := b.validate(tok); err == nil {
			t.Fatal("a token was accepted with no discovery document")
		}
	}
	// It was asked once in that burst, not once per request.
	d, _ := DiscoveryFor(p.url(), nil)
	if _, err := d.Document(); err == nil {
		t.Fatal("document from a down provider")
	}
	// Back up, and after the retry window it recovers by itself.
	p.set(func(p *idp) { p.down = false })
	d.mu.Lock()
	d.attemptAt = time.Now().Add(-time.Minute)
	d.mu.Unlock()
	b2, _ := newBoundary([]string{p.url()}, nil, 0)
	if _, err := b2.validate(tok); err != nil {
		t.Fatalf("after the provider came back: %v", err)
	}
}

// A refresh that fails keeps serving the last good document; one that succeeds replaces it.
func TestDiscoveryKeepsTheLastGoodDocument(t *testing.T) {
	p := newIdP(t)
	key := newKey(t)
	p.set(func(p *idp) { p.keys = []jwkKey{{"k1", &key.PublicKey}} })
	now := time.Now()
	d, err := newDiscovery(p.url(), nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d.now = func() time.Time { return now }
	first, err := d.Document()
	if err != nil {
		t.Fatal(err)
	}
	// Stale, and the provider is down: the last good copy is still the answer.
	p.set(func(p *idp) { p.down = true })
	now = now.Add(2 * time.Hour)
	for range 3 {
		got, err := d.Document()
		if err != nil || got != first {
			t.Fatalf("stale on failure: %+v, %v; want %+v", got, err, first)
		}
	}
	eventually(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.refreshing })
	// The provider moves its keys and comes back: the next stale read refreshes behind it.
	p.set(func(p *idp) { p.down = false; p.jwksPath = "/keys2"; p.jwks = p.srv.URL + "/keys2" })
	now = now.Add(time.Minute)
	if got, _ := d.Document(); got != first {
		t.Fatalf("a refresh must not block the request that triggers it: %+v", got)
	}
	eventually(t, func() bool { got, _ := d.Document(); return got.JWKSURI == p.srv.URL+"/keys2" })
}

func eventually(t *testing.T, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

// Keys rotate under a stable discovery document, and a new jwks_uri is followed.
func TestBoundaryFollowsKeyRotationAndANewJWKSURI(t *testing.T) {
	p := newIdP(t)
	old, rotated := newKey(t), newKey(t)
	p.set(func(p *idp) { p.keys = []jwkKey{{"k1", &old.PublicKey}} })
	b, err := newBoundary([]string{p.url()}, nil, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.validate(mint(t, old, "k1", p.srv.URL)); err != nil {
		t.Fatal(err)
	}
	// The provider publishes a new key beside the old one; a token under it verifies.
	p.set(func(p *idp) { p.keys = []jwkKey{{"k1", &old.PublicKey}, {"k2", &rotated.PublicKey}} })
	time.Sleep(5 * time.Millisecond)
	if _, err := b.validate(mint(t, rotated, "k2", p.srv.URL)); err != nil {
		t.Fatalf("a token under a rotated-in key: %v", err)
	}
	// An unknown key is still refused.
	if _, err := b.validate(mint(t, newKey(t), "k3", p.srv.URL)); err == nil {
		t.Error("a token under a key the provider never published was accepted")
	}
	// The document moves its key set: the verifier follows it.
	p.set(func(p *idp) { p.jwksPath = "/keys2"; p.jwks = p.srv.URL + "/keys2" })
	d, _ := DiscoveryFor(p.url(), nil)
	d.mu.Lock()
	d.fetchedAt = time.Now().Add(-2 * time.Hour)
	d.attemptAt = time.Time{}
	d.mu.Unlock()
	_, _ = d.Document()
	eventually(t, func() bool { got, _ := d.Document(); return got.JWKSURI == p.srv.URL+"/keys2" })
	if _, err := b.validate(mint(t, rotated, "k2", p.srv.URL)); err != nil {
		t.Fatalf("after the key set moved: %v", err)
	}
}

// The document is fetched once for a burst of tokens, never per request.
func TestNoFetchPerRequest(t *testing.T) {
	p := newIdP(t)
	key := newKey(t)
	p.set(func(p *idp) { p.keys = []jwkKey{{"k1", &key.PublicKey}} })
	b, _ := newBoundary([]string{p.url()}, nil, 0)
	for range 50 {
		if _, err := b.validate(mint(t, key, "k1", p.srv.URL)); err != nil {
			t.Fatal(err)
		}
	}
	if n := p.docHits.Load(); n != 1 {
		t.Errorf("the discovery document was fetched %d times for 50 tokens, want 1", n)
	}
}

// Concurrent cold-start callers share the in-flight discovery fetch instead of
// losing the race to ErrNoDiscovery while the first request is fetching.
func TestDiscoveryColdStartCoalescesConcurrentCallers(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	var hits atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-gate
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   srv.URL,
			"jwks_uri": srv.URL + "/jwks",
		})
	}))
	t.Cleanup(srv.Close)

	d, err := newDiscovery(srv.URL+WellKnown, srv.Client(), discoveryTTL)
	if err != nil {
		t.Fatal(err)
	}
	const callers = 4
	errs := make([]error, callers)
	docs := make([]Document, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			docs[idx], errs[idx] = d.Document()
		}(i)
	}
	<-started
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()

	for i := range callers {
		if errs[i] != nil || docs[i].Issuer != srv.URL {
			t.Fatalf("caller %d = %+v, %v; want valid document", i, docs[i], errs[i])
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("discovery fetched %d times for %d concurrent cold-start callers, want 1", got, callers)
	}
}
