package host

// ONE CEILING FOR THE DEPLOYMENT.
//
// ScopeRateLimit kept its minutes in a map in each process, so an org held to N a
// minute was held to N by every process that served it — N times the replicas,
// times the apps. These drive two limiters as two processes do: separate limiter
// instances, separate Coords with separate local counts, and nothing in common but
// the deployment's KV. And they take the KV away, which must loosen the ceiling to
// one per process and never refuse a request for it.

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/internal/driver/kv/kvtest"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// process is one process's ScopeRateLimit and the Coord it counts through.
type process struct {
	app *zip.App
	co  *coord.Coord
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
	app.Use(ScopeRateLimit(nil, gp))
	app.Raw(http.MethodGet, "/v1/train/jobs", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) })
	return process{app: app, co: co}
}

// orgCeiling is a policy store holding every org to rpm a minute.
func orgCeiling(t *testing.T, rpm int) *edge.Store {
	t.Helper()
	return staticPol(t, edge.Policy{OrgRPM: rpm})
}

// TestTwoProcessesShareOneCeiling: an org held to 3 a minute, asked through two
// processes in turn, is admitted 3 times in all and refused the 4th on whichever
// process takes it; a key held to 2 a minute likewise. Another org is untouched.
//
// It runs on the in-process KV everywhere, and again on a real valkey-server where
// one is installed, so the gate never depends on the machine it runs on.
//
// MUTATION: count on unnamed Windows (or put back the per-process map) and each
// process admits its own 3.
func TestTwoProcessesShareOneCeiling(t *testing.T) {
	t.Run("in-process KV", func(t *testing.T) { shareOneCeiling(t, coordtest.Memory(t)) })
	t.Run("valkey", func(t *testing.T) {
		url := kvtest.KV(t)
		shareOneCeiling(t, func(id string) *coord.Coord { return kvtest.Pod(t, url, id) })
	})
}

func shareOneCeiling(t *testing.T, pod func(id string) *coord.Coord) {
	gp := orgCeiling(t, 3)
	a := limiterProcess(t, pod("cloud-0"), gp)
	b := limiterProcess(t, pod("cloud-1"), gp)
	ps := []process{a, b}

	for i := range 3 {
		if code := ps[i%2].send(t, "acme", "", "").StatusCode; code != http.StatusOK {
			t.Fatalf("request %d of the org's 3, on process %d: %d", i+1, i%2, code)
		}
	}
	for i, p := range ps {
		resp := p.send(t, "acme", "", "")
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("process %d admitted past the org's shared 3: %d", i, resp.StatusCode)
		}
		if got := resp.Header.Get("X-RateLimit-Limit"); got != "3" {
			t.Fatalf("process %d refusal names limit %q, want 3", i, got)
		}
	}
	if code := b.send(t, "globex", "", "").StatusCode; code != http.StatusOK {
		t.Fatalf("another org shares acme's count: %d", code)
	}

	// A key's own minute is one count too, inside an org with room for it.
	gp2 := orgCeiling(t, 0)
	a, b = limiterProcess(t, a.co, gp2), limiterProcess(t, b.co, gp2)
	if code := a.send(t, "initech", "k1", "2").StatusCode; code != http.StatusOK {
		t.Fatalf("key request 1: %d", code)
	}
	if code := b.send(t, "initech", "k1", "2").StatusCode; code != http.StatusOK {
		t.Fatalf("key request 2: %d", code)
	}
	resp := a.send(t, "initech", "k1", "2")
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("X-RateLimit-Limit") != "2" {
		t.Fatalf("the key's 3rd request across two processes: %d limit %q, want 429 naming 2",
			resp.StatusCode, resp.Header.Get("X-RateLimit-Limit"))
	}
	if code := b.send(t, "initech", "k2", "2").StatusCode; code != http.StatusOK {
		t.Fatalf("a sibling key shares k1's count: %d", code)
	}
}

