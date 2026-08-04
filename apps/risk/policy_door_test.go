package risk

// policy_door_test.go — the decision regime has ONE address, and a write there
// answers the policy it wrote and nothing else.
//
// These are the tests that fail if the dissolution is undone. The regime used to
// be written at PUT /v1/risk/state/appetite and read at GET /v1/risk/policy: two
// addresses for one plane, the writing one named after the mutable spot the
// regime happened to sit in. Worse, that write answered a fifteen-field report of
// the whole MODEL — what it had learned, its aggregate strain, its blind
// coordinates, its refusals, how much of the event surface had been folded in — to
// a call that changes three numbers.
//
// Two things are held closed here, and each is a real defect if it comes back:
//
//	THE SECOND ADDRESS. A surface that answers at two paths has to keep both
//	true. The old one is gone, and a generated client cannot reach it.
//	THE MODEL IN THE ANSWER. A policy write that reports learned state is a
//	second source for a fact the model owns, computed by a writer whose reason to
//	hold the lock was something else entirely. That is how the regime came to
//	live on the learned state's row in the first place.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
)

// TestPolicy_HasOneAddress: the regime is written and read at /v1/risk/policy,
// and the address that used to write it is GONE.
//
// A 404/405 on the old path is the whole assertion: it is not routed, so it is
// not in the document, so no generated SDK method, CLI command or MCP tool
// projects from it. Re-register it and this fails.
func TestPolicy_HasOneAddress(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 1_000_000})
	const regime = `{"review":0.02,"sample":0.10,"live":false}`

	// The one address takes the write.
	if code, body := req(t, app, http.MethodPut, "/v1/risk/policy", orgA, "u_"+orgA, regime); code != http.StatusOK {
		t.Fatalf("PUT /v1/risk/policy = %d %s", code, body)
	}
	// And the read, which is what makes it one address rather than two.
	if code, body := req(t, app, http.MethodGet, "/v1/risk/policy", orgA, "u_"+orgA, ""); code != http.StatusOK {
		t.Fatalf("GET /v1/risk/policy = %d %s", code, body)
	}
	// The dissolved one takes nothing. Fiber answers an unrouted path 404 and a
	// routed path with the wrong method 405; either proves it is not this plane's
	// door, so both are accepted and anything 2xx is the regression.
	code, body := req(t, app, http.MethodPut, "/v1/risk/state/appetite", orgA, "u_"+orgA, regime)
	if code == http.StatusOK {
		t.Fatalf("PUT /v1/risk/state/appetite still writes the regime (%d %s) — the plane has two "+
			"addresses again, and both have to stay true", code, body)
	}
	if code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /v1/risk/state/appetite = %d %s, want 404 or 405", code, body)
	}
}

// TestPolicy_AWriteAnswersThePolicyAndNotTheModel: the response to a regime
// change carries the version, the history and the bounds — and carries NO field
// of the model.
//
// The named fields are the ones the old answer had: what the model learned, its
// shape, whether it is warm, the threshold in force, the realised share, the
// refusals by reason, the blind coordinates, the fold coverage and the aggregate
// strain. Every one of them is a fact about the model or its telemetry, read from
// the model or from telemetry. Put any of them back on this answer and this test
// says which one.
func TestPolicy_AWriteAnswersThePolicyAndNotTheModel(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 1_000_000})

	code, body := req(t, app, http.MethodPut, "/v1/risk/policy", orgA, "u_"+orgA,
		`{"review":0.02,"sample":0.10,"live":true}`)
	if code != http.StatusOK {
		t.Fatalf("PUT /v1/risk/policy = %d %s", code, body)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	// What a policy write DOES answer.
	for _, want := range []string{"version", "history", "disposed", "retained", "changes", "window"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("the answer to a policy write has no %q: %s", want, body)
		}
	}
	// What it must NEVER answer: the model, and the telemetry of the model.
	for _, forbidden := range []string{
		"shape", "learned", "warm", // what the model IS
		"cut", "stated", "realised", "saturated", // the operating point and its realisation
		"refused", "blind", // telemetry, by reason and by feature
		"surface", "aggregates", // the fold, and the aggregate strain
		"tenant", // the address the caller already knows, echoed back
	} {
		if _, leaked := got[forbidden]; leaked {
			t.Fatalf("a policy write answered %q — that is a fact about the model, not about the "+
				"policy, and reporting it here is a second source for it: %s", forbidden, body)
		}
	}
}

