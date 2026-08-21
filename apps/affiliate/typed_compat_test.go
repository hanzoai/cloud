package affiliate

// typed_compat_test.go pins what the conversion to typed ops must not move:
// the contract. The dual-shape reads keep their EXACT key sets in both shapes — an
// enrolled zero still renders and a not-enrolled caller never grows a field —
// and every write that refused a bodyless request with c.Bind's 400 still
// refuses one, because zip's tolerant decode would otherwise turn that refusal
// into a write of zero values.

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"testing"
)

// keysOf returns a body's top-level JSON keys, sorted.
func keysOf(t *testing.T, body []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return slices.Sorted(maps.Keys(m))
}

func wantKeys(t *testing.T, path string, body []byte, want ...string) {
	t.Helper()
	got := keysOf(t, body)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s keys = %v, want %v (%s)", path, got, want, body)
	}
}

// TestShapesStayExact proves both shapes of every dual-shape read carry exactly
// the keys they always carried: nothing omitted because it was zero, nothing
// added because a type now names it.
func TestShapesStayExact(t *testing.T) {
	app, s, _ := mount(t)

	// The not-enrolled shapes.
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"/v1/affiliate", []string{"defaultRateBps", "isAffiliate"}},
		{"/v1/affiliate/me", []string{"defaultRateBps", "isAffiliate", "schedule"}},
		{"/v1/affiliate/me/earnings", []string{"isAffiliate"}},
		{"/v1/affiliate/me/links", []string{"isAffiliate", "maxLinks"}},
	} {
		st, body := req(t, app, http.MethodGet, tc.path, "orgZ", false, nil)
		if st != http.StatusOK {
			t.Fatalf("%s want 200, got %d (%s)", tc.path, st, body)
		}
		wantKeys(t, tc.path, body, tc.want...)
	}

	// The enrolled shapes, with ZERO money — every money key must still render.
	applyAndApprove(t, app, s, "orgA", "acme", "")
	_, body := req(t, app, http.MethodGet, "/v1/affiliate", "orgA", false, nil)
	wantKeys(t, "/v1/affiliate", body,
		"accruedCents", "code", "handle", "id", "isAffiliate", "link", "marginBps",
		"paidCents", "payouts", "pendingCents", "rateBps", "referredCount",
		"requestedCode", "status")
	_, body = req(t, app, http.MethodGet, "/v1/affiliate/me", "orgA", false, nil)
	wantKeys(t, "/v1/affiliate/me", body,
		"accruedCents", "code", "downlineTotal", "handle", "id", "isAffiliate",
		"levels", "link", "marginBps", "paidCents", "payouts", "pendingCents",
		"rateBps", "status")
	_, body = req(t, app, http.MethodGet, "/v1/affiliate/me/earnings", "orgA", false, nil)
	wantKeys(t, "/v1/affiliate/me/earnings", body,
		"accruedCents", "byPeriod", "byReferredOrg", "isAffiliate", "marginBps",
		"paidCents", "pendingCents")
	_, body = req(t, app, http.MethodGet, "/v1/affiliate/me/links", "orgA", false, nil)
	wantKeys(t, "/v1/affiliate/me/links", body,
		"isAffiliate", "links", "maxLinks", "status")
}

// TestBodylessWritesStillRefuse proves each write that bound a body keeps
// c.Bind's 400 on a bodyless request — and that the refusal really did refuse:
// a bodyless apply enrolls nobody.
func TestBodylessWritesStillRefuse(t *testing.T) {
	app, s, _ := mount(t)
	idA, _ := applyAndApprove(t, app, s, "orgA", "acme", "")

	for _, tc := range []struct {
		method, path, org string
		admin             bool
	}{
		{http.MethodPost, "/v1/affiliate/apply", "orgN", false},
		{http.MethodPost, "/v1/affiliate/attribute", "orgN", false},
		{http.MethodPost, "/v1/affiliate/click", "", false},
		{http.MethodPost, "/v1/affiliate/me/links", "orgA", false},
		{http.MethodPost, "/v1/affiliate/me/handle", "orgA", false},
		{http.MethodPost, "/v1/admin/affiliate/" + idA + "/rate", "admin", true},
		{http.MethodPost, "/v1/admin/affiliate/" + idA + "/payout", "admin", true},
	} {
		if st, body := req(t, app, tc.method, tc.path, tc.org, tc.admin, nil); st != http.StatusBadRequest {
			t.Fatalf("bodyless %s %s want 400, got %d (%s)", tc.method, tc.path, st, body)
		}
	}

	// The refused apply wrote nothing.
	if _, err := s.State.store.GetByOrg(context.Background(), "orgN"); err != errNotFound {
		t.Fatalf("a bodyless apply must enroll nobody, got %v", err)
	}
	// The refused rate post moved nothing: the approved default still stands.
	a, err := s.State.store.GetByID(context.Background(), idA)
	if err != nil || a.RateBps != defaultRateBps {
		t.Fatalf("a bodyless rate post must not set a rate: rate=%d err=%v", a.RateBps, err)
	}
}
