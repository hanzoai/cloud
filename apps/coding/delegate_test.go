package coding

// A run's harness answers the gateway with its PERSON's delegated token, minted
// for that run (key.go, cloud.Delegate), and never with this deployment's own.

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
)

const theMachine = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhZG1pbi9oYW56by1jbG91ZCJ9.MACHINE"

// jwtWith is a token-shaped string whose payload says scope; the runner never
// verifies it — IAM signed it, and the gateway verifies it — it only checks the
// scope before a box is handed it.
func jwtWith(scope, mark string) string {
	b, _ := json.Marshal(map[string]string{"scope": scope, "sub": "alice", "mark": mark})
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(b) + ".SIG"
}

// fakeIAM answers the delegation grant and records each request it was asked.
type fakeIAM struct {
	mu      sync.Mutex
	asked   []url.Values
	revoked []url.Values
	status  int
	token   func(n int) string
	// life is the expires_in IAM answers; zero answers 600.
	life int
	// trail is the audit trail the process keeps while IAM is served: a credential that renews as a
	// person is never unrecorded (key.go), so a run without one is refused.
	trail *memTrail
	// answer, when set, is the status IAM answers the nth delegation with (0 is a token), taking
	// the place of status; hold, when set, may hold a request until the channel it names is closed
	// (or the caller gives up), which is a call to IAM that hangs.
	answer func(n int, form url.Values) int
	hold   func(form url.Values) <-chan struct{}
	// actorHold, when set, may hold a request for the deployment's own token (the client_credentials
	// grant) until the channel it names is closed or the caller gives up; actorLife is the expires_in
	// that grant answers, zero answering an hour; actors counts the grants asked.
	actorHold func() <-chan struct{}
	actorLife int
	actors    int
}

// useRealSource makes the runner read its own identity through the real token source against this
// IAM, as production does, rather than a static token.
func (f *fakeIAM) useRealSource(t *testing.T) {
	t.Helper()
	real := iam.Machine()
	prev := source
	source = func() oauth2.TokenSource { return real }
	t.Cleanup(func() { source = prev })
}

// setActorHold changes whether the deployment's own token is held, under the lock the handlers read it by.
func (f *fakeIAM) setActorHold(fn func() <-chan struct{}) {
	f.mu.Lock()
	f.actorHold = fn
	f.mu.Unlock()
}

func (f *fakeIAM) actorGrants() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.actors
}

// setAnswer and setHold change what IAM does from now on, under the lock its handlers read them by.
func (f *fakeIAM) setAnswer(fn func(n int, form url.Values) int) {
	f.mu.Lock()
	f.answer = fn
	f.mu.Unlock()
}

func (f *fakeIAM) setHold(fn func(form url.Values) <-chan struct{}) {
	f.mu.Lock()
	f.hold = fn
	f.mu.Unlock()
}

// asks is every delegation IAM was asked for, in order.
func (f *fakeIAM) asks() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// revokedTokens is every token IAM was asked to revoke, in order.
func (f *fakeIAM) revokedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.revoked))
	for _, r := range f.revoked {
		out = append(out, r.Get("token"))
	}
	return out
}

