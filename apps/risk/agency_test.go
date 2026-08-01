package risk

// agency_test.go is the regression suite for the forgeable classification.
//
// THE DEFECT: `declared` was `actor.agent != ""`, so any caller could type a name
// and be classified `agent`; the published field said the reference was resolved
// in the org's own registry, and no such lookup existed. Worse, `bot` was
// STRUCTURALLY UNREACHABLE — every op here requires a validated principal, so the
// credential terms the bot lane turned on could never both hold.
//
// Each test below fails if either half comes back.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubRegistry answers for a fixed set of references, counts its calls, and can
// be made unreachable — the three behaviours the classification turns on.
type stubRegistry struct {
	mu    sync.Mutex
	known map[string]bool
	down  bool
	calls int
}

func (s *stubRegistry) declared(_ context.Context, ref string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.down {
		return false, fmt.Errorf("registry unreachable")
	}
	return s.known[ref], nil
}

func (s *stubRegistry) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestAgencyIsResolvedNotAsserted is the headline. A caller naming an agent this
// org never registered must NOT be classified as an agent, and the answer must be
// the strongest thing that fact supports: it claimed an agency we can disprove.
func TestAgencyIsResolvedNotAsserted(t *testing.T) {
	app, s := wireApp(t)
	reg := &stubRegistry{known: map[string]bool{"agent_real": true}}
	s.State.reg = reg

	for _, tc := range []struct {
		name, actor, want, refusal string
	}{
		{"a reference this org registered", `"actor":{"agent":"agent_real"},`, AgencyAgent, ""},
		{"a reference it did not", `"actor":{"agent":"agent_invented"},`, AgencyBot, ""},
		{"a session and no agent", `"actor":{"session":"sess_1"},`, AgencyHuman, ""},
		{"nothing named at all", "", AgencyUnknown, ""},
	} {
		code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
			`{"stage":"signup","subject":{"kind":"account","id":"a1"},`+tc.actor+`"signals":{"ip":"203.0.113.1"}}`)
		if code != http.StatusOK {
			t.Fatalf("%s: decide = %d %s", tc.name, code, body)
		}
		var out riskDecision
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if out.Agency != tc.want {
			t.Errorf("%s: agency = %q, want %q", tc.name, out.Agency, tc.want)
		}
		if tc.refusal != "" && out.Refusal != tc.refusal {
			t.Errorf("%s: refusal = %q, want %q", tc.name, out.Refusal, tc.refusal)
		}
	}
	if reg.count() == 0 {
		t.Fatal("the registry was never asked — the classification is still reading the request body")
	}
}

// TestTheBotLaneIsReachable pins the second half of the defect. A vocabulary term
// no input can produce is not a classification, it is decoration — and this one
// was the product's whole differentiator.
func TestTheBotLaneIsReachable(t *testing.T) {
	app, s := wireApp(t)
	s.State.reg = &stubRegistry{known: map[string]bool{}}

	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"transaction","id":"tx1"},"actor":{"agent":"scripted"}}`)
	if code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	var out riskDecision
	_ = json.Unmarshal(body, &out)
	if out.Agency != AgencyBot {
		t.Fatalf("agency = %q — undeclared automation cannot be reached, so the bad-bot lane classifies nobody", out.Agency)
	}
}

// TestAnUnreachableRegistryIsAGapAndSaysSo. Believing the caller when the check
// could not be made is the forgery back again; silently answering `unknown` is the
// same gap with the evidence removed. The honest answer names itself.
func TestAnUnreachableRegistryIsAGapAndSaysSo(t *testing.T) {
	app, s := wireApp(t)
	s.State.reg = &stubRegistry{known: map[string]bool{"agent_real": true}, down: true}

	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"signup","subject":{"kind":"account","id":"a1"},"actor":{"agent":"agent_real"}}`)
	if code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	var out riskDecision
	_ = json.Unmarshal(body, &out)
	if out.Agency == AgencyAgent {
		t.Fatal("an unreachable registry classified a claimed reference as an agent — the claim was believed")
	}
	if out.Agency != AgencyUnknown {
		t.Fatalf("agency = %q, want unknown when the registry could not answer", out.Agency)
	}
	if out.Refusal != RefusalUnverified {
		t.Fatalf("refusal = %q, want %q — an unchecked classification that does not say so is indistinguishable from a checked one",
			out.Refusal, RefusalUnverified)
	}
}

