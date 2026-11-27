package auto

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// tokenOK returns a fake custodied token; tokenFail simulates a provider whose
// credential was never sealed (the fail-closed path).
func tokenOK(string) ([]byte, error)   { return []byte("fake-token"), nil }
func tokenFail(string) ([]byte, error) { return nil, fmt.Errorf("no custody for provider") }

func TestXVerifyFollow(t *testing.T) {
	following := []map[string]any{{"id": "1", "username": "hanzoai"}, {"id": "2", "username": "someoneelse"}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": following})
	}))
	defer ts.Close()
	old := xAPIBase
	xAPIBase = ts.URL
	defer func() { xAPIBase = old }()

	out, err := runXVerifyFollow(context.Background(), provider.RunContext{
		Token: tokenOK, Input: map[string]any{"targetUser": "hanzoai", "userId": "123"},
	})
	if err != nil {
		t.Fatalf("verify_follow: %v", err)
	}
	m := out.(map[string]any)
	if m["verified"] != true {
		t.Fatalf("expected verified true, got %v", m)
	}
	if m["source"] != "social:x:follow" || m["dedupKey"] != "social:x:follow" {
		t.Fatalf("bad source/dedupKey: %v", m)
	}

	out2, err := runXVerifyFollow(context.Background(), provider.RunContext{
		Token: tokenOK, Input: map[string]any{"targetUser": "notfollowed", "userId": "123"},
	})
	if err != nil {
		t.Fatalf("verify_follow2: %v", err)
	}
	if out2.(map[string]any)["verified"] != false {
		t.Fatalf("expected verified false, got %v", out2)
	}
}

func TestXVerifyFollowFailClosed(t *testing.T) {
	_, err := runXVerifyFollow(context.Background(), provider.RunContext{
		Token: tokenFail, Input: map[string]any{"targetUser": "hanzoai", "userId": "123"},
	})
	if err == nil || !strings.Contains(err.Error(), "x not connected") {
		t.Fatalf("expected 'x not connected', got %v", err)
	}
}

type capturedAward struct {
	auth string
	body map[string]any
}

func stubAwardServer(cap *capturedAward) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/waitlist/award" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		cap.auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &cap.body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "awarded": 15, "alreadyAwarded": false,
			"source": cap.body["source"], "points": 15, "rank": 3, "total": 100,
		})
	}))
}

// awardTrail mounts auto with an audit trail, as Use does, and returns it.
func awardTrail(t *testing.T) *audit.Recorder {
	t.Helper()
	rec, err := audit.Open(t.TempDir(), "auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	prev := mounted
	mounted = &cloud.Service[state]{State: state{audit: rec}}
	t.Cleanup(func() { mounted = prev; _ = rec.Close() })
	return rec
}

// sudo is a SuperAdmin's call.
var sudo = zip.WithCaller(context.Background(), zip.Caller{Org: "admin", User: "admin/z", Email: "z@hanzo.ai", Owner: "admin", Admin: true})

func awards(t *testing.T, rec *audit.Recorder, result string) []audit.Record {
	t.Helper()
	rows, _, err := rec.Query(context.Background(), audit.Filter{Action: "waitlist.award", Result: result})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestWaitlistAwardPoints(t *testing.T) {
	rec := awardTrail(t)
	cap := &capturedAward{}
	ts := stubAwardServer(cap)
	defer ts.Close()
	t.Setenv(waitlistURLEnv, ts.URL)
	t.Setenv(waitlistSecretEnv, "s3cr3t")

	out, err := runWaitlistAward(sudo, provider.RunContext{Org: "admin", Input: map[string]any{
		"waitlist": "demo", "email": "bob@example.com", "source": "social:x:follow", "dedupKey": "social:x:follow",
		"points": 1000000,
	}})
	if err != nil {
		t.Fatalf("award_points: %v", err)
	}
	if cap.auth != "Bearer s3cr3t" {
		t.Fatalf("bad auth forwarded: %q", cap.auth)
	}
	if cap.body["waitlist"] != "demo" || cap.body["source"] != "social:x:follow" || cap.body["email"] != "bob@example.com" {
		t.Fatalf("bad forwarded body: %v", cap.body)
	}
	if out.(map[string]any)["awarded"].(float64) != 15 {
		t.Fatalf("expected awarded 15, got %v", out)
	}
	if _, ok := cap.body["points"]; ok {
		t.Fatalf("a caller-stated amount was forwarded: %v", cap.body)
	}
	made := awards(t, rec, "success")
	if len(made) != 1 || len(awards(t, rec, "intent")) != 1 {
		t.Fatalf("%d award records and %d intents, want 1 each", len(made), len(awards(t, rec, "intent")))
	}
	var got waitlistAward
	if err := json.Unmarshal(made[0].After, &got); err != nil {
		t.Fatal(err)
	}
	if got != (waitlistAward{Waitlist: "demo", Source: "social:x:follow", DedupKey: "social:x:follow", Target: "email:b***@example.com", Points: 15}) || !made[0].Auth.IsAdmin || made[0].Actor.Email != "z@hanzo.ai" {
		t.Fatalf("record %+v %+v", got, made[0])
	}
}

// Nobody but a SuperAdmin awards, and no caller awards a stated amount: each
// refusal reaches no waitlist and is on the trail.
func TestWaitlistAwardRefuses(t *testing.T) {
	rec := awardTrail(t)
	cap := &capturedAward{}
	ts := stubAwardServer(cap)
	defer ts.Close()
	t.Setenv(waitlistURLEnv, ts.URL)
	t.Setenv(waitlistSecretEnv, "s3cr3t")
	in := func(source string) provider.RunContext {
		return provider.RunContext{Org: "acme", Input: map[string]any{"waitlist": "demo", "email": "me@acme.test", "source": source, "points": 1000000}}
	}
	for what, ctx := range map[string]context.Context{
		"member":          zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "acme/u"}),
		"org admin":       zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "acme/u", OrgAdmin: true}),
		"owner, no bit":   zip.WithCaller(context.Background(), zip.Caller{Org: "admin", User: "admin/u", Owner: "admin"}),
		"a flow's worker": context.Background(),
	} {
		if _, err := runWaitlistAward(ctx, in("social:x:follow")); err == nil {
			t.Errorf("%s awarded", what)
		}
	}
	for _, source := range []string{"grant", "GRANT", "Grant"} {
		if _, err := runWaitlistAward(sudo, in(source)); err == nil {
			t.Errorf("a SuperAdmin's tool awarded source %s", source)
		}
	}
	if cap.body != nil {
		t.Fatalf("a refused award reached the waitlist: %v", cap.body)
	}
	if n := len(awards(t, rec, "deny")); n != 7 {
		t.Fatalf("%d refusals on the trail, want 7", n)
	}
}

