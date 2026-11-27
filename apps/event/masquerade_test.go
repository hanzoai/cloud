// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	planeops "github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// send issues one request with the headers the identity boundary would have minted.
// The harness runs no boundary, so these ARE the verified identity downstream sees.
func send(t *testing.T, app *zip.App, method, path string, hdr map[string]string, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", chrome)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestOperatorOfNamesOnlySomeoneActingAsSomeoneElse(t *testing.T) {
	for _, tc := range []struct {
		what string
		who  zip.Caller
		want string
	}{
		{"an impersonation token names its operator", zip.Caller{User: "u-ada", Org: "acme", Owner: "acme", ActedBy: "admin/z"}, "admin/z"},
		{"a SuperAdmin switched into a tenant is the operator", zip.Caller{User: "u-z", Name: "z", Org: "acme", Owner: "admin", Admin: true}, "admin/z"},
		{"a SuperAdmin with no username is named by id", zip.Caller{User: "u-z", Org: "acme", Owner: "admin", Admin: true}, "admin/u-z"},
		{"a SuperAdmin at home acts as themselves", zip.Caller{User: "u-z", Name: "z", Org: "admin", Owner: "admin", Admin: true}, ""},
		{"a member working in their second org acts as themselves", zip.Caller{User: "u-ada", Name: "ada", Org: "globex", Owner: "acme"}, ""},
		{"the person's own session", zip.Caller{User: "u-ada", Name: "ada", Org: "acme", Owner: "acme"}, ""},
		{"no validated principal names nobody, whatever rode along", zip.Caller{Org: "acme", ActedBy: "admin/z", Admin: true}, ""},
	} {
		if got := operatorOf(tc.who); got != tc.want {
			t.Errorf("%s: operatorOf = %q, want %q", tc.what, got, tc.want)
		}
	}
}

// The mark is the server's in both directions: a body cannot write it, and a body
// cannot overwrite it. And it never touches the caller's map.
func TestTheMarkIsTheServers(t *testing.T) {
	forged := map[string]any{attrImpersonated: "true", attrImpersonatedBy: "admin/mallory", attrSubject: "u-ada", "plan": "pro"}
	got := mark(forged, signer{})
	if _, has := got[attrImpersonated]; has {
		t.Errorf("a client wrote the mark itself: %v", got)
	}
	if _, has := got[attrImpersonatedBy]; has {
		t.Errorf("a client named an operator: %v", got)
	}
	if _, has := got[attrSubject]; has {
		t.Errorf("a client vouched for a subject: %v", got)
	}
	if got["plan"] != "pro" {
		t.Errorf("an ordinary property was dropped with the mark: %v", got)
	}
	if forged[attrImpersonatedBy] != "admin/mallory" {
		t.Error("mark wrote into the caller's map")
	}

	got = mark(map[string]any{attrImpersonated: "false", attrImpersonatedBy: "", attrSubject: "u-bob"}, signer{subject: "u-ada", operator: "admin/z"})
	if got[attrImpersonated] != "true" || got[attrImpersonatedBy] != "admin/z" || got[attrSubject] != "u-ada" || !operated(got) {
		t.Errorf("an operator's event lost its mark to the body: %v", got)
	}
	plain := map[string]any{"plan": "pro"}
	if got := mark(plain, signer{}); operated(got) || len(got) != 1 {
		t.Errorf("an ordinary event gained a mark: %v", got)
	}
}

