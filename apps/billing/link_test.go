package billing

// link_test.go — what /v1/billing/link decides from the caller's own credential: whether
// they administer the org they act in, and which accounts they hold. commerce, which
// decides the rest against the books, is a stand-in here that records what it was told;
// its own rules are tested in apps/commerce.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	iamsdk "github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
)

// commerceSeen is what the stand-in commerce was asked, and what it answers.
type commerceSeen struct {
	mu    sync.Mutex
	read  []client.PlanLinkIn
	put   []client.PlanLinkPutIn
	drop  []client.PlanLinkDropIn
	fault error
}

func (s *commerceSeen) last() (in client.PlanLinkIn, put client.PlanLinkPutIn, drop client.PlanLinkDropIn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.read); n > 0 {
		in = s.read[n-1]
	}
	if n := len(s.put); n > 0 {
		put = s.put[n-1]
	}
	if n := len(s.drop); n > 0 {
		drop = s.drop[n-1]
	}
	return
}

// planePeer serves one app's ops on its socket and waits until it listens.
func planePeer(t *testing.T, name string, peer *zip.App) {
	t.Helper()
	at := zip.SocketPath(name)
	go func() { _ = peer.Listen(at) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if c, err := net.Dial("unix", at); err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never accepted", at)
		}
	}
}

// linkWorld stands up IAM (who each user is), the answer to "does this person own this
// org", and commerce's three link ops.
func linkWorld(t *testing.T, rows map[string]iamsdk.Self, owns map[string]bool) *commerceSeen {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	idp := zip.New(zip.Config{AppName: "iam", DisableStartupMessage: true})
	idp.Post("/iam/self", func(ctx context.Context, _ *struct{}) (*iamsdk.Self, error) {
		p, ok := rows[zip.CallerOf(ctx).User]
		if !ok {
			return nil, zip.ErrUnauthorized("no such subject")
		}
		return &p, nil
	}, zip.WithOperationID(iamsdk.IAMSelf))
	planePeer(t, "iam", idp)

	t.Cleanup(func() { cloud.SetPeers(cloud.Peers{}) })
	cloud.SetPeers(cloud.Peers{OwnerOf: func(_ context.Context, person, org string) (bool, error) {
		return owns[person+"|"+org], nil
	}})

	seen := &commerceSeen{}
	state := &client.PlanLinkState{Plans: []client.LendablePlan{}}
	peer := zip.New(zip.Config{AppName: "commerce", DisableStartupMessage: true})
	peer.Post("/billing/link", func(_ context.Context, in *client.PlanLinkIn) (*client.PlanLinkState, error) {
		seen.mu.Lock()
		defer seen.mu.Unlock()
		seen.read = append(seen.read, *in)
		return state, nil
	}, zip.WithOperationID(client.BillingLink))
	peer.Post("/billing/link/put", func(_ context.Context, in *client.PlanLinkPutIn) (*client.PlanLinkState, error) {
		seen.mu.Lock()
		defer seen.mu.Unlock()
		seen.put = append(seen.put, *in)
		return state, seen.fault
	}, zip.WithOperationID(client.BillingLinkPut))
	peer.Post("/billing/link/drop", func(_ context.Context, in *client.PlanLinkDropIn) (*client.PlanLinkState, error) {
		seen.mu.Lock()
		defer seen.mu.Unlock()
		seen.drop = append(seen.drop, *in)
		return state, seen.fault
	}, zip.WithOperationID(client.BillingLinkDrop))
	planePeer(t, "commerce", peer)
	return seen
}