// A trail that will not take the intent stops the award before the waitlist is
// asked; a waitlist that refuses is recorded as an error.
func TestWaitlistAwardRecordsIntentFirst(t *testing.T) {
	rec := awardTrail(t)
	cap := &capturedAward{}
	ts := stubAwardServer(cap)
	defer ts.Close()
	t.Setenv(waitlistURLEnv, ts.URL)
	t.Setenv(waitlistSecretEnv, "s3cr3t")
	in := provider.RunContext{Org: "admin", Input: map[string]any{"waitlist": "demo", "email": "b@e.com", "source": "social:x:follow"}}
	_ = rec.Close()
	if _, err := runWaitlistAward(sudo, in); err == nil || cap.body != nil {
		t.Fatalf("awarded with a trail that refused the intent: %v %v", err, cap.body)
	}
	rec = awardTrail(t)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer down.Close()
	t.Setenv(waitlistURLEnv, down.URL)
	if _, err := runWaitlistAward(sudo, in); err == nil {
		t.Fatal("a refused award reported success")
	}
	if len(awards(t, rec, "intent")) != 1 || len(awards(t, rec, "error")) != 1 || len(awards(t, rec, "success")) != 0 {
		t.Fatal("the trail does not say the award was attempted and failed")
	}
}

// With no trail there is no award.
func TestWaitlistAwardNeedsTheTrail(t *testing.T) {
	prev := mounted
	mounted = nil
	t.Cleanup(func() { mounted = prev })
	cap := &capturedAward{}
	ts := stubAwardServer(cap)
	defer ts.Close()
	t.Setenv(waitlistURLEnv, ts.URL)
	t.Setenv(waitlistSecretEnv, "s3cr3t")
	if _, err := runWaitlistAward(sudo, provider.RunContext{Input: map[string]any{"waitlist": "demo", "email": "b@e.com", "source": "social:x:follow"}}); err == nil || cap.body != nil {
		t.Fatalf("awarded with no trail: %v %v", err, cap.body)
	}
}

func TestWaitlistAwardFailClosed(t *testing.T) {
	t.Setenv(waitlistURLEnv, "")
	t.Setenv(waitlistSecretEnv, "")
	_, err := runWaitlistAward(context.Background(), provider.RunContext{Input: map[string]any{
		"waitlist": "demo", "email": "b@e.com", "source": "grant",
	}})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected 'not configured', got %v", err)
	}
}

// TestPipelineVerifyThenAward threads a verifier's output into award_points — the
// exact composition a flow performs: verify -> {source,dedupKey} -> award.
func TestPipelineVerifyThenAward(t *testing.T) {
	xs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "9", "username": "hanzoai"}}})
	}))
	defer xs.Close()
	oldX := xAPIBase
	xAPIBase = xs.URL
	defer func() { xAPIBase = oldX }()

	verifyOut, err := runXVerifyFollow(context.Background(), provider.RunContext{
		Token: tokenOK, Input: map[string]any{"targetUser": "hanzoai", "userId": "42"},
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	v := verifyOut.(map[string]any)
	if v["verified"] != true {
		t.Fatal("precondition: expected verified")
	}

	cap := &capturedAward{}
	as := stubAwardServer(cap)
	defer as.Close()
	t.Setenv(waitlistURLEnv, as.URL)
	t.Setenv(waitlistSecretEnv, "flow-secret")

	// Thread verify output -> award input (what the engine's PrevOutputs threads).
	// A flow step carries no SuperAdmin of its own, so this is one a SuperAdmin ran.
	awardTrail(t)
	_, err = runWaitlistAward(sudo, provider.RunContext{
		PrevOutputs: map[string]any{"verify": v},
		Input: map[string]any{
			"waitlist": "demo", "email": "flowuser@example.com",
			"source": v["source"], "dedupKey": v["dedupKey"],
		},
	})
	if err != nil {
		t.Fatalf("award: %v", err)
	}
	if cap.body["source"] != "social:x:follow" || cap.auth != "Bearer flow-secret" {
		t.Fatalf("pipeline did not thread verify->award: %v (auth %q)", cap.body, cap.auth)
	}
}
