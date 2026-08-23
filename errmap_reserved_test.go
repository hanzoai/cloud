package cloud

// errmap_reserved_test.go measures which keys a REFUSAL's envelope claims, so no
// comment has to assert it.
//
// This exists because the fleet has now got it wrong twice in opposite directions,
// both times by inheriting prose instead of marshalling one:
//
//   - Several ledgers said the envelope is {status, code, error}. It was, at an
//     older zip. It is RFC 9457 problem-details now, so the SENTENCE is `detail`,
//     and two routes converted this week moved their sentence key without saying so.
//   - The correction then said a body carrying its own `error` key "cannot ride
//     Detail at all" — inherited from the same older reading. `error` is FREE: the
//     envelope has not written it since the move to problem-details.
//
// A reserved-key set is a fact about a DEPENDENCY, which is exactly the kind of
// claim that expires. So it is asserted here against the pinned zip, and the day
// zip changes its envelope this goes red instead of a dozen comments going quietly
// wrong.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
)

// reserved is what the envelope writes over its members. A domain key with one of
// these names is silently displaced; any other name survives.
var reserved = []string{"type", "title", "status", "detail", "code"}

func TestTheRefusalEnvelopeClaimsExactlyTheseKeys(t *testing.T) {
	// Every reserved name is offered as a MEMBER with a value the envelope cannot
	// produce, so a survivor is proof the envelope did not write that key.
	members := map[string]any{}
	for _, k := range reserved {
		members[k] = "MEMBER-SURVIVED"
	}
	raw, err := json.Marshal((&zip.HTTPError{
		Status: http.StatusConflict, Code: "envelope_code", Msg: "the envelope's sentence",
	}).With(members))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range reserved {
		if got[k] == "MEMBER-SURVIVED" {
			t.Errorf("%q is NOT reserved after all — the member survived, so a refusal may carry it "+
				"and the comments that call it reserved are over-strict: %s", k, raw)
		}
	}
	// And the envelope really did write all five, rather than the member merely
	// vanishing: an assertion that passes because nothing rendered is no assertion.
	if got["detail"] != "the envelope's sentence" || got["code"] != "envelope_code" ||
		got["title"] != "Conflict" || got["type"] != "about:blank" {
		t.Errorf("the envelope is incomplete, so the check above proved nothing: %s", raw)
	}
	if n, _ := got["status"].(float64); int(n) != http.StatusConflict {
		t.Errorf("status = %v, want 409: %s", got["status"], raw)
	}
}

// `error` is FREE, which is the half the corrections got wrong. The money wire's
// refusal — a NESTED {"error":{code,message}} — rides Detail intact, so what keeps
// cloud.Denied on its own middleware is not expressibility: DenyEnvelope writes
// that object BARE, and Detail would wrap it in an envelope. That is a wire change
// to the money path, and this test says the choice is a decision rather than a
// limitation.
func TestADomainErrorKeyRidesDetailIntact(t *testing.T) {
	nested := map[string]any{"code": "insufficient_balance", "message": "top up to continue"}
	raw, err := json.Marshal((&zip.HTTPError{
		Status: http.StatusPaymentRequired, Code: "insufficient_balance", Msg: "the ledger says no",
	}).With(map[string]any{"error": nested}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Error  map[string]any `json:"error"`
		Detail string         `json:"detail"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error["code"] != "insufficient_balance" || got.Error["message"] != "top up to continue" {
		t.Fatalf("the nested error did NOT survive, so `error` is reserved after all: %s", raw)
	}
	if got.Detail == "" {
		t.Errorf("the envelope's own sentence is missing, so the member displaced it: %s", raw)
	}
}