func (f *fakeIAM) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path == "/v1/iam/oauth/revoke" && r.Method == http.MethodPost {
			f.mu.Lock()
			f.revoked = append(f.revoked, r.PostForm)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.URL.Path != "/v1/iam/oauth/token" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.PostForm.Get("grant_type") == "client_credentials" {
			f.mu.Lock()
			f.actors++
			n, hold, life := f.actors, f.actorHold, cmp.Or(f.actorLife, 3600)
			f.mu.Unlock()
			if hold != nil {
				if wait := hold(); wait != nil {
					select {
					case <-wait:
					case <-r.Context().Done():
						return
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "machine-" + strconv.Itoa(n), "token_type": "Bearer", "expires_in": life})
			return
		}
		f.mu.Lock()
		f.asked = append(f.asked, r.PostForm)
		n := len(f.asked)
		hold, answer := f.hold, f.answer
		f.mu.Unlock()
		if hold != nil {
			if wait := hold(r.PostForm); wait != nil {
				select {
				case <-wait:
				case <-r.Context().Done():
					return
				}
			}
		}
		status := f.status
		if answer != nil {
			status = answer(n, r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			code, why := "access_denied", "the subject is not a member of acme"
			switch status {
			case http.StatusServiceUnavailable:
				code, why = "temporarily_unavailable", "delegation key not mounted"
			case http.StatusUnauthorized:
				code, why = "invalid_client", "the client credential is not known"
			case http.StatusBadRequest:
				code, why = "invalid_grant", "the actor token is not this client's own"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": why})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": f.token(n), "token_type": "Bearer", "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
			"expires_in": cmp.Or(f.life, 600), "scope": "ai:inference",
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "cloud-secret")
	prev := source
	source = func() oauth2.TokenSource { return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: theMachine}) }
	t.Cleanup(func() { source = prev })
	f.trail = &memTrail{}
	l := ledgerOf(t)
	l.trail = f.trail
	flights.keep(l)
	t.Cleanup(func() { flights.keep(nil) })
}

func agentRan(p *pod) (argv []string, fed string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, a := range p.ran {
		if slices.Contains(a, "--skip-git-repo-check") {
			return a, p.fed[i], true
		}
	}
	return nil, "", false
}

func headPod() *pod {
	return &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
}

func TestARunCarriesItsPersonsDelegatedTokenAndNotTheMachines(t *testing.T) {
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+strconv.Itoa(n)) }}
	iam.serve(t)
	p := headPod()
	servePod(t, p)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	run := aRun()
	if _, err := (sandboxRunner{}).Run(ctx, "acme", "9b1d0a2e-alice", run, nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(iam.asked) != 1 {
		t.Fatalf("IAM was asked %d times, want once for the run", len(iam.asked))
	}
	a := iam.asked[0]
	life, _ := strconv.Atoi(a.Get("lifetime"))
	want := map[string]string{
		"grant_type":        "urn:ietf:params:oauth:grant-type:token-exchange",
		"client_id":         "hanzo-cloud",
		"actor_token":       theMachine,
		"requested_subject": "9b1d0a2e-alice",
		"org":               "acme",
		"scope":             "ai:inference",
		"resource":          "https://" + cloud.Domain(),
		"run":               run.SessionID,
	}
	for k, v := range want {
		if a.Get(k) != v {
			t.Errorf("IAM was asked %s=%q, want %q", k, a.Get(k), v)
		}
	}
	if life <= 0 || life > 600 {
		t.Errorf("lifetime = %d, want the run's remaining budget (≤ 600s)", life)
	}

	argv, fed, ok := agentRan(p)
	if !ok {
		t.Fatal("the agent never ran")
	}
	cred, _, _ := strings.Cut(fed, "\n")
	if cred != jwtWith("ai:inference", "run-1") {
		t.Fatalf("HANZO_API_KEY = %q, want the delegated token IAM minted for this run", cred)
	}
	// The machine token reaches no command, by stdin or by argv.
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.ran {
		if strings.Contains(strings.Join(p.ran[i], " "), theMachine) || strings.Contains(p.fed[i], theMachine) {
			t.Fatalf("command %d carries the machine token", i)
		}
	}
	if strings.Contains(strings.Join(argv, " "), cred) {
		t.Fatal("the delegated token is on argv")
	}
	// It ends with the run: revoked at IAM, as the client it was delegated to.
	if len(iam.revoked) != 1 || iam.revoked[0].Get("token") != cred ||
		iam.revoked[0].Get("client_id") != "hanzo-cloud" || iam.revoked[0].Get("client_secret") != "cloud-secret" {
		t.Fatalf("revocations = %v, want the run's token revoked once by hanzo-cloud", iam.revoked)
	}
	// It is blinded out of everything the run writes.
	if i := slices.IndexFunc(p.ran, func(a []string) bool { return slices.Contains(a, "--skip-git-repo-check") }); !slices.Contains(p.blind[i], cred) {
		t.Fatalf("the delegated token is not blinded: %v", p.blind[i])
	}
}

// Each run gets its own.
func TestEachRunMintsItsOwn(t *testing.T) {
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+strconv.Itoa(n)) }}
	iam.serve(t)
	for n := 1; n <= 2; n++ {
		p := headPod()
		servePod(t, p)
		if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "hanzo/alice", aRun(), nil); err != nil {
			t.Fatalf("run %d: %v", n, err)
		}
		_, fed, _ := agentRan(p)
		if cred, _, _ := strings.Cut(fed, "\n"); cred != jwtWith("ai:inference", "run-"+strconv.Itoa(n)) {
			t.Fatalf("run %d carried %q", n, cred)
		}
	}
	if len(iam.asked) != 2 {
		t.Fatalf("IAM asked %d times for two runs", len(iam.asked))
	}
}

