package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// platforms stands in for X, LinkedIn and Facebook, keeping each post it took.
type platforms struct {
	mu     sync.Mutex
	posted map[string][]string
	// fail answers the next n posts to a platform with a status.
	fail map[string][]int
}

func (p *platforms) next(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if q := p.fail[name]; len(q) > 0 {
		p.fail[name] = q[1:]
		return q[0]
	}
	return 0
}

func (p *platforms) took(name, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posted[name] = append(p.posted[name], text)
}

func (p *platforms) count(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.posted[name])
}

// standIn serves the three platforms, points the posters at them, and connects
// the named accounts for acme.
func standIn(t *testing.T, connected ...string) *platforms {
	t.Helper()
	p := &platforms{posted: map[string][]string{}, fail: map[string][]int{}}
	refuse := func(w http.ResponseWriter, st int) {
		w.WriteHeader(st)
		_, _ = w.Write([]byte(`{"detail":"refused by the stand-in"}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/x/2/tweets":
			if st := p.next("x"); st != 0 {
				refuse(w, st)
				return
			}
			var in struct{ Text string }
			_ = json.Unmarshal(body, &in)
			p.took("x", in.Text)
			_, _ = w.Write([]byte(`{"data":{"id":"190001"}}`))
		case r.URL.Path == "/li/v2/userinfo":
			_, _ = w.Write([]byte(`{"sub":"m-7"}`))
		case r.URL.Path == "/li/v2/ugcPosts":
			if r.Header.Get("X-Restli-Protocol-Version") != "2.0.0" || !strings.Contains(string(body), "urn:li:person:m-7") {
				refuse(w, http.StatusBadRequest)
				return
			}
			p.took("linkedin", string(body))
			w.Header().Set("X-Restli-Id", "urn:li:share:42")
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/fb/me/accounts":
			_, _ = w.Write([]byte(`{"data":[{"id":"pg1","name":"Acme","access_token":"page-token"}]}`))
		case r.URL.Path == "/fb/pg1/feed":
			if r.Header.Get("Authorization") != "Bearer page-token" {
				refuse(w, http.StatusForbidden)
				return
			}
			p.took("facebook", r.FormValue("message"))
			_, _ = w.Write([]byte(`{"id":"pg1_9"}`))
		default:
			refuse(w, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	ox, ol, og, ow := xAPI, linkedinAPI, graphAPI, postWaits
	xAPI, linkedinAPI, graphAPI, postWaits = srv.URL+"/x", srv.URL+"/li", srv.URL+"/fb", []time.Duration{0, 0}
	t.Cleanup(func() { xAPI, linkedinAPI, graphAPI, postWaits = ox, ol, og, ow })
	// Each named account is connected for acme the way a connect leaves it: a
	// row, and its token sealed in the org's KMS namespace.
	newApp(t, newKMS(t))
	for _, c := range connected {
		conn := Connection{Org: "acme", Provider: c, ExternalID: "acct-" + c, AccountLabel: c}
		token := "tok-" + c
		if c == "facebook" {
			// The sealed credential is the Page token, and the post goes to that Page.
			conn.ExternalID, conn.Label, conn.BotUserID = "pg1", "pg1", "page"
			token = "page-token"
		}
		if err := kmsPut(mounted, custodyPath(registry[c], "acme", conn.Label), accessSecret, []byte(token)); err != nil {
			t.Fatal(err)
		}
		if err := mounted.State.store.Upsert(context.Background(), conn); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// post makes one post as org over the plane op, as apps/auto asks for it.
func postAs(t *testing.T, org, to, text string) *client.SocialPostOut {
	t.Helper()
	out, err := planePost(cloud.For(context.Background(), org), &client.SocialPostIn{To: to, Text: text})
	if err != nil {
		t.Fatalf("post %s: %v", to, err)
	}
	return out
}

// A post is made with the token the org connected, read from its own KMS
// namespace here: each platform answers where it went, a busy one is asked
// again, a refusal is said in the platform's words, and another org's account
// is not reached.
func TestAPostIsMadeWithTheOrgsOwnToken(t *testing.T) {
	p := standIn(t, "x", "linkedin", "facebook")
	for to, want := range map[string]string{
		"x":        "https://x.com/i/status/190001",
		"linkedin": "https://www.linkedin.com/feed/update/urn:li:share:42",
		"facebook": "https://www.facebook.com/pg1_9",
	} {
		if got := postAs(t, "acme", to, "Hello from the week."); got.URL != want || got.Error != "" {
			t.Errorf("%s = %+v", to, got)
		}
	}
	p.fail["x"] = []int{429, 503}
	if got := postAs(t, "acme", "x", "Busy, then not."); got.URL == "" {
		t.Fatalf("a busy X = %+v", got)
	}
	p.fail["x"] = []int{403}
	if got := postAs(t, "acme", "x", "No."); got.URL != "" || !strings.Contains(got.Error, "X refused the post (403): refused by the stand-in") {
		t.Fatalf("a refusing X = %+v", got)
	}
	if got := postAs(t, "globex", "x", "Theirs."); !strings.Contains(got.Error, "X is not connected") || p.count("x") != 2 {
		t.Fatalf("another org's X = %+v, posts %d", got, p.count("x"))
	}
	if _, err := planePost(cloud.For(context.Background(), "acme"), &client.SocialPostIn{To: "myspace", Text: "x"}); err == nil {
		t.Fatal("a network nobody posts to was taken")
	}
	// A network the org has not connected is an answer, not a fault.
	if got := postAs(t, "acme", "instagram", "x"); !strings.Contains(got.Error, "Instagram is not connected") {
		t.Fatalf("an unconnected network = %+v", got)
	}
}

// A timeout after a post may have gone is not posted again.
func TestATimeoutIsNotPostedTwice(t *testing.T) {
	standIn(t, "x")
	calls := 0
	orig := posters["x"]
	posters["x"] = poster{name: "X", provider: xProvider, publish: func(context.Context, postTo) (string, error) {
		calls++
		return "", context.DeadlineExceeded
	}}
	t.Cleanup(func() { posters["x"] = orig })
	if got := postAs(t, "acme", "x", "once"); got.URL != "" || calls != 1 {
		t.Fatalf("after a timeout = %+v, asked %d times", got, calls)
	}
}
