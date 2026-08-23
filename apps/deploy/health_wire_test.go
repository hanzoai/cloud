package deploy

// health_wire_test.go pins the probe's body ACROSS its conversion to a typed op.
//
// The conversion was possible because zip's WithStatus became variadic and an
// answer can state which declared status it is (StatusCoder) — the probe answers
// 200 or 503 over ONE shape, which was exactly the reason its old ledger entry
// gave for staying raw. That entry cited "WithStatus refuses a non-2xx by design",
// true at the zip of the day and false at the pinned one.
//
// What needed care is not the status but the PRESENCE of the two booleans. The map
// this replaced left a key UNSET when the probe never learned that fact: a run that
// could not reach the apiserver never found out whether the CRD is served, and
// reporting `crd: false` there would state something it does not know. A plain bool
// field with omitempty cannot express that — false and absent would render alike —
// so both are pointers, and this drives all three branches to prove it.

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestTheProbeKeepsItsPresenceSemantics(t *testing.T) {
	for _, tc := range []struct {
		name       string
		report     *deployHealth
		wantStatus int
		wantKeys   []string
		absent     []string
	}{{
		name:       "apiserver unreachable: crd is UNKNOWN, so it is absent",
		report:     &deployHealth{Service: "deploy", Status: "degraded", K8s: ptr(false)},
		wantStatus: http.StatusServiceUnavailable,
		wantKeys:   []string{"service", "status", "k8s"},
		absent:     []string{"crd"},
	}, {
		name:       "apiserver up, CRD not served: crd is KNOWN false and must appear",
		report:     &deployHealth{Service: "deploy", Status: "degraded", K8s: ptr(true), CRD: ptr(false)},
		wantStatus: http.StatusServiceUnavailable,
		wantKeys:   []string{"service", "status", "k8s", "crd"},
	}, {
		name:       "healthy",
		report:     &deployHealth{Service: "deploy", Status: "ok", K8s: ptr(true), CRD: ptr(true)},
		wantStatus: http.StatusOK,
		wantKeys:   []string{"service", "status", "k8s", "crd"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.report.StatusCode(); got != tc.wantStatus {
				t.Errorf("StatusCode() = %d, want %d — an orchestrator reading only the code would be "+
					"told the wrong thing", got, tc.wantStatus)
			}
			raw, err := json.Marshal(tc.report)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for _, k := range tc.wantKeys {
				if _, ok := body[k]; !ok {
					t.Errorf("%q is missing: %s", k, raw)
				}
			}
			for _, k := range tc.absent {
				if _, ok := body[k]; ok {
					t.Errorf("%q is PRESENT but was never learned — a false here is a claim the probe "+
						"cannot make: %s", k, raw)
				}
			}
		})
	}
}

// The false a caller must be able to READ: a known-false CRD is not omitempty'd
// away. This is the assertion a plain bool field would fail.
func TestAKnownFalseIsNotOmitted(t *testing.T) {
	raw, _ := json.Marshal(&deployHealth{Service: "deploy", Status: "degraded", K8s: ptr(true), CRD: ptr(false)})
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if v, ok := body["crd"]; !ok || v != false {
		t.Fatalf("crd must be present and false, got %v (ok=%v): %s", v, ok, raw)
	}
}

func ptr(b bool) *bool { return &b }
