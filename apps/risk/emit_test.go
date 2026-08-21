package risk

// emit_test.go — the decision, on the shared event plane, held to the four
// properties that make a telemetry hand-off safe to put behind a payment.
//
//	IT HAPPENS. A decision nobody can query is not a control anybody can operate,
//	so the decide path is asserted to state one — end to end, through planeDecide.
//	IT CANNOT FAIL THE DECISION. The peer is made to refuse, and then to hang, and
//	the door still answers its verdict.
//	IT CARRIES NO RAW SUBJECT AND NO RAW AMOUNT. Asserted over the WHOLE rendered
//	occurrence, not over the fields the test remembered to look at.
//	SHADOW IS STATED AND SAID SO. That is the entire value of a shadow regime:
//	what the gate WOULD have done, on real traffic, changing nothing.

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"

	"github.com/hanzoai/cloud"
	contract "github.com/hanzoai/cloud/plane"
	peer "github.com/hanzoai/cloud/plane/event"
)

// filed is one emit as the plane received it: what was said, and whom it was said
// for. The org is read off the CALL rather than off the body, because the body
// cannot carry one — which is exactly the property under test.
type filed struct {
	org string
	in  *contract.EventIn
}

// intercept substitutes the plane call with one that records, and restores it.
//
// The buffer is what keeps the substitution honest under the emit's own ceiling:
// an unbuffered channel would make every emit block until the test read it, which
// is the failure mode the ceiling exists to survive rather than one under test.
func intercept(t *testing.T) <-chan filed {
	t.Helper()
	ch := make(chan filed, maxEmits)
	prev := send
	send = func(ctx context.Context, in *contract.EventIn) (*contract.EventCaptured, error) {
		ch <- filed{org: cloud.Who(ctx).Org, in: in}
		return &contract.EventCaptured{Accepted: 1}, nil
	}
	t.Cleanup(func() { send = prev })
	return ch
}

// await reads one emit, or fails saying what its absence means.
func await(t *testing.T, ch <-chan filed) filed {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("the decide path stated nothing on the shared event plane — the decision is " +
			"visible only to the process that made it, which is the whole defect this closes")
		return filed{}
	}
}