// TestPolicy_TheWriteAnswersWhatTheReadWouldSay: one shape, two verbs.
//
// The write's answer IS the read's answer, so a caller has exactly one type to
// understand for this plane and never has to call GET to find out what its PUT
// did. Give the write its own response shape and the two drift; this fails the
// moment they do.
func TestPolicy_TheWriteAnswersWhatTheReadWouldSay(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 1_000_000})

	code, wrote := req(t, app, http.MethodPut, "/v1/risk/policy", orgA, "u_"+orgA,
		`{"review":0.03,"sample":0.05,"live":false}`)
	if code != http.StatusOK {
		t.Fatalf("PUT /v1/risk/policy = %d %s", code, wrote)
	}
	code, read := req(t, app, http.MethodGet, "/v1/risk/policy", orgA, "u_"+orgA, "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/risk/policy = %d %s", code, read)
	}
	var w, r riskPolicyOut
	if err := json.Unmarshal(wrote, &w); err != nil {
		t.Fatalf("decode write: %v: %s", err, wrote)
	}
	if err := json.Unmarshal(read, &r); err != nil {
		t.Fatalf("decode read: %v: %s", err, read)
	}
	if w.Version != r.Version || len(w.History) != len(r.History) {
		t.Fatalf("the write answered version %d over %d versions and the read answered version %d "+
			"over %d — one plane, one shape, one answer",
			w.Version, len(w.History), r.Version, len(r.History))
	}
	if w.Retained != r.Retained || w.Changes != r.Changes || w.Window != r.Window {
		t.Fatalf("the write and the read publish different bounds: %+v vs %+v", w, r)
	}
}

// TestPolicy_ARestatementNeedsNoFlag: idempotence is READ OFF THE VALUE.
//
// [plane.enact] mints no version for a restatement of the regime in force, so the
// second write answers the same version as the first. That equality IS the signal,
// which is why there is no `minted` boolean: a flag describing the operation is a
// second thing to keep true beside the value that already says it.
func TestPolicy_ARestatementNeedsNoFlag(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 1_000_000})
	const same = `{"review":0.02,"sample":0.10,"live":true}`

	first := putRegime(t, app, same)
	if first.Version != 1 {
		t.Fatalf("the first regime landed as version %d, want 1", first.Version)
	}
	again := putRegime(t, app, same)
	if again.Version != first.Version {
		t.Fatalf("restating the SAME regime moved the version %d → %d; a version means the Nth "+
			"distinct policy adopted, not the Nth save", first.Version, again.Version)
	}
	if len(again.History) != len(first.History) {
		t.Fatalf("restating the same regime grew the history %d → %d", len(first.History), len(again.History))
	}
	// A DIFFERENT regime does mint, which is what makes the equality above mean
	// something rather than being a write that quietly does nothing.
	moved := putRegime(t, app, `{"review":0.04,"sample":0.10,"live":true}`)
	if moved.Version != first.Version+1 {
		t.Fatalf("a genuinely different regime landed as version %d, want %d",
			moved.Version, first.Version+1)
	}
}

// putRegime states a regime and decodes the policy value it answers.
func putRegime(t *testing.T, app *zip.App, body string) riskPolicyOut {
	t.Helper()
	code, raw := req(t, app, http.MethodPut, "/v1/risk/policy", orgA, "u_"+orgA, body)
	if code != http.StatusOK {
		t.Fatalf("PUT /v1/risk/policy = %d %s", code, raw)
	}
	var out riskPolicyOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, raw)
	}
	return out
}
