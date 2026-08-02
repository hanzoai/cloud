package cloud

// What the gate keys on, what it bills for, and what it says when it stops
// measuring. Every test here drives the real middleware through a real request,
// because the defect each one pins was invisible at the unit level: the sensor
// was correct about the key it was GIVEN, and the gate was giving it a string the
// caller had chosen.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
)

// REGRESSION — a block hold was evaded five times out of five by rotating the
// Authorization header and nothing else. Enforcement was keyed on
// Fingerprint(callerToken): the raw Authorization value, validated or not, which
// is a string the ATTACKER PICKS. A caller presenting garbage was therefore
// HARDER to hold than one presenting nothing at all, which is a perverse
// incentive in exactly the anonymous lane the platform row exists to arm.
func TestAbuseGate_AHoldSurvivesCredentialRotation(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(abuseVerdict(ActionBlock))
	app, _ := abuseApp(t, edge.ModeLive)

	const addr = "203.0.113.200"
	if got := abuseHit(app, "GET", "/v1/models", "", "junk-000000", addr).StatusCode; got != 403 {
		t.Fatalf("the first refusal did not land: %d", got)
	}
	for i := 1; i <= 5; i++ {
		cred := fmt.Sprintf("junk-%06d", i)
		if got := abuseHit(app, "GET", "/v1/models", "", cred, addr).StatusCode; got != 403 {
			t.Fatalf("attempt %d with %q → %d: the hold was evaded by editing one header", i, cred, got)
		}
	}

	// A caller presenting NO credential is held the same way — the two must not
	// differ, or presenting garbage becomes the cheaper attack.
	if got := abuseHit(app, "GET", "/v1/models", "", "", addr).StatusCode; got != 403 {
		t.Fatalf("the same address with no credential → %d, want 403", got)
	}

	// A DIFFERENT address is a different caller and is judged on its own.
	if got := abuseHit(app, "GET", "/v1/models", "", "junk-000000", "198.51.100.77").StatusCode; got != 403 {
		t.Fatalf("a different address → %d; the scorer still blocks it on its own merits", got)
	}
}

// The same rotation, seen from the sensor: one address is one row, whatever it
// puts in the header. Keying on the presented value opened a table entry per
// request, which is how the anonymous lane's ceiling was reached on demand.
func TestAbuseGate_RotatingACredentialDoesNotMintCallers(t *testing.T) {
	resetScorer(t)
	app, tr := abuseApp(t, edge.ModeShadow)
	for i := 0; i < 500; i++ {
		abuseHit(app, "GET", "/v1/models", "", fmt.Sprintf("junk-%06d", i), "203.0.113.201")
	}
	v := tr.View("", edge.ModeShadow, abuseNow())
	if len(v.Callers) != 1 {
		t.Fatalf("500 invented credentials from one address opened %d caller rows, want 1", len(v.Callers))
	}
	if v.Callers[0].Requests != 500 {
		t.Fatalf("the address accumulated %d requests, want 500 — the counts reset per credential", v.Callers[0].Requests)
	}
	// The invented credentials are still counted as spread: this is the stuffing
	// signature, and losing it would blind the sensor to the attack it is for.
	if v.Lanes[edge.AgencyBot] == 0 {
		t.Fatalf("an address presenting 500 credentials is the bot lane: %v", v.Lanes)
	}
}

// A VALIDATED credential is still its own caller. The fix is "only a credential
// we issued names a caller", not "credentials stop mattering" — without this the
// whole per-credential sensor would collapse to a per-IP one.
func TestAbuseGate_AValidatedCredentialIsStillItsOwnCaller(t *testing.T) {
	resetScorer(t)
	app, tr := abuseApp(t, edge.ModeShadow)
	abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.5")
	abuseHit(app, "GET", "/v1/models", "acme", "sk-live-2", "203.0.113.5")

	v := tr.View("acme", edge.ModeShadow, abuseNow())
	if len(v.Callers) != 2 {
		t.Fatalf("two validated credentials from one address = %d rows, want 2", len(v.Callers))
	}
	for _, c := range v.Callers {
		if len(c.Cred) != fingerprintLen {
			t.Fatalf("a validated caller must be keyed by its fingerprint, got %q", c.Cred)
		}
	}
}

