package integrations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// linear_test.go proves the inbound half of the Linear connector: the HMAC is
// fail-closed, a stale delivery is refused, the tenant is derived ONLY from the
// signed organization id (a spoofed X-Org-Id is ignored), an Issue event lands in
// the todo sink under the Linear identifier, and Issue and Comment events both
// reach the automations trigger.

type issueCapture struct {
	mu   sync.Mutex
	rows []cloud.IssueUpsert
}

func (c *issueCapture) last() cloud.IssueUpsert {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rows[len(c.rows)-1]
}

func (c *issueCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.rows)
}

func useIssueSink(t *testing.T) *issueCapture {
	t.Helper()
	c := &issueCapture{}
	cloud.RegisterIssueSink(func(_ context.Context, in cloud.IssueUpsert) (cloud.IssueUpsertResult, error) {
		c.mu.Lock()
		c.rows = append(c.rows, in)
		c.mu.Unlock()
		return cloud.IssueUpsertResult{Created: true}, nil
	})
	t.Cleanup(func() { cloud.RegisterIssueSink(nil) })
	return c
}

type triggerCapture struct {
	mu    sync.Mutex
	names []string
	orgs  []string
}

func useTrigger(t *testing.T) *triggerCapture {
	t.Helper()
	orig := fireAutomation
	t.Cleanup(func() { fireAutomation = orig })
	c := &triggerCapture{}
	SetAutomationTrigger(func(_ context.Context, org, _, name, _ string, _ int, _ map[string]any) (int, error) {
		c.mu.Lock()
		c.names = append(c.names, name)
		c.orgs = append(c.orgs, org)
		c.mu.Unlock()
		return 1, nil
	})
	return c
}