// IAM refusing is the run's failure — never a cue to spend as the deployment — and
// a token IAM answers that is not confined is refused before any box holds it.
func TestARunIsNeverHandedAnythingElse(t *testing.T) {
	for name, iam := range map[string]*fakeIAM{
		"refused":      {status: http.StatusForbidden},
		"not confined": {token: func(int) string { return jwtWith("openid profile", "wide") }},
		"too short":    {token: func(int) string { return jwtWith("ai:inference", "short") }, life: 30},
	} {
		t.Run(name, func(t *testing.T) {
			iam.serve(t)
			p := headPod()
			servePod(t, p)
			_, err := (sandboxRunner{}).Run(context.Background(), "acme", "hanzo/alice", aRun(), nil)
			if err == nil || !strings.Contains(err.Error(), "delegated") && !strings.Contains(err.Error(), "granted") {
				t.Fatalf("run err = %v, want the delegation's refusal", err)
			}
			if _, _, ok := agentRan(p); ok {
				t.Fatal("the agent ran without its person's token")
			}
		})
	}
}

// IAM with delegation switched on and no delegation key mounted answers 503
// "delegation key not mounted" (and still signs everything else). The run is
// refused with those words — never started on anything else.
func TestARunNamesAnUnmountedDelegationKey(t *testing.T) {
	iam := &fakeIAM{status: http.StatusServiceUnavailable}
	iam.serve(t)
	p := headPod()
	servePod(t, p)
	_, err := (sandboxRunner{}).Run(context.Background(), "acme", "hanzo/alice", aRun(), nil)
	if err == nil || !strings.Contains(err.Error(), "delegation key not mounted") {
		t.Fatalf("run err = %v, want the run refused naming the unmounted delegation key", err)
	}
	if _, _, ok := agentRan(p); ok {
		t.Fatal("the agent ran without its person's token")
	}
}

// A person whose wallet pays for nothing gets the model API's 402 on the run's
// first model call: the delegated token bills them, never the deployment. The run asks
// the free model once, and when that is refused too it PARKS (park.go) rather than
// failing: nothing it wrote is lost, its sandbox is given back, and it goes on when
// credits are added. It writes nothing to its branch when it had written nothing, and
// still ends its token.
//
// The refusal is ai's (routers' limitReached, insufficient_balance) as the harness
// prints a refused turn: "unexpected status <status>: <error.message>".
func TestARunTheModelAPIRefusesForMoneySaysSo(t *testing.T) {
	const refused = "unexpected status 402 Payment Required: e2e-paid is paid from funds you add, or by a paid plan; " +
		"granted credit does not pay for it. Add funds or upgrade: https://hanzo.ai/pay"
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+strconv.Itoa(n)) }}
	iam.serve(t)
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		case slices.Contains(argv, "--skip-git-repo-check"):
			turn, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": refused}})
			return client.Ran{ExitCode: 1, Stdout: `{"type":"thread.started","thread_id":"t1"}` + "\n" +
				`{"type":"turn.started"}` + "\n" + string(turn) + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	res, err := (sandboxRunner{}).Run(context.Background(), "bare", "uuid-carol", aRun(), nil)
	if err != nil {
		t.Fatalf("a refused model call is the run's reported failure, not a transport error: %v", err)
	}
	if res.OK || res.Changed {
		t.Fatalf("a run whose model calls were refused reports work: %+v", res)
	}
	if res.Park == nil || res.Park.Reason != agents.ParkPayment || !res.Park.At.IsZero() || res.Error != "" {
		t.Fatalf("the run reads %+v / %q, want it parked for payment with no time", res.Park, res.Error)
	}
	if p.ends != 1 {
		t.Fatalf("a parked run ended %d sandboxes, want its box given back", p.ends)
	}
	if p.gitVerb("push") {
		t.Fatalf("a refused run pushed:\n%s", strings.Join(p.lines(), "\n"))
	}
	p.mu.Lock()
	agents := 0
	for _, a := range p.ran {
		if slices.Contains(a, "--skip-git-repo-check") {
			agents++
		}
	}
	p.mu.Unlock()
	if agents != 2 {
		t.Fatalf("the agent ran %d times; a refusal for money is asked once more, on %s", agents, codingFallback)
	}
	p.mu.Lock()
	var last []string
	for _, a := range p.ran {
		if slices.Contains(a, "--skip-git-repo-check") {
			last = a
		}
	}
	p.mu.Unlock()
	if !slices.Contains(last, codingFallback) {
		t.Fatalf("the retry did not ask %s: %q", codingFallback, last)
	}
	if len(iam.revoked) != 1 {
		t.Fatalf("revocations = %d, want the run's token ended once", len(iam.revoked))
	}
}