// attr reads one attribute off a stated occurrence.
func attr(in *contract.EventIn, name string) (string, bool) {
	for _, a := range in.Attributes {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

// TestSendDefaultsToTheRealPeerCall pins what intercept SUBSTITUTES: in
// production the emit is the generated client's call and nothing else.
//
// A seam is a var, so it is exactly as easy to rebind at the DECLARATION as in a
// test — and rebound to a func returning nil, every decision would go silently
// unstated while every test that installs its own fake first kept passing. So the
// default is asserted by code pointer: the point is the IDENTITY of the callee,
// and reaching a live analytics socket to observe its behaviour is exactly what a
// unit test cannot do.
func TestSendDefaultsToTheRealPeerCall(t *testing.T) {
	got := reflect.ValueOf(send).Pointer()
	want := reflect.ValueOf(peer.EventCapture).Pointer()
	if got != want {
		t.Error("the plane call does not default to the generated client — a substituted emit " +
			"drops every decision while the door still answers a verdict")
	}
}

// TestPlaneDecide_StatesTheDecisionOnTheSharedPlane is the point of the change,
// asserted end to end: one decide, one occurrence, on the shared plane, under the
// tenant the decision was REACHED for.
//
// Mutation proof: drop the emit call from planeDecide and this fails on the
// absence; take the org from anywhere but the decision's own tenant and it fails
// on the tenant.
func TestPlaneDecide_StatesTheDecisionOnTheSharedPlane(t *testing.T) {
	probe.reset(true)
	mountApp(t)
	ch := intercept(t)

	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindAccount, Subject: "u_412",
		Signals: []contract.Signal{
			{Name: contract.SignalNano, Value: "420000000"},
			{Name: contract.SignalCountry, Value: "gb"},
		},
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	f := await(t, ch)

	// THE TENANT. The bare org the decision acted for — the same slug the event
	// plane files a row under, never the qualified key and never another org.
	if f.org != orgA {
		t.Errorf("the occurrence was stated for org %q, want %q — a decision describes the "+
			"organisation it was reached for and no other", f.org, orgA)
	}
	if f.org == string(key(t, brandA, orgA)) {
		t.Error("the qualified tenant key was sent as the org — the event plane's tenant is the bare slug")
	}
	// WHAT IT IS.
	if f.in.Name != nameDecided || f.in.Product != surface {
		t.Errorf("stated {name:%q product:%q}, want {%q %q}", f.in.Name, f.in.Product, nameDecided, surface)
	}
	// WHAT IT SAYS. Every field the verdict carries, read back off the occurrence.
	for _, want := range []struct{ name, value string }{
		{attrStage, cloud.StagePayment},
		{attrKind, contract.KindAccount},
		{attrAction, out.Action},
		{attrPolicy, strconv.Itoa(out.Policy)},
		{attrCountry, "GB"},
		{attrValue, bracketUnder},
	} {
		value, ok := attr(f.in, want.name)
		if !ok {
			t.Errorf("the occurrence carries no %q — the decision is on the plane and unreadable", want.name)
			continue
		}
		if value != want.value {
			t.Errorf("%s = %q, want %q", want.name, value, want.value)
		}
	}
	// The refusal is the field to read FIRST, and it is present exactly when the
	// verdict is not a scored one — an empty one stored as a value would read as a
	// refusal named "".
	refusal, stated := attr(f.in, attrRefusal)
	if stated != (out.Refusal != "") || refusal != out.Refusal {
		t.Errorf("refusal on the occurrence is %q (present:%v), the verdict's is %q — "+
			"an unscored allow and a clean one must never be the same row", refusal, stated, out.Refusal)
	}
	if _, ok := attr(f.in, attrPosture); !ok {
		t.Error("the occurrence does not say whether the verdict was live or shadow")
	}
}

// TestPlaneDecide_AnUnreachablePlaneDoesNotFailTheDecision. The gate that asks
// this scorer is the CREDIT DOOR: a telemetry outage that refused a payment would
// be a control firing on exactly the customers it must not fire on.
//
// Both shapes of outage, because they fail differently. A peer that REFUSES is
// the easy one. A peer that HANGS is the one that would take the door with it,
// and it is why the emit is detached rather than merely error-tolerant.
//
// Mutation proof: make the emit blocking (call the peer on this goroutine, or
// wait on its result) and the hanging case never answers.
func TestPlaneDecide_AnUnreachablePlaneDoesNotFailTheDecision(t *testing.T) {
	probe.reset(true)
	mountApp(t)

	// Released at the end of the test, so the wedged emit is not still holding a
	// slot when the next one runs.
	stuck := make(chan struct{})
	defer close(stuck)

	for _, tc := range []struct {
		name string
		peer func(context.Context, *contract.EventIn) (*contract.EventCaptured, error)
	}{
		{
			name: "the peer refuses",
			peer: func(context.Context, *contract.EventIn) (*contract.EventCaptured, error) {
				return nil, errors.New("cloud: app is not deployed here: analytics")
			},
		},
		{
			name: "the peer never answers",
			peer: func(ctx context.Context, _ *contract.EventIn) (*contract.EventCaptured, error) {
				<-stuck // ignores the context, exactly as a wedged socket does
				return nil, ctx.Err()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := send
			send = tc.peer
			t.Cleanup(func() { send = prev })

			type answered struct {
				out *contract.RiskDecided
				err error
			}
			done := make(chan answered, 1)
			go func() {
				out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
					Stage: cloud.StagePayment, Kind: contract.KindAccount, Subject: "u_412",
				})
				done <- answered{out, err}
			}()
			select {
			case a := <-done:
				if a.err != nil {
					t.Fatalf("planeDecide: %v — a telemetry failure became a refused decision", a.err)
				}
				if a.out.Action != cloud.ActionAllow {
					t.Errorf("action %q, want %q — the verdict is the model's, not the plane's",
						a.out.Action, cloud.ActionAllow)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the door did not answer while the event plane was unreachable — the emit " +
					"is on the decision's path, and a telemetry outage can now refuse a payment")
			}
		})
	}
}