// TestTheLookupIsMemoisedPerTenantAndBounded pins the amplification bound. One
// request must not become one internal call, or a caller with a loop turns a
// decision into a fan-out; and the memo must be this tenant's own, with this
// tenant's own eviction.
func TestTheLookupIsMemoisedPerTenantAndBounded(t *testing.T) {
	app, s := wireApp(t)
	reg := &stubRegistry{known: map[string]bool{"agent_real": true}}
	s.State.reg = reg

	for i := 0; i < 5; i++ {
		code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
			`{"stage":"signup","subject":{"kind":"account","id":"a1"},"actor":{"agent":"agent_real"}}`)
		if code != http.StatusOK {
			t.Fatalf("decide = %d %s", code, body)
		}
	}
	if n := reg.count(); n != 1 {
		t.Fatalf("five decisions made %d registry calls — one request is one internal call, which is a fan-out a caller controls", n)
	}

	// A negative is memoised too: the flood case is exactly the uncached one if
	// only the positives are kept.
	for i := 0; i < 3; i++ {
		req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
			`{"stage":"signup","subject":{"kind":"account","id":"a1"},"actor":{"agent":"nope"}}`)
	}
	if n := reg.count(); n != 2 {
		t.Fatalf("three decisions naming one unknown reference made %d total calls, want 2 — a negative is not memoised", n)
	}

	// And the memo is bounded, by this tenant's own eviction.
	cache := &resOf(t, s, Tenant("hanzo/acme")).agency
	now := time.Now()
	for i := 0; i < agencyCacheMax*2; i++ {
		cache.put(fmt.Sprintf("ref-%d", i), false, now)
	}
	cache.mu.Lock()
	held := len(cache.at)
	cache.mu.Unlock()
	if held > agencyCacheMax {
		t.Fatalf("the agency memo holds %d entries against a bound of %d", held, agencyCacheMax)
	}
}

// TestOneTenantsRegistryAnswerIsNotAnothers. The memo is per resident, so a
// reference that resolves for one org can never resolve for another off the back
// of it — which would be a cross-tenant read of the registry.
func TestOneTenantsRegistryAnswerIsNotAnothers(t *testing.T) {
	app, s := wireApp(t)
	// The registry answers for whoever asks; the ISOLATION being tested is that
	// A's cached "yes" is not readable by B, so B's own lookup decides.
	reg := &perOrgRegistry{yes: map[string]bool{"agent_a": true}}
	s.State.reg = reg

	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"signup","subject":{"kind":"account","id":"a1"},"actor":{"agent":"agent_a"}}`)
	if code != http.StatusOK {
		t.Fatalf("A decide = %d %s", code, body)
	}
	var mine riskDecision
	_ = json.Unmarshal(body, &mine)
	if mine.Agency != AgencyAgent {
		t.Fatalf("A's own agent classified %q", mine.Agency)
	}

	// B names the SAME reference. It is not B's, so B must not inherit the answer.
	reg.yes = map[string]bool{} // whoever asks now, the answer is no
	code, body = req(t, app, http.MethodPost, "/v1/risk/decide", "beta", "u_beta",
		`{"stage":"signup","subject":{"kind":"account","id":"b1"},"actor":{"agent":"agent_a"}}`)
	if code != http.StatusOK {
		t.Fatalf("B decide = %d %s", code, body)
	}
	var theirs riskDecision
	_ = json.Unmarshal(body, &theirs)
	if theirs.Agency == AgencyAgent {
		t.Fatal("B read A's cached registry answer — one tenant's agent became another's")
	}
}

type perOrgRegistry struct{ yes map[string]bool }

func (p *perOrgRegistry) declared(_ context.Context, ref string) (bool, error) {
	return p.yes[ref], nil
}

// TestTheDocumentDoesNotClaimWhatTheCodeDoesNot. The published Agency field once
// described a registry lookup that did not exist. Prose is the contract an SDK
// user and a model read, so a claim in it is a claim we owe.
func TestTheDocumentDoesNotClaimWhatTheCodeDoesNot(t *testing.T) {
	body, err := os.ReadFile("typed.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	src := string(body)
	// Whatever the field says, it must not promise facts nothing computes. These
	// two were in the old text and neither was ever derived.
	for _, gone := range []string{"the credential class", "the account's metered shape"} {
		if strings.Contains(src, gone) {
			t.Errorf("the published Agency description still promises %q, and nothing computes it", gone)
		}
	}
	if !strings.Contains(src, "looked up in") {
		t.Error("the published Agency description no longer says the reference is looked up — the one thing that IS true of it")
	}
}