// A run on no remote keeps its work in its sandbox alone, so it cannot give the sandbox
// back and wait: the refusal ends it as it always has, in the API's own words.
func TestARunWithNoBranchTheModelAPIRefusesForMoneyFails(t *testing.T) {
	const refused = "unexpected status 402 Payment Required: add funds or upgrade: https://hanzo.ai/pay"
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+strconv.Itoa(n)) }}
	iam.serve(t)
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		case slices.Contains(argv, "--skip-git-repo-check"):
			turn, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": refused}})
			return client.Ran{ExitCode: 1, Stdout: `{"type":"thread.started","thread_id":"t1"}` + "\n" + string(turn) + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	run := aRun()
	run.Local = true
	res, err := (sandboxRunner{}).Run(context.Background(), "bare", "uuid-carol", run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "the dev harness exited 1: " + refused; res.OK || res.Park != nil || res.Error != want {
		t.Fatalf("a run with nowhere to keep its work reads %+v / %q, want it failed as %q", res.Park, res.Error, want)
	}
}

func TestScienceRunRequestsConfinedScienceScopesAndFallsBackOnLegacyIAM(t *testing.T) {
	iam := &fakeIAM{
		token: func(n int) string {
			if n == 1 {
				return jwtWith("ai:inference research workflow:gpu", "sci-1")
			}
			return jwtWith("ai:inference", "fallback-"+strconv.Itoa(n))
		},
	}
	iam.serve(t)
	p := headPod()
	servePod(t, p)

	run := aRun()
	run.Remote = "https://github.com/hanzoai/ml.git"
	if _, err := (sandboxRunner{}).Run(context.Background(), "hanzo", "hanzo/sci", run, nil); err != nil {
		t.Fatalf("science run: %v", err)
	}
	asks := iam.asks()
	if len(asks) != 1 || asks[0].Get("scope") != "ai:inference research workflow:gpu" {
		t.Fatalf("IAM asks = %v, want scope 'ai:inference research workflow:gpu'", asks)
	}

	// Against an older IAM that refuses the sub-scopes with invalid_scope, key() falls back to ai:inference.
	prevSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path == "/v1/iam/oauth/revoke" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("scope") != "ai:inference" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_scope", "error_description": "legacy iam"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": jwtWith("ai:inference", "legacy-ok"),
			"token_type":   "Bearer", "expires_in": 600, "scope": "ai:inference",
		})
	}))
	t.Cleanup(prevSrv.Close)
	t.Setenv("IAM_URL", prevSrv.URL)

	p2 := headPod()
	servePod(t, p2)
	if _, err := (sandboxRunner{}).Run(context.Background(), "hanzo", "hanzo/sci", run, nil); err != nil {
		t.Fatalf("legacy IAM fallback run: %v", err)
	}
}

func TestScienceScopesAvoidsFalsePositives(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  RunRequest
		want bool
	}{
		{"hanzoai/ml https", RunRequest{Remote: "https://github.com/hanzoai/ml.git"}, true},
		{"hanzo-inc/kai ssh", RunRequest{Remote: "git@git.hanzo.ai:hanzo-inc/kai.git"}, true},
		{"zenlm repo", RunRequest{Remote: "https://github.com/zenlm/zen-reranker"}, true},
		{"explicit @sci directive", RunRequest{Remote: "https://github.com/hanzoai/bot.git", DeveloperInstructions: "Act as @sci on this run."}, true},
		{"explicit research.bench directive", RunRequest{DeveloperInstructions: "Dispatch research.bench on fleet."}, true},
		{"unrelated html repo", RunRequest{Remote: "https://github.com/hanzoai/html.git"}, false},
		{"unrelated yaml repo", RunRequest{Remote: "https://github.com/hanzoai/yaml.git"}, false},
		{"unrelated kaizen repo", RunRequest{Remote: "https://github.com/hanzoai/kaizen.git"}, false},
		{"unrelated zenith repo", RunRequest{Remote: "https://github.com/hanzoai/zenith.git"}, false},
		{"foreign org ml repo", RunRequest{Remote: "https://github.com/stranger/ml.git"}, false},
		{"email handle in instructions", RunRequest{Instructions: "Contact dev@science.example or ops@sci-ops.io"}, false},
		{"prompt mentioning science paths", RunRequest{
			Remote: "https://github.com/hanzoai/console.git",
			Prompt: "Edit index.html, config.yaml, kaizen, zenith, /v1/research/runs and research.bench docs",
		}, false},
	} {
		got := len(scienceScopes(tc.req)) > 0
		if got != tc.want {
			t.Errorf("%s: scienceScopes(%+v) = %v, want %v", tc.name, tc.req, scienceScopes(tc.req), tc.want)
		}
	}
}