func linearSign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// linearPayload builds a delivery envelope with a fresh timestamp.
func linearPayload(t *testing.T, orgID, typ, action string, data map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"action": action, "type": typ, "organizationId": orgID,
		"webhookTimestamp": time.Now().UnixMilli(), "data": data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func linearIssueData() map[string]any {
	return map[string]any{
		"id": "issue-uuid-1", "identifier": "ENG-7", "title": "Billing tests fail",
		"description": "flaky on main", "url": "https://linear.app/acme/issue/ENG-7",
		"state":    map[string]any{"name": "In Progress", "type": "started"},
		"team":     map[string]any{"key": "ENG", "name": "Engineering"},
		"assignee": map[string]any{"name": "Sam", "email": "sam@acme.test"},
		"labels":   []map[string]any{{"name": "bug"}, {"name": "billing"}},
	}
}

func linearPost(t *testing.T, app *zip.App, sig, org string, payload []byte) httpResult {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, "/v1/integration/linear/webhook", bytes.NewReader(payload))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("Linear-Delivery", "d-"+linearSign("delivery", payload)[:8])
	if sig != "" {
		rq.Header.Set("Linear-Signature", sig)
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("webhook: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Body: b}
}

func TestVerifyLinearSignature(t *testing.T) {
	body := []byte(`{"a":1}`)
	good := linearSign("s3cr3t", body)
	if !verifyLinearSignature("s3cr3t", good, body) {
		t.Fatal("valid signature must verify")
	}
	for name, tc := range map[string]struct{ secret, sig string }{
		"empty secret":  {"", good},
		"empty sig":     {"s3cr3t", ""},
		"wrong secret":  {"other", good},
		"not hex":       {"s3cr3t", "zz"},
		"tampered body": {"s3cr3t", linearSign("s3cr3t", []byte(`{"a":2}`))},
	} {
		if verifyLinearSignature(tc.secret, tc.sig, body) {
			t.Fatalf("%s must fail closed", name)
		}
	}
}

func TestLinearLabelsDecodeBothWires(t *testing.T) {
	var a, b linearIssue
	if err := json.Unmarshal([]byte(`{"labels":[{"name":"x"},{"name":" "}]}`), &a); err != nil || len(a.Labels) != 1 || a.Labels[0] != "x" {
		t.Fatalf("webhook shape: %v %v", a.Labels, err)
	}
	if err := json.Unmarshal([]byte(`{"labels":{"nodes":[{"name":"y"}]}}`), &b); err != nil || len(b.Labels) != 1 || b.Labels[0] != "y" {
		t.Fatalf("graphql shape: %v %v", b.Labels, err)
	}
	if linearState("completed") != "closed" || linearState("canceled") != "closed" || linearState("started") != "open" || linearState("") != "open" {
		t.Fatal("state fold")
	}
}

func TestLinearWebhookMirrorsAndTriggers(t *testing.T) {
	sink := useIssueSink(t)
	trig := useTrigger(t)
	const secret = "lin_secret_for_acme"
	const betaSecret = "lin_secret_for_beta"
	app := newApp(t, newKMS(t))
	ctx := context.Background()
	// What claim writes: the organization → org row, and the org's own secret.
	_ = mounted.State.store.Upsert(ctx, Connection{Org: "acme", Provider: "linear", ExternalID: "org-acme"})
	_ = mounted.State.store.Upsert(ctx, Connection{Org: "beta", Provider: "linear", ExternalID: "org-beta"})
	if err := sealTokens(mounted, kmsPath("acme", "linear"), map[string]string{linearWebhookSecret: secret}); err != nil {
		t.Fatal(err)
	}
	if err := sealTokens(mounted, kmsPath("beta", "linear"), map[string]string{linearWebhookSecret: betaSecret}); err != nil {
		t.Fatal(err)
	}

	// A signed Issue create for acme's organization mirrors under acme.
	p := linearPayload(t, "org-acme", "Issue", "create", linearIssueData())
	if r := linearPost(t, app, linearSign(secret, p), "beta", p); r.Code != http.StatusOK {
		t.Fatalf("issue create want 200, got %d (%s)", r.Code, r.Body)
	}
	if sink.count() != 1 {
		t.Fatalf("sink rows = %d, want 1", sink.count())
	}
	row := sink.last()
	if row.Org != "acme" || row.ExtRef != "linear:ENG-7" || row.Repo != "ENG" || row.State != "open" ||
		row.Assignee != "Sam" || len(row.Labels) != 2 || row.ProjectKey != linearTodoProjectKey {
		t.Fatalf("mirrored row wrong: %+v", row)
	}
	if len(trig.names) != 1 || trig.names[0] != "issue.create" || trig.orgs[0] != "acme" {
		t.Fatalf("trigger = %v %v", trig.names, trig.orgs)
	}

	// The other organization maps to its own org, and only ITS secret verifies it.
	p = linearPayload(t, "org-beta", "Issue", "update", linearIssueData())
	if r := linearPost(t, app, linearSign(secret, p), "", p); r.Code != http.StatusUnauthorized {
		t.Fatalf("acme's secret over beta's delivery want 401, got %d", r.Code)
	}
	if r := linearPost(t, app, linearSign(betaSecret, p), "", p); r.Code != http.StatusOK {
		t.Fatalf("beta update want 200, got %d (%s)", r.Code, r.Body)
	}
	if sink.last().Org != "beta" {
		t.Fatalf("beta row org = %q", sink.last().Org)
	}

	// A Comment reaches the trigger and touches no row.
	p = linearPayload(t, "org-acme", "Comment", "create", map[string]any{"id": "c1", "body": "@hanzo take a look"})
	if r := linearPost(t, app, linearSign(secret, p), "", p); r.Code != http.StatusOK {
		t.Fatalf("comment want 200, got %d", r.Code)
	}
	if sink.count() != 2 || trig.names[len(trig.names)-1] != "comment.create" {
		t.Fatalf("comment: rows=%d triggers=%v", sink.count(), trig.names)
	}

	// Fail closed: bad signature, no signature, stale delivery, unknown organization.
	p = linearPayload(t, "org-acme", "Issue", "create", linearIssueData())
	if r := linearPost(t, app, linearSign("wrong", p), "", p); r.Code != http.StatusUnauthorized {
		t.Fatalf("bad sig want 401, got %d", r.Code)
	}
	if r := linearPost(t, app, "", "", p); r.Code != http.StatusUnauthorized {
		t.Fatalf("no sig want 401, got %d", r.Code)
	}
	stale, _ := json.Marshal(map[string]any{"action": "create", "type": "Issue", "organizationId": "org-acme",
		"webhookTimestamp": time.Now().Add(-5 * time.Minute).UnixMilli(), "data": linearIssueData()})
	if r := linearPost(t, app, linearSign(secret, stale), "", stale); r.Code != http.StatusUnauthorized {
		t.Fatalf("stale want 401, got %d", r.Code)
	}
	p = linearPayload(t, "org-nobody", "Issue", "create", linearIssueData())
	if r := linearPost(t, app, linearSign(secret, p), "", p); r.Code != http.StatusOK {
		t.Fatalf("unknown org want 200, got %d", r.Code)
	}
	if sink.count() != 2 {
		t.Fatalf("refused deliveries must not reach the sink: rows=%d", sink.count())
	}
}

// TestLinearCommentResolvesIdentifier drives the comment op against a mock Linear:
// the identifier is looked up, then the mutation runs with the resolved id.
func TestLinearCommentResolvesIdentifier(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		calls = append(calls, in.Query)
		switch {
		case bytes.Contains([]byte(in.Query), []byte("issue(id:")):
			if in.Variables["id"] != "ENG-7" {
				t.Errorf("lookup id = %v", in.Variables["id"])
			}
			_, _ = io.WriteString(w, `{"data":{"issue":{"id":"uuid-7"}}}`)
		case bytes.Contains([]byte(in.Query), []byte("commentCreate")):
			if in.Variables["issueId"] != "uuid-7" {
				t.Errorf("mutation issueId = %v", in.Variables["issueId"])
			}
			_, _ = io.WriteString(w, `{"data":{"commentCreate":{"success":true,"comment":{"id":"c9","url":"https://linear.app/acme/issue/ENG-7#comment-c9"}}}}`)
		default:
			_, _ = io.WriteString(w, `{"errors":[{"message":"unexpected"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	prev := linearAPI
	linearAPI = srv.URL
	t.Cleanup(func() { linearAPI = prev })

	out, err := linearCommentWith(context.Background(), "tok", &linearCommentIn{Issue: "ENG-7", Body: "on it"})
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != "c9" || len(calls) != 2 {
		t.Fatalf("out=%+v calls=%d", out, len(calls))
	}
}
