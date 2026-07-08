package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	out, err := runXVerifyFollow(context.Background(), RunContext{
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

	out2, err := runXVerifyFollow(context.Background(), RunContext{
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
	_, err := runXVerifyFollow(context.Background(), RunContext{
		Token: tokenFail, Input: map[string]any{"targetUser": "hanzoai", "userId": "123"},
	})
	if err == nil || !strings.Contains(err.Error(), "x not connected") {
		t.Fatalf("expected 'x not connected', got %v", err)
	}
}

func TestDiscordVerifyMember(t *testing.T) {
	status := http.StatusOK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bot ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"user":{"id":"u"}}`))
	}))
	defer ts.Close()
	old := discordAPIBase
	discordAPIBase = ts.URL
	defer func() { discordAPIBase = old }()

	status = http.StatusOK
	out, err := runDiscordVerifyMember(context.Background(), RunContext{
		Token: tokenOK, Input: map[string]any{"guildId": "g1", "userId": "u1"},
	})
	if err != nil {
		t.Fatalf("verify_member: %v", err)
	}
	m := out.(map[string]any)
	if m["verified"] != true || m["source"] != "social:discord:join" {
		t.Fatalf("expected member verified, got %v", m)
	}

	status = http.StatusNotFound
	out2, err := runDiscordVerifyMember(context.Background(), RunContext{
		Token: tokenOK, Input: map[string]any{"guildId": "g1", "userId": "u2"},
	})
	if err != nil {
		t.Fatalf("verify_member2: %v", err)
	}
	if out2.(map[string]any)["verified"] != false {
		t.Fatalf("expected not member, got %v", out2)
	}
}

func TestDiscordVerifyMemberFailClosed(t *testing.T) {
	_, err := runDiscordVerifyMember(context.Background(), RunContext{
		Token: tokenFail, Input: map[string]any{"guildId": "g1", "userId": "u1"},
	})
	if err == nil || !strings.Contains(err.Error(), "discord not connected") {
		t.Fatalf("expected 'discord not connected', got %v", err)
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

func TestWaitlistAwardPoints(t *testing.T) {
	cap := &capturedAward{}
	ts := stubAwardServer(cap)
	defer ts.Close()
	t.Setenv(waitlistURLEnv, ts.URL)
	t.Setenv(waitlistSecretEnv, "s3cr3t")

	out, err := runWaitlistAward(context.Background(), RunContext{Input: map[string]any{
		"waitlist": "demo", "email": "bob@example.com", "source": "social:x:follow", "dedupKey": "social:x:follow",
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
}

func TestWaitlistAwardFailClosed(t *testing.T) {
	t.Setenv(waitlistURLEnv, "")
	t.Setenv(waitlistSecretEnv, "")
	_, err := runWaitlistAward(context.Background(), RunContext{Input: map[string]any{
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

	verifyOut, err := runXVerifyFollow(context.Background(), RunContext{
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
	_, err = runWaitlistAward(context.Background(), RunContext{
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
