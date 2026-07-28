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
	var tv struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &tv)
	code, body = doKey(t, app, "POST", "/v1/agents/targets/"+tv.ID+"/claim-key", org, "")
	if code != 200 {
		t.Fatalf("mint claim key: %d %s", code, body)
	}
	var kv struct {
		ClaimKey string `json:"claimKey"`
	}
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
	var rv struct {
		SessionID string `json:"sessionId"`
	}
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

// ---- M2: owner/machine scoping of the claim-key plane ----

// reqAs sends a request with an EXPLICIT principal (X-User-Id) + optional org-admin
// bit and claim key + JSON body, so a test can prove owner/admin scoping distinct
// from org scoping.
func reqAs(t *testing.T, app *zip.App, method, path, org, user string, admin bool, key string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
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

// registerAs registers a target in org OWNED by the given principal, returning its id.
func registerAs(t *testing.T, app *zip.App, org, user, host string) string {
	t.Helper()
	code, body := reqAs(t, app, "POST", "/v1/agents/targets", org, user, false, "", map[string]any{"label": host, "host": host})
	if code != 201 && code != 200 {
		t.Fatalf("register target: %d %s", code, body)
	}
	var tv struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &tv)
	if tv.ID == "" {
		t.Fatalf("register returned no id: %s", body)
	}
	return tv.ID
}

// A machine belongs to the principal that registered it: a DIFFERENT member of the
// SAME org can neither mint/rotate its claim key, nor patch, nor delete it — only its
// owner or an org admin can. Every refusal collapses to not-found (no oracle).
func TestClaimKeyPlane_OwnerScoped(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	id := registerAs(t, app, "acme", "alice", "evo")

	// A non-owner member of the same org is DENIED on every management verb.
	if code, _ := reqAs(t, app, "POST", "/v1/agents/targets/"+id+"/claim-key", "acme", "mallory", false, "", nil); code != 404 {
		t.Fatalf("non-owner mint must be denied (no oracle -> 404), got %d", code)
	}
	if code, _ := reqAs(t, app, "PATCH", "/v1/agents/targets/"+id, "acme", "mallory", false, "", map[string]any{"status": TargetOffline}); code != 404 {
		t.Fatalf("non-owner patch must be denied, got %d", code)
	}
	if code, _ := reqAs(t, app, "DELETE", "/v1/agents/targets/"+id, "acme", "mallory", false, "", nil); code != 404 {
		t.Fatalf("non-owner delete must be denied, got %d", code)
	}

	// The OWNER can mint.
	code, body := reqAs(t, app, "POST", "/v1/agents/targets/"+id+"/claim-key", "acme", "alice", false, "", nil)
	if code != 200 {
		t.Fatalf("owner mint must succeed, got %d %s", code, body)
	}

	// An ORG ADMIN (self-service org management) can manage any of the org's machines.
	if code, _ := reqAs(t, app, "POST", "/v1/agents/targets/"+id+"/claim-key", "acme", "boss", true, "", nil); code != 200 {
		t.Fatalf("org admin mint must succeed, got %d", code)
	}
	if code, _ := reqAs(t, app, "PATCH", "/v1/agents/targets/"+id, "acme", "boss", true, "", map[string]any{"status": TargetOnline}); code != 200 {
		t.Fatalf("org admin patch must succeed, got %d", code)
	}
}

// A non-owner cannot CLAIM a victim's runs or REPORT on them, even if the claim-key
// authorization were somehow satisfied — the ownership gate refuses first.
func TestClaimReport_OwnerScoped(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	old := claimLongPoll
	claimLongPoll = 150 * time.Millisecond
	defer func() { claimLongPoll = old }()

	id := registerAs(t, app, "acme", "alice", "evo")
	// Alice mints her machine's key.
	code, body := reqAs(t, app, "POST", "/v1/agents/targets/"+id+"/claim-key", "acme", "alice", false, "", nil)
	if code != 200 {
		t.Fatalf("owner mint: %d %s", code, body)
	}
	var kv struct {
		ClaimKey string `json:"claimKey"`
	}
	_ = json.Unmarshal(body, &kv)

	// Mallory (same org, not the owner) with the RIGHT key is still refused: she does
	// not own the machine. (In practice she cannot obtain the key, since mint is
	// owner-scoped — this is the defense-in-depth arm.)
	if code, _ := reqAs(t, app, "POST", "/v1/agents/targets/"+id+"/claim", "acme", "mallory", false, kv.ClaimKey, nil); code != 403 {
		t.Fatalf("non-owner claim must be 403, got %d", code)
	}
	if code, _ := reqAs(t, app, "POST", "/v1/agents/targets/"+id+"/runs/sess_x/report", "acme", "mallory", false, kv.ClaimKey, map[string]any{"ok": true}); code != 403 {
		t.Fatalf("non-owner report must be 403, got %d", code)
	}

	// The owner with her key claims (no work -> 204) and reports fine.
	if code, _ := reqAs(t, app, "POST", "/v1/agents/targets/"+id+"/claim", "acme", "alice", false, kv.ClaimKey, nil); code != 204 {
		t.Fatalf("owner claim (no work) must be 204, got %d", code)
	}
}

// A pre-migration UNOWNED target (owner=”) is admin-only, and its owner heals it by
// re-registering (register binds the owner). Proven at the store + handler seam.
func TestUnownedTarget_AdminOnly_ThenBoundByRegister(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	ctx := context.Background()
	now := time.Now().Unix()
	// Seed an unowned row directly, as an upgraded pre-owner DB would carry.
	if err := mounted.State.store.CreateTarget(ctx, Target{ID: "tgt_legacy", Org: "acme", Owner: "", Label: "old", Kind: TargetMachine, Status: TargetOnline, Host: "old", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// A plain member cannot mint on an unowned row.
	if code, _ := reqAs(t, app, "POST", "/v1/agents/targets/tgt_legacy/claim-key", "acme", "alice", false, "", nil); code != 404 {
		t.Fatalf("unowned row must be member-denied, got %d", code)
	}
	// An org admin can.
	if code, _ := reqAs(t, app, "POST", "/v1/agents/targets/tgt_legacy/claim-key", "acme", "boss", true, "", nil); code != 200 {
		t.Fatalf("unowned row must be admin-manageable, got %d", code)
	}
	// The owner heals it by re-registering the SAME host — register ADOPTS the
	// unowned row and BINDS the owner (no duplicate).
	code, body := reqAs(t, app, "POST", "/v1/agents/targets", "acme", "alice", false, "", map[string]any{"label": "old", "host": "old"})
	if code != 200 && code != 201 {
		t.Fatalf("re-register: %d %s", code, body)
	}
	var tv struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &tv)
	if tv.ID != "tgt_legacy" {
		t.Fatalf("re-register must adopt the unowned row (same id), got %q", tv.ID)
	}
	// Now alice (the bound owner) can mint.
	if code, _ := reqAs(t, app, "POST", "/v1/agents/targets/tgt_legacy/claim-key", "acme", "alice", false, "", nil); code != 200 {
		t.Fatalf("after binding, the owner must be able to mint, got %d", code)
	}
}