// asks sends one request and answers the status.
func asks(t *testing.T, app *zip.App, method, path, body string, hdr ...string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// who is the credential headers of a signed-in person acting in org, a member of orgs.
func who(id, org string, admin bool, orgs ...string) []string {
	h := []string{"X-User-Id", id, "X-User-Owner", org, "X-Org-Id", org, "X-User-Orgs", strings.Join(orgs, ",")}
	if admin {
		h = append(h, "X-User-IsOrgAdmin", "true")
	}
	return h
}

func TestTheLinkRoutesDecideWhoTheCallerIsFromTheirCredential(t *testing.T) {
	seen := linkWorld(t, map[string]iamsdk.Self{
		"uuid-ada": {ID: "uuid-ada", Row: "hanzo/lkada"},   // a person of the shared signup org
		"uuid-bob": {ID: "uuid-bob", Row: "lkacme/lkbob"}, // an owner of one org, a member of another
	}, map[string]bool{"lkacme/lkbob|lkacme": true})
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	app := selecting(t, cloud.Deps{Audit: trail})

	// A signup-org person holds their own wallet, and an org they are only a member of
	// is not theirs to lend.
	if got := asks(t, app, http.MethodGet, "/v1/billing/link", "", who("uuid-ada", "carbon", false, "hanzo", "carbon", "lkacme")...); got != 200 {
		t.Fatalf("GET: %d", got)
	}
	read, _, _ := seen.last()
	if len(read.Held) != 1 || read.Held[0] != "hanzo/lkada" || read.Privileged {
		t.Fatalf("a signup person and plain member was told %+v; want only their own wallet, unprivileged", read)
	}

	// An owner holds the orgs IAM says they own and no other in their token, and their
	// own wallet is not lendable outside the signup org.
	asks(t, app, http.MethodGet, "/v1/billing/link", "", who("uuid-bob", "lkacme", true, "lkacme", "lkother", "hanzo", "admin")...)
	read, _, _ = seen.last()
	if len(read.Held) != 1 || read.Held[0] != "lkacme" || !read.Privileged {
		t.Fatalf("an owner of lkacme was told %+v; want lkacme alone, privileged", read)
	}

	// PUT states who linked it and what they hold, and never takes either from the body.
	body := `{"from":"lkacme","held":["lkother"],"by":"hanzo/someone","privileged":true}`
	if got := asks(t, app, http.MethodPut, "/v1/billing/link", body, who("uuid-bob", "lkcarbon", false, "lkacme", "lkcarbon")...); got != 200 {
		t.Fatalf("PUT: %d", got)
	}
	_, put, _ := seen.last()
	if put.From != "lkacme" || put.By != "lkacme/lkbob" || put.Privileged || len(put.Held) != 1 || put.Held[0] != "lkacme" {
		t.Fatalf("commerce was told %+v; want the credential's facts and the body's only `from`", put)
	}

	// DELETE carries ?from= and the same facts.
	if got := asks(t, app, http.MethodDelete, "/v1/billing/link?from=lkacme", "", who("uuid-bob", "lkcarbon", true, "lkacme", "lkcarbon")...); got != 200 {
		t.Fatalf("DELETE: %d", got)
	}
	_, _, drop := seen.last()
	if drop.From != "lkacme" || !drop.Privileged || len(drop.Held) != 1 {
		t.Fatalf("commerce was told %+v", drop)
	}

	// A refusal commerce chose reaches the caller whole, and every act is on the trail,
	// the refused ones too.
	seen.mu.Lock()
	seen.fault = &zip.HTTPError{Status: http.StatusConflict, Code: client.LinkPlanTaken, Msg: "the plan is already lent"}
	seen.mu.Unlock()
	req := httptest.NewRequest(http.MethodPut, "/v1/billing/link", strings.NewReader(`{"from":"lkacme"}`))
	req.Header.Set("Content-Type", "application/json")
	for i, h := 0, who("uuid-bob", "lkcarbon", true, "lkacme", "lkcarbon"); i+1 < len(h); i += 2 {
		req.Header.Set(h[i], h[i+1])
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	var problem struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&problem)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || problem.Code != client.LinkPlanTaken {
		t.Fatalf("refusal = %d %+v, want 409 %s", resp.StatusCode, problem, client.LinkPlanTaken)
	}
	links, _, _ := trail.Query(context.Background(), audit.Filter{Action: "billing.plan.link"})
	unlinks, _, _ := trail.Query(context.Background(), audit.Filter{Action: "billing.plan.unlink"})
	if len(links) != 2 || len(unlinks) != 1 {
		t.Fatalf("trail holds %d link and %d unlink rows; want 2 and 1", len(links), len(unlinks))
	}
	for _, r := range links {
		if r.Actor.Org != "lkcarbon" || !strings.Contains(string(r.After), `"from":"lkacme"`) || !strings.Contains(string(r.After), `"by":"lkacme/lkbob"`) {
			t.Errorf("link row = %+v %s; want under lkcarbon naming the account and who", r.Actor, r.After)
		}
	}
	if links[0].Outcome.Status != 200 && links[1].Outcome.Status != 200 {
		t.Errorf("no successful link row among %+v", links)
	}
}