// The scorer must be asked about the SAME thing the sensor holds. Asking about a
// string the caller picked would let a refused caller be re-judged as somebody
// else by editing one header — the hold would stand and the question would not.
func TestAbuseGate_TheScorerIsAskedAboutTheCallerNotTheHeader(t *testing.T) {
	resetScorer(t)
	var subjects []string
	SetRiskScorer(func(_ context.Context, _ string, q RiskQuery) (RiskVerdict, error) {
		subjects = append(subjects, q.Subject.ID)
		return RiskVerdict{ID: "d", Action: ActionAllow}, nil
	})
	app, _ := abuseApp(t, edge.ModeLive)
	abuseHit(app, "GET", "/v1/models", "", "junk-a", "203.0.113.210")
	abuseHit(app, "POST", "/v1/iam/mint-user-keys", "", "junk-b", "203.0.113.210")

	if len(subjects) < 2 {
		t.Fatalf("the scorer was asked %d times, want at least 2", len(subjects))
	}
	if subjects[0] != subjects[1] {
		t.Fatalf("one caller was asked about as two subjects (%q, %q)", subjects[0], subjects[1])
	}
	if subjects[0] != "203.0.113.210" {
		t.Fatalf("subject = %q, want the address — the only identity an unvalidated caller has", subjects[0])
	}
}

// A caller the sensor's ceiling turned away is UNMEASURED, and must not be read
// as a caller making its first request: that pattern screens EVERY time, so a
// flood the sensor cannot track would become one scorer call and one metered
// screen per request — a bill and a stampede, produced by the bound that was
// supposed to protect the process.
func TestAbuseGate_AnUnmeasuredCallerIsNotScreenedAsNew(t *testing.T) {
	resetScorer(t)
	asked := 0
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		asked++
		return RiskVerdict{ID: "d", Action: ActionAllow}, nil
	})
	app, tr := abuseApp(t, edge.ModeLive)

	// Fill the anonymous lane's ceiling with live callers. The ceiling is read off
	// the report rather than hard-coded: the report is where an operator reads it.
	ceiling := tr.View("", edge.ModeLive, time.Now()).Ceiling
	for i := 0; i < ceiling; i++ {
		tr.Observe(edge.Signal{Org: "", IP: fmt.Sprintf("10.%d.%d.%d", i/65536, i/256%256, i%256),
			Path: "/v1/models", Class: edge.CredAnonymous}, time.Now())
	}
	before := asked
	for i := 0; i < 20; i++ {
		abuseHit(app, "GET", "/v1/models", "", "", fmt.Sprintf("198.51.%d.%d", i/256, i%256))
	}
	if asked != before {
		t.Fatalf("the scorer was asked %d times about callers the sensor could not measure", asked-before)
	}

	// A privileged grant is still screened: that branch protects the grant, not
	// the sensor.
	before = asked
	abuseHit(app, "POST", "/v1/iam/mint-user-keys", "", "", "198.51.200.1")
	if asked == before {
		t.Fatal("a privileged grant must be screened even when the sensor is saturated")
	}

	// And the saturation is READABLE, in the lane that has no tenant.
	if v := tr.View("", edge.ModeLive, time.Now()); v.Strain != edge.StrainRefuse || v.Refused == 0 {
		t.Fatalf("the anonymous lane's saturation is not readable: %+v", v)
	}
}

// Money: a screen the scorer never answered is judgement not rendered, and
// charging for it would put an outage of ours on a customer's invoice.
func TestAbuseGate_OnlyAScoredScreenIsBilled(t *testing.T) {
	g := &abuseGate{}
	for _, tc := range []struct {
		v    RiskVerdict
		bill bool
	}{
		{RiskVerdict{ID: "d", Action: ActionAllow}, true},
		{RiskVerdict{ID: "d", Action: ActionBlock, Score: 0.9}, true},
		{RiskVerdict{Action: ActionAllow, Refusal: RefusalAbsent}, false},
		{RiskVerdict{Action: ActionAllow, Refusal: RefusalBusy}, false},
		{RiskVerdict{Action: ActionAllow, Refusal: RefusalStuck}, false},
		{RiskVerdict{Action: ActionAllow, Refusal: RefusalTimeout}, false},
		{RiskVerdict{Action: ActionAllow, Refusal: RefusalError}, false},
		{RiskVerdict{Action: ActionAllow, Refusal: RefusalSilent}, false},
		{RiskVerdict{Action: ActionBlock, Refusal: RefusalUnknown}, false},
	} {
		if got := g.bill("acme", "default", "req-1", "203.0.113.1", tc.v); got != tc.bill {
			t.Errorf("bill(%+v) = %v, want %v", tc.v, got, tc.bill)
		}
	}
}