// TestDecision_CarriesNoRawSubjectAndNoRawAmount. The event plane is a SHARED
// store read by every lens the organisation has; the subject is a string the
// asking gate chose, and gates choose real ones.
//
// It walks the WHOLE rendered occurrence rather than the fields it expects to be
// wrong, so a later field that carried the identifier through fails here too.
//
// Mutation proof: state in.Subject instead of the digest, or the amount instead
// of its bracket, and this names the value it found.
func TestDecision_CarriesNoRawSubjectAndNoRawAmount(t *testing.T) {
	tn := key(t, brandA, orgA)
	const email = "ada@example.com"
	const nano = int64(75_000) * nanoPerUSD

	in := &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindPerson, Subject: email,
		Signals: []contract.Signal{{Name: contract.SignalNano, Value: strconv.FormatInt(nano, 10)}},
	}
	d := decided{
		A:       anomaly.Assessment{Scored: true, Score: 0.91, Cut: 0.5, Alert: true},
		Version: 4, Shape: "halfspace:abc",
	}
	ev := decision(tn, in, nano, d, answer(d))

	said := []string{ev.Name, ev.Product, ev.Subject}
	for _, a := range ev.Attributes {
		said = append(said, a.Name, a.Value)
	}
	whole := strings.Join(said, "\x00")
	for _, banned := range []string{email, "ada", "example.com", strconv.FormatInt(nano, 10), "75000"} {
		if strings.Contains(whole, banned) {
			t.Errorf("the occurrence carries %q — the shared plane must not become a second copy "+
				"of what the gate stated", banned)
		}
	}
	// It is still GROUPABLE: the digest is stable, and it is the subject's only form.
	if ev.Subject == "" {
		t.Fatal("the occurrence names no subject at all — decisions about one subject can no longer be grouped")
	}
	if ev.Subject != digest(tn, contract.KindPerson, email) {
		t.Errorf("subject %q is not the digest of what was judged", ev.Subject)
	}
	// And the value is the BRACKET, which is what an operator asks of it.
	if got, _ := attr(ev, attrValue); got != bracketReview {
		t.Errorf("value = %q, want %q — the bracket is the stated bound the amount was past", got, bracketReview)
	}
}

// TestDigest_IsScopedToTheTenantAndTheKind. A grouping key that took the same
// value under two organisations would let one tenant's rows be correlated with
// another's by anybody who could read both.
func TestDigest_IsScopedToTheTenantAndTheKind(t *testing.T) {
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)
	const subject = "u_412"

	switch {
	case digest(a, contract.KindAccount, subject) == digest(b, contract.KindAccount, subject):
		t.Error("one subject digests the same under two organisations — the plane's rows are cross-tenant correlatable")
	case digest(a, contract.KindAccount, subject) == digest(a, contract.KindPerson, subject):
		t.Error("one identifier digests the same under two kinds — a person and an account sharing an id became one subject")
	case digest(a, contract.KindAccount, subject) != digest(a, contract.KindAccount, " "+subject+" "):
		t.Error("the digest is not stable across the padding the wire may carry — one subject would group as two")
	case digest(a, contract.KindAccount, "") != "":
		t.Error("an unstated subject produced a digest — a hash of nothing is a subject that does not exist")
	}
	// Two brands' identically named organisations are two tenants, and this is
	// where that holds for the plane's own key too.
	if digest(a, contract.KindAccount, subject) == digest(key(t, brandB, orgA), contract.KindAccount, subject) {
		t.Error("the same org under two brands digests one subject — the brand half is not in the key")
	}
}

