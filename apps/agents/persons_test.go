package agents

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// cast is an app behind the real identity boundary with the callers a held org
// has: two customers of hanzo, an admin of hanzo, a SuperAdmin acting in hanzo,
// and, in a customer's own org, a member and its admin.
type cast struct {
	app                        *zip.App
	first, second, staff, root string
	firstSub, secondSub        string
	acmeMember, acmeAdmin      string
}

func castApp(t *testing.T) cast {
	t.Helper()
	iss := iamtest.New(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	cloud.Identify(app, &cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)})
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{AI: &fakeAI{content: "the answer"}}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	person := func(sub, name, org, role string) string {
		return iss.Sign(t, iamtest.Claims{Sub: sub, PreferredUsername: name, Owner: org, Orgs: []map[string]any{{"org": org, "role": role}}})
	}
	const firstSub, secondSub = "7a1e0000-0000-4000-8000-0000000000c1", "7a1e0000-0000-4000-8000-0000000000c2"
	return cast{
		app:        app,
		first:      person(firstSub, "first", "hanzo", "admin"),
		second:     person(secondSub, "second", "hanzo", "admin"),
		staff:      person("7a1e0000-0000-4000-8000-0000000000c3", "staff", "hanzo", "admin"),
		root:       person("7a1e0000-0000-4000-8000-0000000000c4", "root", "admin", "owner"),
		acmeMember: person("7a1e0000-0000-4000-8000-0000000000c5", "ann", "acme", "member"),
		acmeAdmin:  person("7a1e0000-0000-4000-8000-0000000000c6", "boss", "acme", "admin"),
		firstSub:   firstSub, secondSub: secondSub,
	}
}

// in sends bearer's request acting in org (the SuperAdmin names the org it acts
// in; everyone else acts in their own).
func (p cast) in(t *testing.T, org, method, path, bearer string, body any) (int, map[string]any) {
	t.Helper()
	return sendAsIn(t, p.app, org, method, path, bearer, body)
}

func runInputs(out map[string]any) []string {
	var got []string
	runs, _ := out["runs"].([]any)
	for _, r := range runs {
		got = append(got, r.(map[string]any)["input"].(string))
	}
	return got
}

func listOf(out map[string]any, key string) []any {
	l, _ := out[key].([]any)
	return l
}

// A run is the person's who asked for it: its input and output are their words.
// In a held org, a second customer, an admin of the org and a SuperAdmin each
// read none of a member's runs — on the org feed, on the agent's history, on the
// agent itself and on the activity feed — and a run no person asked for is a
// SuperAdmin's alone to read. (Regression: GET /v1/agent/runs handed a new
// customer 50 other cast's prompts and answers.)
func TestARunIsItsPersons(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		p := castApp(t)
		if code, out := p.in(t, "hanzo", http.MethodPost, "/v1/agent", p.first, map[string]any{
			"name": "helper", "model": "zen-flash", "cap_micro_usd": 1000000, "max_task_micro_usd": 100000, "period": "month",
		}); code != http.StatusCreated {
			t.Fatalf("create: %d %v", code, out)
		}
		for who, bearer := range map[string]string{"first's secret": p.first, "second's secret": p.second} {
			if code, out := p.in(t, "hanzo", http.MethodPost, "/v1/agent/helper/run", bearer, map[string]any{"input": who}); code != http.StatusOK {
				t.Fatalf("run as %q: %d %v", who, code, out)
			}
		}
		for _, r := range []Run{
			{ID: "run_nobody", Org: "hanzo", AgentName: "helper", Status: "ok", Input: "nobody's", CreatedAt: time.Now().Unix()},
			// The scheduler ran first's agent: the run is first's, who set it running.
			{ID: "run_cron", Org: "hanzo", AgentName: "helper", Status: "ok", Input: "scheduled", Actor: scheduledActor(Agent{Org: "hanzo", Name: "helper"}), CreatedAt: time.Now().Unix()},
		} {
			if err := storeOf(t, &mounted.State, "hanzo").InsertRun(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		}

		want := map[string][]string{"first": {"first's secret", "scheduled"}, "second": {"second's secret"}, "staff": nil, "root": {"nobody's", "scheduled"}}
		for name, bearer := range map[string]string{"first": p.first, "second": p.second, "staff": p.staff, "root": p.root} {
			for _, path := range []string{"/v1/agent/runs", "/v1/agent/helper/runs"} {
				code, out := p.in(t, "hanzo", http.MethodGet, path, bearer, nil)
				if got := runInputs(out); code != http.StatusOK || !sameStrings(got, want[name]) {
					t.Fatalf("%s reads %s: %d %v; want %v", name, path, code, got, want[name])
				}
			}
			code, out := p.in(t, "hanzo", http.MethodGet, "/v1/agent/helper", bearer, nil)
			recent := 0
			for _, r := range listOf(out, "recentRuns") {
				in := r.(map[string]any)["input"].(string)
				if !contains(want[name], in) {
					t.Fatalf("%s sees %q among the agent's recent runs", name, in)
				}
				recent++
			}
			if code != http.StatusOK || recent != len(want[name]) {
				t.Fatalf("%s reads the agent: %d, %d recent runs; want %d", name, code, recent, len(want[name]))
			}
			_, feed := p.in(t, "hanzo", http.MethodGet, "/v1/agent/activity", bearer, nil)
			runs := 0
			for _, e := range listOf(feed, "activity") {
				if k := e.(map[string]any)["kind"]; k == "invoked" || k == "failed" {
					runs++
				}
			}
			if runs != len(want[name]) {
				t.Fatalf("%s's activity holds %d runs; want %d", name, runs, len(want[name]))
			}
		}

	})
}