// And the unanswered screens are COUNTED, so a scorer that has silently stopped
// answering is a number on the org's own report rather than a quiet day.
func TestAbuseGate_UnansweredScreensAreOnTheOrgsReport(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		return RiskVerdict{}, nil // installed, answers with nothing
	})
	app, tr := abuseApp(t, edge.ModeLive)
	for i := 0; i < 3; i++ {
		abuseHit(app, "GET", "/v1/models", "acme", fmt.Sprintf("sk-live-%d", i), "203.0.113.5")
	}
	v := tr.View("acme", edge.ModeLive, abuseNow())
	if v.Screens != 3 || v.Unscored != 3 {
		t.Fatalf("screens=%d unscored=%d, want 3 and 3 — a dark scorer must be visible", v.Screens, v.Unscored)
	}
}

// The lane split is the one number this report exists for, and it used to be a
// constant: the gate passed the lane IN, before it had asked the sensor what the
// caller had been doing, so every request ever counted landed in "unknown".
func TestAbuseGate_TheLaneSplitIsTheRealLane(t *testing.T) {
	resetScorer(t)
	app, tr := abuseApp(t, edge.ModeShadow)
	abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.5")
	abuseHit(app, "GET", "/v1/models", "acme", "eyJhbGciOi.payload.sig", "203.0.113.6")

	v := tr.View("acme", edge.ModeShadow, abuseNow())
	if v.Lanes[edge.AgencyAgent] != 1 || v.Lanes[edge.AgencyHuman] != 1 {
		t.Fatalf("lanes = %v, want one agent and one human", v.Lanes)
	}
	if v.Lanes[edge.AgencyUnknown] != 0 {
		t.Fatalf("attributable traffic landed in %q: %v", edge.AgencyUnknown, v.Lanes)
	}
}

// THE DEPLOYMENT THIS RUNS IN TODAY. The DigitalOcean load balancer in front of
// the ingress is TCP with TLS passthrough and no PROXY protocol, so traefik's
// peer is the balancer's own VPC address and no client address reaches this
// process at all. ClientIP correctly answers "" — every hop in the chain is ours
// — and an anonymous caller therefore has NO identity.
//
// The dangerous spelling is to key that under the empty address: one row for the
// entire internet, counts that add up to a stuffing signature within seconds, and
// a single held verdict that refuses everybody. The sensor must decline to
// attribute it, keep counting the volume, and say what it cannot see.
func TestAbuseGate_NoClientAddressIsNotOneGiantCaller(t *testing.T) {
	resetScorer(t)
	asked := 0
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		asked++
		return RiskVerdict{ID: "d", Action: ActionBlock, Cause: "peers"}, nil
	})
	app, tr := abuseApp(t, edge.ModeLive)

	// No X-Forwarded-For at all, which in this deployment is every internet
	// request: 40 different callers, none of whom can be told apart.
	for i := 0; i < 40; i++ {
		if got := abuseHit(app, "GET", "/v1/models", "", fmt.Sprintf("junk-%03d", i), "").StatusCode; got != 200 {
			t.Fatalf("request %d → %d: one verdict was enforced against unidentifiable traffic", i, got)
		}
	}
	if asked != 0 {
		t.Fatalf("the scorer was asked %d times about a caller that has no identity", asked)
	}

	v := tr.View("", edge.ModeLive, time.Now())
	if v.Blind != 40 || v.Strain != edge.StrainBlind {
		t.Fatalf("the sensor must report what it cannot see: blind=%d strain=%q", v.Blind, v.Strain)
	}
	if v.Requests != 40 {
		t.Fatalf("the volume must still be visible: requests=%d, want 40", v.Requests)
	}
	if len(v.Callers) != 0 {
		t.Fatalf("unidentifiable traffic produced %d caller rows: %+v", len(v.Callers), v.Callers)
	}

	// A VALIDATED credential still names its caller in the same deployment — the
	// credentialed plane keeps working with no client address at all.
	abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "")
	if c := tr.View("acme", edge.ModeLive, time.Now()).Callers; len(c) != 1 {
		t.Fatalf("a validated caller with no address must still be its own row, got %+v", c)
	}
}
