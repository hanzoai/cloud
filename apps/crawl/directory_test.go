package crawl

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/botauth"
	"github.com/zap-proto/zip"
)

// keyStore is the deployment's key set held in memory, as KMS holds it.
type keyStore struct {
	mu   sync.Mutex
	set  []byte
	fail error
}

func (k *keyStore) Load(context.Context) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail != nil {
		return nil, k.fail
	}
	if k.set == nil {
		return nil, botauth.ErrNoKeys
	}
	return bytes.Clone(k.set), nil
}

func (k *keyStore) Save(_ context.Context, set []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.set = bytes.Clone(set)
	return nil
}

// composed mounts crawl the way the composition root does (cloud.UseAll): on an
// app of its own, so only what crawl registers as a ROUTE answers there. A
// middleware answering the directory passed a direct mount and 404'd in
// production; this is the mount that tells the two apart. The prefixes are the
// manifest row's (manifest/botauth_test.go pins the row).
func composed(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	crawl := cloud.Plugin{Name: "crawl", Price: cloud.Metered, Prefixes: []string{botauth.Path, Path}, Use: Use}
	if err := cloud.UseAll(app, []cloud.Plugin{crawl}, &cloud.Config{}, cloud.Deps{}); err != nil {
		t.Fatalf("UseAll: %v", err)
	}
	return app
}

// directory asks app for the key directory on host and answers the response and
// its body.
func directory(t *testing.T, app *zip.App, host string) (*http.Response, []byte) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodGet, botauth.Path, nil)
	rq.Host = host
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET %s: %v", botauth.Path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// TestTheDirectoryPublishesTheKeyEveryFetchIsSignedWith is #423 at crawl's door:
// signed out, crawl answers the directory, minting the deployment's key on the
// first ask; the directory proves it holds that key for the host it was asked on;
// and a request signed the way crawl's client signs verifies against exactly
// what was published.
func TestTheDirectoryPublishesTheKeyEveryFetchIsSignedWith(t *testing.T) {
	store := &keyStore{}
	s := &botauth.Signer{Agent: "https://api.hanzo.test" + botauth.Path, Store: store}
	botauth.Use(s)
	t.Cleanup(func() { botauth.Use(nil) })
	app := composed(t)

	resp, doc := directory(t, app, "api.hanzo.test")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("directory: %d %s", resp.StatusCode, doc)
	}
	for h, want := range map[string]string{
		"Content-Type":                botauth.Media,
		"Cache-Control":               "public, max-age=3600",
		"Access-Control-Allow-Origin": "*",
	} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if store.set == nil {
		t.Fatal("the first directory request stored no key")
	}
	if err := botauth.VerifyDirectory(resp.Header, "api.hanzo.test", doc, time.Now()); err != nil {
		t.Fatalf("the directory does not prove the key it publishes: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "https://example.com/article", nil)
	r.Host = ""
	if err := s.Sign(r); err != nil {
		t.Fatal(err)
	}
	r.Host = r.URL.Host
	if err := botauth.Verify(r, doc, time.Now()); err != nil {
		t.Fatalf("a signed fetch does not verify against the published directory: %v", err)
	}
	if got := r.Header.Get("Signature-Agent"); got != `"https://api.hanzo.test/.well-known/http-message-signatures-directory"` {
		t.Fatalf("Signature-Agent = %s", got)
	}

	// Another deployment's key: the published directory refuses it.
	other := &botauth.Signer{Agent: s.Agent, Store: &keyStore{}}
	if _, err := other.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	o := httptest.NewRequest(http.MethodGet, "https://example.com/article", nil)
	o.Host = ""
	if err := other.Sign(o); err != nil {
		t.Fatal(err)
	}
	o.Host = o.URL.Host
	if err := botauth.Verify(o, doc, time.Now()); !errors.Is(err, botauth.ErrRefused) {
		t.Fatalf("a key the directory does not publish verified: %v", err)
	}
}

// TestTheDirectoryWithNoKeyIsAnAnswerNotAnEmptySet: a process with no signer has
// nothing to publish (404), and a store that cannot be read is 503; neither is a
// 200 with an empty key set, which a verifier would cache as "this bot has no
// keys".
func TestTheDirectoryWithNoKeyIsAnAnswerNotAnEmptySet(t *testing.T) {
	botauth.Use(nil)
	app := composed(t)
	// crawl's own 404, not the router's: the route answered and said why.
	if resp, body := directory(t, app, "api.hanzo.test"); resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "publishes no keys") {
		t.Fatalf("with no signer: %d %s", resp.StatusCode, body)
	}
	botauth.Use(&botauth.Signer{Agent: "https://api.hanzo.test" + botauth.Path, Store: &keyStore{fail: errors.New("kms: unreachable")}})
	t.Cleanup(func() { botauth.Use(nil) })
	resp, body := directory(t, app, "api.hanzo.test")
	if resp.StatusCode != http.StatusServiceUnavailable || strings.Contains(string(body), "keys\"") {
		t.Fatalf("with an unreadable store: %d %s", resp.StatusCode, body)
	}
}

// TestEveryPageFetchIsSigned pins the one client every page read leaves through
// (Fetch, so /v1/crawl, websearch's page reads, the answer engine's research reads
// and ai's fetch tool) to the shared signer.
func TestEveryPageFetchIsSigned(t *testing.T) {
	if !botauth.Signs(client.Transport) {
		t.Fatal("crawl's client does not sign: its requests leave without Web Bot Auth")
	}
}