// An operator's events stay in our stream: the sinks that forward to a customer's
// ad platforms never see them, and the customer's own events still flow.
func TestOperatorTrafficNeverReachesASink(t *testing.T) {
	got := make(chan []SinkEvent, 1)
	remove := AddSink(func(org string, evs []SinkEvent) {
		if org == "acme" {
			got <- evs
		}
	})
	defer remove()
	evs := []CaptureEvent{
		{Type: "event", Event: "order_completed", DistinctID: "u-ada", Revenue: 49, signer: signer{subject: "u-ada", operator: "admin/z"}},
		{Type: "event", Event: "order_completed", DistinctID: "u-bob", Revenue: 20, Properties: map[string]any{attrImpersonated: "true"}},
	}
	for i := range evs {
		evs[i].Properties = mark(evs[i].Properties, evs[i].signer)
	}
	fanOut("acme", sourceEvent, evs)
	select {
	case sunk := <-got:
		if len(sunk) != 1 || sunk[0].DistinctID != "u-bob" || operated(sunk[0].Properties) {
			t.Fatalf("sunk %+v, want only the customer's own order, unmarked", sunk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the customer's own order never reached the sink")
	}
}

// End to end through the one endpoint: an event written under an impersonation, or
// by a SuperAdmin switched into the tenant, lands marked; the same body from the
// person themselves lands unmarked, whatever it claims.
func TestIngestMarksAnOperatorsEvents(t *testing.T) {
	body := `{"batch":[{"type":"event","event":"signup","distinctId":"u-ada","properties":{"consent":"analytics","impersonated":"false","impersonated_by":"nobody","$subject":"u-bob"}},` +
		`{"type":"pageview","path":"/pricing","distinctId":"u-ada","properties":{"consent":"analytics"}}]}`
	for _, tc := range []struct {
		what string
		hdr  map[string]string
		want string
	}{
		{"impersonation token", map[string]string{"X-User-Id": "u-ada", "X-Org-Id": "acme", "X-User-Owner": "acme", zip.HeaderActedBy: "admin/z"}, "admin/z"},
		{"SuperAdmin org switch", map[string]string{"X-User-Id": "u-z", "X-User-Name": "z", "X-Org-Id": "acme", "X-User-Owner": "admin", "X-User-IsAdmin": "true"}, "admin/z"},
		{"the person themselves", map[string]string{"X-User-Id": "u-ada", "X-Org-Id": "acme", "X-User-Owner": "acme"}, ""},
	} {
		w := fakeWarehouse(t)
		app := mountApp(t)
		code, got := send(t, app, http.MethodPost, canonEndpoint, tc.hdr, body)
		if code != http.StatusOK {
			t.Fatalf("%s: POST = %d (%s), want 200", tc.what, code, got)
		}
		if len(w.facts) != 2 {
			t.Fatalf("%s: %d facts landed, want 2", tc.what, len(w.facts))
		}
		for _, f := range w.facts {
			if f.org != "acme" {
				t.Errorf("%s: fact filed under %q, want acme", tc.what, f.org)
			}
			if f.attributes[attrSubject] != tc.hdr["X-User-Id"] {
				t.Errorf("%s: $subject %q, want the verified %q", tc.what, f.attributes[attrSubject], tc.hdr["X-User-Id"])
			}
			mark, by := f.attributes[attrImpersonated], f.attributes[attrImpersonatedBy]
			if tc.want == "" {
				if mark != "" || by != "" {
					t.Errorf("%s: unmarked traffic carries a mark %q/%q", tc.what, mark, by)
				}
				continue
			}
			if mark != "true" || by != tc.want {
				t.Errorf("%s: %s marked %q by %q, want true by %q", tc.what, f.name, mark, by, tc.want)
			}
		}
	}
}

// A peer stating an occurrence for an impersonated request marks it the same way,
// from the identity the plane forwarded.
func TestPlaneCaptureMarksAnOperatorsEvents(t *testing.T) {
	w := fakeWarehouse(t)
	ctx := zip.WithCaller(context.Background(), zip.Caller{User: "u-ada", Org: "acme", Owner: "acme", ActedBy: "admin/z"})
	if _, err := planeCapture(ctx, &planeops.EventIn{Name: "order_completed", Subject: "u-ada",
		Attributes: []planeops.Signal{{Name: attrImpersonated, Value: "false"}}}); err != nil {
		t.Fatalf("planeCapture: %v", err)
	}
	if len(w.facts) != 1 || w.facts[0].attributes[attrImpersonated] != "true" || w.facts[0].attributes[attrImpersonatedBy] != "admin/z" {
		t.Fatalf("plane fact = %+v, want marked by admin/z", w.facts)
	}
}

func TestTrafficOf(t *testing.T) {
	for in, want := range map[string]traffic{"": customers, "exclude": customers, "INCLUDE": everyone, " only ": operators} {
		got, err := trafficOf(in)
		if err != nil || got != want {
			t.Errorf("trafficOf(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := trafficOf("all"); err == nil {
		t.Error("an unknown value must be refused, not read as a default")
	}
}

// Every product-event lens counts the tenant's own traffic unless asked: the
// clause rides eventsWhere, binds nothing, and is a server constant.
func TestLensesCountTheTenantsOwnTrafficByDefault(t *testing.T) {
	start, end := time.Unix(0, 0), time.Unix(1000, 0)
	for _, tc := range []struct {
		t    traffic
		want string
	}{
		{customers, "attributes['impersonated'] != 'true'"},
		{operators, "attributes['impersonated'] = 'true'"},
		{everyone, ""},
	} {
		where, args := eventsWhere("acme", start, end, tc.t)
		if len(args) != 4 {
			t.Errorf("%s: the mark must bind nothing, got %d args", tc.t, len(args))
		}
		if tc.want == "" {
			if strings.Contains(where, "impersonated") {
				t.Errorf("include must not filter: %q", where)
			}
			continue
		}
		if !strings.HasSuffix(where, " AND "+tc.want) {
			t.Errorf("%s: where = %q, want suffix %q", tc.t, where, tc.want)
		}
	}
	// The zero value is the customer's view, so a lens that names nothing excludes.
	if traffic("").clause() != customers.clause() {
		t.Error("the zero traffic must be the default exclusion")
	}
	// The customer-metric reads that take no parameter exclude.
	sql, _ := outcomesSQL("acme", "", "signup", start, end)
	if !strings.Contains(sql, customers.clause()) {
		t.Errorf("outcomes counts operator traffic: %q", sql)
	}
	where, _ := campaignWhere("acme", "cmp", "", start, end)
	if !strings.Contains(where, customers.clause()) {
		t.Errorf("campaign counts operator traffic: %q", where)
	}
}

// An unknown impersonated value is the caller's mistake, refused before any store is
// asked.
func TestLensesRefuseAnUnknownImpersonated(t *testing.T) {
	app := mountApp(t)
	for _, p := range []string{"/v1/event/overview", "/v1/event/top", "/v1/event/insights/events"} {
		code, body := do(t, app, http.MethodGet, p+"?impersonated=everything", "u", "acme")
		if code != http.StatusBadRequest {
			t.Errorf("GET %s?impersonated=everything = %d (%s), want 400", p, code, body)
		}
		if code, body := do(t, app, http.MethodGet, p+"?impersonated=only", "u", "acme"); code != http.StatusServiceUnavailable {
			t.Errorf("GET %s?impersonated=only = %d (%s), want 503 (admitted, no warehouse)", p, code, body)
		}
	}
}

// TestTheSubjectRidesTheStream: the stream is first party, so an event names the
// signed-in person behind it whatever the visitor chose for the third parties, and
// the operator mark always stays.
func TestTheSubjectRidesTheStream(t *testing.T) {
	who := signer{subject: "u-1", operator: "admin/z"}
	for _, consent := range []string{"", "analytics"} {
		p := mark(map[string]any{"consent": consent}, who)
		if p[attrSubject] != "u-1" || p[attrImpersonatedBy] != "admin/z" {
			t.Fatalf("consent %q marked %v", consent, p)
		}
	}
}