// An agent is the org's to read and run, and its creator's to change or delete:
// another customer of a held org, and an admin of it, are refused, while in a
// customer's own org an admin may manage a member's agent.
func TestAnAgentIsChangedByItsCreator(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		p := castApp(t)
		create := map[string]any{"name": "mine", "model": "zen-flash", "cap_micro_usd": 1000000, "max_task_micro_usd": 100000, "period": "month"}
		if code, out := p.in(t, "hanzo", http.MethodPost, "/v1/agent", p.first, create); code != http.StatusCreated {
			t.Fatalf("create: %d %v", code, out)
		}
		if code, _ := p.in(t, "hanzo", http.MethodGet, "/v1/agent/mine", p.second, nil); code != http.StatusOK {
			t.Fatalf("another member reads the org's agent: %d", code)
		}
		for name, bearer := range map[string]string{"another customer": p.second, "an admin of hanzo": p.staff} {
			if code, out := p.in(t, "hanzo", http.MethodPatch, "/v1/agent/mine", bearer, map[string]any{"instructions": "taken"}); code != http.StatusForbidden {
				t.Fatalf("%s changes it: %d %v", name, code, out)
			}
			if code, out := p.in(t, "hanzo", http.MethodDelete, "/v1/agent/mine", bearer, nil); code != http.StatusForbidden {
				t.Fatalf("%s deletes it: %d %v", name, code, out)
			}
		}
		if code, out := p.in(t, "hanzo", http.MethodPatch, "/v1/agent/mine", p.first, map[string]any{"instructions": "be terse"}); code != http.StatusOK {
			t.Fatalf("its creator changes it: %d %v", code, out)
		}
		if code, out := p.in(t, "hanzo", http.MethodDelete, "/v1/agent/mine", p.first, nil); code != http.StatusNoContent && code != http.StatusOK {
			t.Fatalf("its creator deletes it: %d %v", code, out)
		}

		if code, out := p.in(t, "acme", http.MethodPost, "/v1/agent", p.acmeMember, create); code != http.StatusCreated {
			t.Fatalf("create in acme: %d %v", code, out)
		}
		if code, out := p.in(t, "acme", http.MethodPatch, "/v1/agent/mine", p.acmeAdmin, map[string]any{"instructions": "the admin's"}); code != http.StatusOK {
			t.Fatalf("a customer's own admin manages a member's agent: %d %v", code, out)
		}

	})
}

// A session is the person's who opened it, recorded under the subject the
// boundary attested whatever actor the request states; a session recorded with
// no actor is nobody's: no member and no admin of a held org lists or opens it,
// and a SuperAdmin reads it and acts on none of it.
func TestASessionIsItsPersons(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		p := castApp(t)
		code, out := p.in(t, "hanzo", http.MethodPost, "/v1/agent/sessions", p.first, map[string]any{
			"agent": "hanzo-dev", "title": "first's", "actor": "hanzo/" + p.secondSub,
		})
		id, _ := out["id"].(string)
		if code != http.StatusCreated && code != http.StatusOK || id == "" {
			t.Fatalf("register: %d %v", code, out)
		}
		if actor, _ := out["actor"].(string); actor != "" && actor != "hanzo/"+p.firstSub {
			t.Fatalf("a stated actor was recorded: %q", actor)
		}
		now := time.Now().UnixMilli()
		if err := storeOf(t, &mounted.State, "hanzo").CreateSession(context.Background(), Session{
			ID: "ses_nobody", Org: "hanzo", Agent: "enso", Title: "nobody's", Status: StatusRunning,
			StartedAt: now, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		sessionIDsOf := func(bearer string) map[string]bool {
			t.Helper()
			code, out := p.in(t, "hanzo", http.MethodGet, "/v1/agent/sessions", bearer, nil)
			if code != http.StatusOK {
				t.Fatalf("list: %d %v", code, out)
			}
			got := map[string]bool{}
			for _, s := range listOf(out, "sessions") {
				got[s.(map[string]any)["id"].(string)] = true
			}
			return got
		}
		if got := sessionIDsOf(p.first); !got[id] || got["ses_nobody"] {
			t.Fatalf("first lists %v", got)
		}
		for name, bearer := range map[string]string{"another customer": p.second, "an admin of hanzo": p.staff} {
			if got := sessionIDsOf(bearer); got[id] || got["ses_nobody"] {
				t.Fatalf("%s lists %v", name, got)
			}
			for _, s := range []string{id, "ses_nobody"} {
				if code, _ := p.in(t, "hanzo", http.MethodGet, "/v1/agent/sessions/"+s, bearer, nil); code != http.StatusNotFound {
					t.Fatalf("%s opens %s: %d", name, s, code)
				}
			}
		}
		if got := sessionIDsOf(p.root); !got["ses_nobody"] {
			t.Fatalf("a SuperAdmin does not list the session with no actor: %v", got)
		}
		if code, _ := p.in(t, "hanzo", http.MethodGet, "/v1/agent/sessions/ses_nobody", p.root, nil); code != http.StatusOK {
			t.Fatalf("a SuperAdmin reads the session with no actor: %d", code)
		}
		if code, _ := p.in(t, "hanzo", http.MethodPatch, "/v1/agent/sessions/ses_nobody", p.root, map[string]any{"title": "moved"}); code != http.StatusNotFound {
			t.Fatalf("a SuperAdmin changed the session with no actor: %d", code)
		}

	})
}

// sameStrings reports whether a and b hold the same strings, in any order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !contains(b, x) {
			return false
		}
	}
	return true
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// sendAsIn is sendAs acting in org: the X-Org-Id the boundary resolves the
// caller's acting org from.
func sendAsIn(t *testing.T, app *zip.App, org, method, path, bearer string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}