// TestAnUnreachableCountFailsOpenToEachProcess: with the deployment's KV gone, a
// request is never refused for it. Each process counts alone — admitted up to the
// ceiling, refused past it — so the ceiling holds per process, which is the most a
// limiter can promise without the count it lost.
//
// MUTATION: refuse when coord.Spend cannot ask the KV and the first request is a
// 429; admit everything instead and the 4th on one process is a 200.
func TestAnUnreachableCountFailsOpenToEachProcess(t *testing.T) {
	gone := deadKV(t)
	gp := orgCeiling(t, 3)
	a := limiterProcess(t, gone("cloud-0"), gp)
	b := limiterProcess(t, gone("cloud-1"), gp)
	if !a.co.Shared() {
		t.Fatal("the dead KV is not configured as the deployment's count; this test proves nothing")
	}
	for i, p := range []process{a, b} {
		for n := range 3 {
			if code := p.send(t, "acme", "", "").StatusCode; code != http.StatusOK {
				t.Fatalf("process %d request %d with the KV down: %d, want 200 — a limiter took the API down", i, n+1, code)
			}
		}
		if code := p.send(t, "acme", "", "").StatusCode; code != http.StatusTooManyRequests {
			t.Fatalf("process %d request 4 with the KV down: %d, want 429 from its own count", i, code)
		}
	}
}

// deadKV answers a Coord per pod whose KV_URL names a port nothing listens on.
func deadKV(t *testing.T) func(id string) *coord.Coord {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "kv://" + l.Addr().String()
	_ = l.Close()
	return func(id string) *coord.Coord {
		co, err := coord.New(coord.Config{
			URL:        url,
			Deployment: "test",
			Self:       coord.Member{ID: id, Addr: id + ":8443"},
			Instance:   id,
			Secret:     bytes.Repeat([]byte{9}, 32),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { co.Close(context.Background()) })
		return co
	}
}

// A REFUSED FLOOD IS ANSWERED HERE. Once a ceiling refuses in a process, that
// process answers the rest of the window from what it saw, without asking the
// deployment's count again: here the count is taken away after the refusal, and the
// next request is still refused — asked, a dead KV would have fallen back to this
// process's empty count and admitted it, as a fresh process on it does.
//
// MUTATION: drop the rl.over lookup in charge and the request after the KV dies is
// a 200.
func TestARefusalIsAnsweredWithoutAskingAgain(t *testing.T) {
	gp := orgCeiling(t, 3)
	p := limiterProcess(t, coordtest.Memory(t)("cloud-0"), gp)
	for i := range 3 {
		if code := p.send(t, "acme", "", "").StatusCode; code != http.StatusOK {
			t.Fatalf("request %d of 3: %d", i+1, code)
		}
	}
	if code := p.send(t, "acme", "", "").StatusCode; code != http.StatusTooManyRequests {
		t.Fatalf("request 4 of 3: %d, want 429", code)
	}
	gone := deadKV(t)
	p.co = gone("cloud-0")
	resp := p.send(t, "acme", "", "")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the refused window asked the count again: %d, want the 429 it already knew", resp.StatusCode)
	}
	if wait, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || wait < 1 || wait > 60 {
		t.Fatalf("a remembered refusal says when to come back: Retry-After %q", resp.Header.Get("Retry-After"))
	}
	if code := limiterProcess(t, gone("cloud-1"), gp).send(t, "acme", "", "").StatusCode; code != http.StatusOK {
		t.Fatalf("control: a process that never saw the refusal, on the dead KV, answered %d, want 200", code)
	}
}

// No scope can spell another's window: the class leads and every part is escaped,
// so an org named like a class, or a part holding the separator, is its own window.
func TestEveryCeilingIsItsOwnWindow(t *testing.T) {
	ws := []ceiling{
		{class: "floor", parts: []string{"acme", "audio"}},
		{class: "rule", parts: []string{"floor", "acme", "audio"}},
		{class: "rule", parts: []string{"acme", "", "audio"}},
		{class: "rule", parts: []string{"a/b", "c", ""}},
		{class: "rule", parts: []string{"a", "b/c", ""}},
		{class: "org", parts: []string{"acme"}},
		{class: "org", parts: []string{"acme/"}},
		{class: "key", parts: []string{"acme", "k1"}},
	}
	seen := map[string]int{}
	for i, w := range ws {
		k := w.key()
		if j, dup := seen[k]; dup {
			t.Fatalf("ceilings %d and %d share the window %q", j, i, k)
		}
		seen[k] = i
	}
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
