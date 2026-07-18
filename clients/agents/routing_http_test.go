package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// doKey is a keyless-body request with a machine claim key (X-Target-Key) attached.
func doKey(t *testing.T, app *zip.App, method, path, org, key string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if key != "" {
		req.Header.Set(claimKeyHeader, key)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// registerAndMint registers a target for org and mints its claim key, returning
// (targetID, claimKey).
func registerAndMint(t *testing.T, app *zip.App, org, host string) (string, string) {
	t.Helper()
	code, body := do(t, app, "POST", "/v1/agents/targets", org, map[string]any{"label": host, "host": host})
	if code != 201 && code != 200 {
		t.Fatalf("register target: %d %s", code, body)
	}
	var tv struct{ ID string `json:"id"` }
	_ = json.Unmarshal(body, &tv)
	code, body = doKey(t, app, "POST", "/v1/agents/targets/"+tv.ID+"/claim-key", org, "")
	if code != 200 {
		t.Fatalf("mint claim key: %d %s", code, body)
	}
	var kv struct{ ClaimKey string `json:"claimKey"` }
	_ = json.Unmarshal(body, &kv)
	if kv.ClaimKey == "" {
		t.Fatal("claim key empty")
	}
	return tv.ID, kv.ClaimKey
}

// A claim without the machine's key, or with the WRONG key, is refused — org
// membership alone is not enough to claim a machine's runs.
func TestClaim_RequiresMachineKey(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	old := claimLongPoll
	claimLongPoll = 150 * time.Millisecond
	defer func() { claimLongPoll = old }()

	id, key := registerAndMint(t, app, "acme", "evo")

	// No key => 403.
	if code, _ := doKey(t, app, "POST", "/v1/agents/targets/"+id+"/claim", "acme", ""); code != 403 {
		t.Fatalf("claim with no key must be 403, got %d", code)
	}
	// Wrong key => 403.
	if code, _ := doKey(t, app, "POST", "/v1/agents/targets/"+id+"/claim", "acme", "tgtk_wrong"); code != 403 {
		t.Fatalf("claim with wrong key must be 403, got %d", code)
	}
	// Right key, no work => 204 (never 200, never another tenant's run).
	if code, _ := doKey(t, app, "POST", "/v1/agents/targets/"+id+"/claim", "acme", key); code != 204 {
		t.Fatalf("claim with right key + no work must be 204, got %d", code)
	}
}

// THE machine boundary: a key minted for target A cannot claim target B, and a
// different org cannot claim at all — even with a real key for its own target.
func TestClaim_CrossMachineAndCrossOrgDenied(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	old := claimLongPoll
	claimLongPoll = 150 * time.Millisecond
	defer func() { claimLongPoll = old }()

	idA, keyA := registerAndMint(t, app, "acme", "evoA")
	idB, _ := registerAndMint(t, app, "acme", "evoB")

	// A's key against B => 403 (constant-time mismatch on B's stored hash).
	if code, _ := doKey(t, app, "POST", "/v1/agents/targets/"+idB+"/claim", "acme", keyA); code != 403 {
		t.Fatalf("A's key claiming B must be 403, got %d", code)
	}

	// Offer a run for acme/idA, then a DIFFERENT org cannot claim idA at all (its
	// org scope resolves no such target => 403), and the run is never handed out.
	OfferRoutedRun(RoutedRun{Org: "acme", TargetID: idA, SessionID: "sess_a", Repo: "api"})
	if code, _ := doKey(t, app, "POST", "/v1/agents/targets/"+idA+"/claim", "evil", keyA); code != 403 {
		t.Fatalf("another org claiming acme's target must be 403, got %d", code)
	}
	// acme WITH A's key claims its own run.
	code, body := doKey(t, app, "POST", "/v1/agents/targets/"+idA+"/claim", "acme", keyA)
	if code != 200 {
		t.Fatalf("acme must claim its own run, got %d %s", code, body)
	}
	var rv struct{ SessionID string `json:"sessionId"` }
	_ = json.Unmarshal(body, &rv)
	if rv.SessionID != "sess_a" {
		t.Fatalf("claimed wrong run: %s", body)
	}
}

// The end-to-end machine round trip: offer -> claim -> report reaches the durable
// owner awaiting the result.
func TestClaimReport_RoundTrip(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	id, key := registerAndMint(t, app, "acme", "evo")

	off := OfferRoutedRun(RoutedRun{Org: "acme", TargetID: id, SessionID: "sess_rt", Repo: "api", Branch: "agent/rt"})

	code, body := doKey(t, app, "POST", "/v1/agents/targets/"+id+"/claim", "acme", key)
	if code != 200 {
		t.Fatalf("claim: %d %s", code, body)
	}

	got := make(chan RoutedResult, 1)
	go func() { res, _ := off.Await(context.Background()); got <- res }()

	code, _ = doKeyBody(t, app, "POST", "/v1/agents/targets/"+id+"/runs/sess_rt/report", "acme", key,
		map[string]any{"ok": true, "changed": true, "commitSha": "cafe"})
	if code != 200 {
		t.Fatalf("report: %d", code)
	}
	select {
	case res := <-got:
		if !res.OK || res.CommitSha != "cafe" {
			t.Fatalf("report did not reach the owner: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner never received the report")
	}
}

// doKeyBody is doKey with a JSON body.
func doKeyBody(t *testing.T, app *zip.App, method, path, org, key string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if key != "" {
		req.Header.Set(claimKeyHeader, key)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// ---- store-level liveness gate ----

// TargetDispatchable is the fail-closed gate: online + a live runner (a recent
// claim poll). Offline, no key, or a stale poll all reject.
func TestTargetDispatchable_LivenessGate(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	tgt := Target{ID: "t1", Org: "acme", Label: "evo", Kind: TargetMachine, Status: TargetOnline, Host: "evo", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTarget(ctx, tgt); err != nil {
		t.Fatal(err)
	}

	// No claim key yet => not live => not dispatchable.
	if err := s.TargetDispatchable(ctx, "acme", "t1"); err != errTargetNotLive {
		t.Fatalf("no runner => not dispatchable, got %v", err)
	}
	// Mint + a fresh serving stamp => dispatchable.
	if err := s.UpsertClaimKeyHash(ctx, "acme", "t1", hashClaimKey("k"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.StampServing(ctx, "acme", "t1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.TargetDispatchable(ctx, "acme", "t1"); err != nil {
		t.Fatalf("online + fresh runner => dispatchable, got %v", err)
	}
	// A stale serving stamp => not dispatchable (dead runner).
	if err := s.StampServing(ctx, "acme", "t1", now-int64(servingTTL/time.Second)-5); err != nil {
		t.Fatal(err)
	}
	if err := s.TargetDispatchable(ctx, "acme", "t1"); err != errTargetNotLive {
		t.Fatalf("stale runner => not dispatchable, got %v", err)
	}
	// Fresh again but OFFLINE => not dispatchable.
	_ = s.StampServing(ctx, "acme", "t1", time.Now().Unix())
	tgt.Status = TargetOffline
	tgt.UpdatedAt = time.Now().Unix()
	if err := s.UpdateTarget(ctx, tgt); err != nil {
		t.Fatal(err)
	}
	if err := s.TargetDispatchable(ctx, "acme", "t1"); err != errTargetNotReady {
		t.Fatalf("offline => not dispatchable, got %v", err)
	}
	// Unknown target / cross-org => fail closed.
	if err := s.TargetDispatchable(ctx, "acme", "nope"); err != errTargetNotFound {
		t.Fatalf("unknown target => not found, got %v", err)
	}
	if err := s.TargetDispatchable(ctx, "evil", "t1"); err != errTargetNotFound {
		t.Fatalf("cross-org => not found, got %v", err)
	}
}

// The claim key is stored ONLY as a hash; verify is constant-time + fail closed.
func TestClaimKey_HashedAtRestAndVerified(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	_ = s.CreateTarget(ctx, Target{ID: "t1", Org: "acme", Status: TargetOnline, CreatedAt: now, UpdatedAt: now})

	key, _ := newClaimKey()
	if err := s.UpsertClaimKeyHash(ctx, "acme", "t1", hashClaimKey(key), now); err != nil {
		t.Fatal(err)
	}
	// The stored value is a hash, never the plaintext.
	stored, _, _ := s.ClaimKeyHash(ctx, "acme", "t1")
	if stored == key || stored != hashClaimKey(key) {
		t.Fatalf("claim key must be stored as a hash, not plaintext")
	}
	if err := s.verifyClaimKey(ctx, "acme", "t1", key); err != nil {
		t.Fatalf("correct key must verify: %v", err)
	}
	if err := s.verifyClaimKey(ctx, "acme", "t1", "tgtk_wrong"); err != errClaimKeyBad {
		t.Fatalf("wrong key must fail: %v", err)
	}
	if err := s.verifyClaimKey(ctx, "acme", "t1", ""); err != errClaimKeyBad {
		t.Fatalf("empty key must fail: %v", err)
	}
	// Cross-org verify resolves no key => fail closed.
	if err := s.verifyClaimKey(ctx, "evil", "t1", key); err != errNoClaimKey {
		t.Fatalf("cross-org verify must fail closed, got %v", err)
	}
}
