package sites

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// testPurger builds a Purger the way these tests need it: a ZERO coalescing window,
// so every call is admitted and the assertions are about the request rather than the
// debounce, but with the two fields only NewPurger otherwise supplies. A bare struct
// literal left pending nil, so admit panicked writing to it, and left ceiling zero,
// so takeToken (inMinute >= ceiling) refused every call and nothing was ever sent.
// The tests do not use NewPurger itself because it reads the real environment and
// arms a 10s window.
func testPurger(token, zone, api string, c *http.Client) *Purger {
	return &Purger{
		token: token, zoneID: zone, api: api, client: c,
		log:     luxlog.New("test"),
		pending: map[string]*purgeState{},
		ceiling: 120, // NewPurger's default; 0 would rate-limit every call away
	}
}

func TestPurgerUnconfiguredIsNoop(t *testing.T) {
	p := testPurger("", "", "http://127.0.0.1:0", &http.Client{Timeout: time.Second})
	if p.Configured() {
		t.Fatal("expected unconfigured")
	}
	// Must not touch the network and must not error — a missing CF token never
	// fails a deploy.
	if err := p.PurgeTags(context.Background(), "site-acme-blog"); err != nil {
		t.Fatalf("unconfigured PurgeTags returned error: %v", err)
	}
}

func TestPurgerPostsTagsWithAuth(t *testing.T) {
	var gotAuth, gotPath, gotCT string
	var gotTags []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		var body struct {
			Tags []string `json:"tags"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotTags = body.Tags
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	p := testPurger("tok-secret", "zone123", srv.URL, srv.Client())
	if !p.Configured() {
		t.Fatal("expected configured")
	}
	if err := p.PurgeTags(context.Background(), "site-acme-blog", "site-acme-docs"); err != nil {
		t.Fatalf("PurgeTags: %v", err)
	}
	if gotAuth != "Bearer tok-secret" {
		t.Errorf("auth = %q, want Bearer tok-secret", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotPath != "/zones/zone123/purge_cache" {
		t.Errorf("path = %q, want /zones/zone123/purge_cache", gotPath)
	}
	if len(gotTags) != 2 || gotTags[0] != "site-acme-blog" || gotTags[1] != "site-acme-docs" {
		t.Errorf("tags = %v", gotTags)
	}
}

func TestPurgerPropagatesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()
	p := testPurger("t", "z", srv.URL, srv.Client())
	if err := p.PurgeTags(context.Background(), "site-x-y"); err == nil {
		t.Fatal("expected error on non-2xx")
	}
}

func TestPurgerEmptyTagsIsNoop(t *testing.T) {
	p := testPurger("t", "z", "http://127.0.0.1:0", &http.Client{Timeout: time.Second})
	if err := p.PurgeTags(context.Background()); err != nil {
		t.Fatalf("empty PurgeTags: %v", err)
	}
}