// TestDecision_SaysWhetherTheVerdictWasLiveOrShadow, and the refusal rule beside
// it, because both are about a reader not mistaking one thing for another.
//
// Mutation proof: drop the posture attribute and a shadow deployment reads as an
// armed one; copy the score onto a refusal and "the model has no opinion" reads
// as "the model says this is fine".
func TestDecision_SaysWhetherTheVerdictWasLiveOrShadow(t *testing.T) {
	tn := key(t, brandA, orgA)
	in := &contract.RiskDecideIn{Stage: cloud.StageSignup, Kind: contract.KindAccount, Subject: "u_1"}

	for _, tc := range []struct {
		name    string
		d       decided
		posture string
		scored  bool
	}{
		{
			name:    "live, above the cut",
			d:       decided{A: anomaly.Assessment{Scored: true, Score: 0.9, Cut: 0.5, Alert: true}, Version: 3},
			posture: postureLive, scored: true,
		},
		{
			name:    "shadow, above the cut — nothing changed and the fact is stated",
			d:       decided{A: anomaly.Assessment{Scored: true, Score: 0.9, Cut: 0.5, Shadow: true}, Version: 3},
			posture: postureShadow, scored: true,
		},
		{
			name:    "a warming model, in shadow",
			d:       decided{A: anomaly.Assessment{Reason: anomaly.ReasonWarming, Score: 0.93, Cut: 0.5, Shadow: true}},
			posture: postureShadow, scored: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := decision(tn, in, 0, tc.d, answer(tc.d))
			if got, _ := attr(ev, attrPosture); got != tc.posture {
				t.Errorf("posture = %q, want %q", got, tc.posture)
			}
			_, hasScore := attr(ev, attrScore)
			_, hasCut := attr(ev, attrCut)
			if hasScore != tc.scored || hasCut != tc.scored {
				t.Errorf("{score:%v cut:%v} present, want both %v — a refusal's number is arithmetic, "+
					"not an opinion, and a cut beside an absent score reads as a comparison never made",
					hasScore, hasCut, tc.scored)
			}
			if tc.scored {
				if got, _ := attr(ev, attrScore); got != "0.9" {
					t.Errorf("score = %q, want the verdict's own number", got)
				}
				if got, _ := attr(ev, attrCut); got != "0.5" {
					t.Errorf("cut = %q, want the threshold it was held against", got)
				}
			}
		})
	}
}

// TestPlaneDecide_AShadowDecisionIsStatedAndTagged closes the shadow case end to
// end, on the DEFAULT posture — no organisation is armed by this test and none
// needs to be, which is the point: a shadow regime's whole value is being able to
// read what it would have done.
func TestPlaneDecide_AShadowDecisionIsStatedAndTagged(t *testing.T) {
	probe.reset(true)
	mountApp(t)
	ch := intercept(t)

	if _, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StageSignup, Kind: contract.KindAccount, Subject: "u_9",
	}); err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	f := await(t, ch)
	if posture, _ := attr(f.in, attrPosture); posture != postureShadow {
		t.Errorf("posture = %q, want %q — an unarmed organisation decides in shadow, and a row "+
			"that does not say so reads as an armed decision", posture, postureShadow)
	}
}

// TestBracket_IsTheStatedBoundAndNeverTheAmount, over the policy's own numbers.
func TestBracket_IsTheStatedBoundAndNeverTheAmount(t *testing.T) {
	for _, tc := range []struct {
		nano int64
		want string
	}{
		{0, bracketNone},
		{-1, bracketNone},
		{1, bracketUnder},
		{freezeNano - 1, bracketUnder},
		{freezeNano, bracketFreeze},
		{reviewNano - 1, bracketFreeze},
		{reviewNano, bracketReview},
		{reviewNano * 1000, bracketReview},
	} {
		if got := bracket(tc.nano); got != tc.want {
			t.Errorf("bracket(%d) = %q, want %q", tc.nano, got, tc.want)
		}
	}
}

// TestAlpha2_NarrowsWhatACallerStates. The country is the one attribute here a
// caller supplies, so it is the one that could put a chosen string into a
// LowCardinality dictionary in a shared table.
func TestAlpha2_NarrowsWhatACallerStates(t *testing.T) {
	for in, want := range map[string]string{
		"gb": "GB", " US ": "US", "GB": "GB",
		"": "", "G": "", "GBR": "", "G1": "", "12": "", "ГБ": "",
		strings.Repeat("A", 4096): "",
	} {
		if got := alpha2(in); got != want {
			t.Errorf("alpha2(%q) = %q, want %q", in, got, want)
		}
	}
}
