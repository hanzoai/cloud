package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountApp mounts ONLY the registry surface (no bus URL ⇒ the dispatcher is inert), so
// the CRUD tests never touch NATS.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(nil) })
	return app
}

// do issues a request as a VALIDATED principal for org (X-User-Id present ⇒
// principal.Validated), or anonymously when org == "".
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // validated principal
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestRegistryCRUDAndSecret proves the create→read lifecycle and the secret contract:
// the signing secret is returned ONLY on create, never on list/get.
func TestRegistryCRUDAndSecret(t *testing.T) {
	app := mountApp(t)

	// create
	code, body := do(t, app, http.MethodPost, "/v1/webhooks", "acme", map[string]any{
		"url":         "https://hooks.acme.test/in",
		"events":      []string{"commerce.order.*", "commerce.>"},
		"description": "order feed",
	})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var created Endpoint
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create json: %v (%s)", err, body)
	}
	if created.ID == "" || created.Secret == "" {
		t.Fatalf("create must return id + secret, got %+v", created)
	}
	if created.Status != "active" || created.Org != "acme" || len(created.Events) != 2 {
		t.Fatalf("create round-trip mismatch: %+v", created)
	}

	// get: secret MUST be redacted
	code, body = do(t, app, http.MethodGet, "/v1/webhooks/"+created.ID, "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get want 200, got %d (%s)", code, body)
	}
	var got Endpoint
	_ = json.Unmarshal(body, &got)
	if got.Secret != "" {
		t.Fatalf("get must NOT return the secret, got %q", got.Secret)
	}
	if got.ID != created.ID {
		t.Fatalf("get id mismatch: %s vs %s", got.ID, created.ID)
	}

	// list: one row, secret redacted
	code, body = do(t, app, http.MethodGet, "/v1/webhooks", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	var listResp struct {
		Data []Endpoint `json:"data"`
	}
	_ = json.Unmarshal(body, &listResp)
	if len(listResp.Data) != 1 || listResp.Data[0].Secret != "" {
		t.Fatalf("list must return 1 endpoint with redacted secret, got %+v", listResp.Data)
	}

	// update: disable + change url; secret unchanged and still redacted
	code, body = do(t, app, http.MethodPut, "/v1/webhooks/"+created.ID, "acme", map[string]any{
		"url":    "https://hooks.acme.test/v2",
		"events": []string{"commerce.order.created"},
		"status": "disabled",
	})
	if code != http.StatusOK {
		t.Fatalf("update want 200, got %d (%s)", code, body)
	}
	var updated Endpoint
	_ = json.Unmarshal(body, &updated)
	if updated.Status != "disabled" || updated.URL != "https://hooks.acme.test/v2" || updated.Secret != "" {
		t.Fatalf("update mismatch: %+v", updated)
	}

	// delete
	code, _ = do(t, app, http.MethodDelete, "/v1/webhooks/"+created.ID, "acme", nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d", code)
	}
	code, _ = do(t, app, http.MethodGet, "/v1/webhooks/"+created.ID, "acme", nil)
	if code != http.StatusNotFound {
		t.Fatalf("get after delete want 404, got %d", code)
	}
}

// TestRegistryAuth proves the unauthenticated gate (401, like clients/notify) and the
// https-required + status validation rules.
func TestRegistryAuth(t *testing.T) {
	app := mountApp(t)

	// no principal → 401 on every route
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/webhooks"},
		{http.MethodPost, "/v1/webhooks"},
		{http.MethodGet, "/v1/webhooks/wh_x"},
		{http.MethodPut, "/v1/webhooks/wh_x"},
		{http.MethodDelete, "/v1/webhooks/wh_x"},
	} {
		if code, _ := do(t, app, tc.method, tc.path, "", map[string]any{"url": "https://x.test/h"}); code != http.StatusUnauthorized {
			t.Fatalf("anon %s %s want 401, got %d", tc.method, tc.path, code)
		}
	}

	// http:// (cleartext) rejected
	if code, _ := do(t, app, http.MethodPost, "/v1/webhooks", "acme", map[string]any{"url": "http://x.test/h"}); code != http.StatusBadRequest {
		t.Fatalf("http:// url want 400, got %d", code)
	}
	// missing url rejected
	if code, _ := do(t, app, http.MethodPost, "/v1/webhooks", "acme", map[string]any{"events": []string{"a.b"}}); code != http.StatusBadRequest {
		t.Fatalf("missing url want 400, got %d", code)
	}
	// bad status rejected
	if code, _ := do(t, app, http.MethodPost, "/v1/webhooks", "acme", map[string]any{"url": "https://x.test/h", "status": "paused"}); code != http.StatusBadRequest {
		t.Fatalf("bad status want 400, got %d", code)
	}
}

// TestRegistryOrgIsolation proves org A can never read, mutate, or delete org B's
// endpoints — the physical per-org store makes B's rows unreachable from A.
func TestRegistryOrgIsolation(t *testing.T) {
	app := mountApp(t)

	// B creates an endpoint.
	code, body := do(t, app, http.MethodPost, "/v1/webhooks", "borg", map[string]any{
		"url": "https://hooks.borg.test/in",
	})
	if code != http.StatusCreated {
		t.Fatalf("B create want 201, got %d (%s)", code, body)
	}
	var bEndpoint Endpoint
	_ = json.Unmarshal(body, &bEndpoint)

	// A cannot see B's endpoint in its own list.
	code, body = do(t, app, http.MethodGet, "/v1/webhooks", "acme", nil)
	var aList struct {
		Data []Endpoint `json:"data"`
	}
	_ = json.Unmarshal(body, &aList)
	if code != http.StatusOK || len(aList.Data) != 0 {
		t.Fatalf("A must see 0 endpoints, got %d (%s)", len(aList.Data), body)
	}

	// A cannot GET / PUT / DELETE B's endpoint by id (404, not another org's data).
	if code, _ := do(t, app, http.MethodGet, "/v1/webhooks/"+bEndpoint.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("A GET B's endpoint want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPut, "/v1/webhooks/"+bEndpoint.ID, "acme", map[string]any{"url": "https://evil.test/h"}); code != http.StatusNotFound {
		t.Fatalf("A PUT B's endpoint want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/webhooks/"+bEndpoint.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("A DELETE B's endpoint want 404, got %d", code)
	}

	// B still has its endpoint intact.
	code, body = do(t, app, http.MethodGet, "/v1/webhooks/"+bEndpoint.ID, "borg", nil)
	if code != http.StatusOK {
		t.Fatalf("B GET own endpoint want 200, got %d (%s)", code, body)
	}
}

// createEP creates an endpoint via the HTTP layer and returns it WITH its one-time secret.
func createEP(t *testing.T, app *zip.App, org string, in map[string]any) Endpoint {
	t.Helper()
	code, body := do(t, app, http.MethodPost, "/v1/webhooks", org, in)
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var e Endpoint
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("create json: %v (%s)", err, body)
	}
	return e
}

// getEP fetches one endpoint via the HTTP layer (200 required).
func getEP(t *testing.T, app *zip.App, org, id string) Endpoint {
	t.Helper()
	code, body := do(t, app, http.MethodGet, "/v1/webhooks/"+id, org, nil)
	if code != http.StatusOK {
		t.Fatalf("get %s want 200, got %d (%s)", id, code, body)
	}
	var e Endpoint
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("get json: %v (%s)", err, body)
	}
	return e
}

// getDeliveries GETs /:id/deliveries with an optional query string (200 required).
func getDeliveries(t *testing.T, app *zip.App, org, id, query string) []DeliveryRow {
	t.Helper()
	code, body := do(t, app, http.MethodGet, "/v1/webhooks/"+id+"/deliveries"+query, org, nil)
	if code != http.StatusOK {
		t.Fatalf("deliveries %s%s want 200, got %d (%s)", id, query, code, body)
	}
	var resp struct {
		Data []DeliveryRow `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("deliveries json: %v (%s)", err, body)
	}
	return resp.Data
}

// seedDelivery writes one delivery row directly into the mounted store (the SAME per-org
// store the handlers read), so a test can stage exact counts/statuses — including a
// "retrying" row the single-attempt test-send path can never itself produce.
func seedDelivery(t *testing.T, org, endpointID, status string) {
	t.Helper()
	st, err := mounted.stores.For(org, "")
	if err != nil {
		t.Fatalf("store for %s: %v", org, err)
	}
	if err := st.recordDelivery(context.Background(), DeliveryRow{
		EndpointID: endpointID, DeliveryID: newUUID(), Subject: "commerce.order.created",
		Attempt: 1, Status: status, HTTPStatus: 200, Created: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed delivery: %v", err)
	}
}

// TestDeliveriesListFilterAndLimit proves GET /:id/deliveries is org-scoped, newest-first,
// honours ?status= and ?limit=, and 404s for a missing or another org's id.
func TestDeliveriesListFilterAndLimit(t *testing.T) {
	app := mountApp(t)
	ep := createEP(t, app, "acme", map[string]any{"url": "https://hooks.acme.test/in"})

	for i := 0; i < 3; i++ {
		seedDelivery(t, "acme", ep.ID, "ok")
	}
	for i := 0; i < 2; i++ {
		seedDelivery(t, "acme", ep.ID, "failed")
	}

	// no filter → all 5 (default limit 50).
	if rows := getDeliveries(t, app, "acme", ep.ID, ""); len(rows) != 5 {
		t.Fatalf("unfiltered want 5 rows, got %d", len(rows))
	}
	// ?status=failed → only the 2 failed rows.
	rows := getDeliveries(t, app, "acme", ep.ID, "?status=failed")
	if len(rows) != 2 {
		t.Fatalf("status=failed want 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Status != "failed" {
			t.Fatalf("status filter leaked a %q row", r.Status)
		}
	}
	// ?limit=1 → exactly 1.
	if rows := getDeliveries(t, app, "acme", ep.ID, "?limit=1"); len(rows) != 1 {
		t.Fatalf("limit=1 want 1 row, got %d", len(rows))
	}
	// an over-max limit is clamped, never rejected (we have 5 rows, well under the cap).
	if rows := getDeliveries(t, app, "acme", ep.ID, "?limit=9999"); len(rows) != 5 {
		t.Fatalf("limit=9999 want the 5 rows (clamped, not rejected), got %d", len(rows))
	}

	// org isolation: borg cannot read acme's deliveries — 404, never another org's log.
	if code, _ := do(t, app, http.MethodGet, "/v1/webhooks/"+ep.ID+"/deliveries", "borg", nil); code != http.StatusNotFound {
		t.Fatalf("cross-org deliveries want 404, got %d", code)
	}
	// unknown id → 404.
	if code, _ := do(t, app, http.MethodGet, "/v1/webhooks/wh_missing/deliveries", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("missing id deliveries want 404, got %d", code)
	}
}

// TestTestSendDeliversAndRecords proves POST /:id/test signs + POSTs to the subscriber
// (signature verifies), records a delivery row, and works even for a DISABLED endpoint.
func TestTestSendDeliversAndRecords(t *testing.T) {
	app := mountApp(t)

	received := make(chan capture, 4)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- capture{
			sigHdr: r.Header.Get("X-Webhook-Signature"),
			event:  r.Header.Get("X-Webhook-Event"),
			deliv:  r.Header.Get("X-Webhook-Delivery"),
			body:   body,
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	// The mounted dispatcher's client must trust the test TLS cert (its URL is https, so it
	// passes the registry's https-only rule — no other seam is touched).
	mounted.disp.http = srv.Client()

	// DISABLED on purpose: testing an endpoint you have paused is the whole point.
	ep := createEP(t, app, "acme", map[string]any{"url": srv.URL, "status": "disabled"})
	if ep.Secret == "" {
		t.Fatal("create must reveal the secret")
	}

	code, body := do(t, app, http.MethodPost, "/v1/webhooks/"+ep.ID+"/test", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("test-send want 200, got %d (%s)", code, body)
	}
	var tr testResult
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatalf("test result json: %v (%s)", err, body)
	}
	if !tr.Delivered || tr.HTTPStatus != http.StatusOK {
		t.Fatalf("test-send to a disabled endpoint must still deliver: %+v", tr)
	}

	got := <-received
	if got.event != testSubject {
		t.Fatalf("X-Webhook-Event = %q, want %q", got.event, testSubject)
	}
	ts, v1 := parseSig(t, got.sigHdr)
	if want := signPayload(ep.Secret, ts, got.body); v1 != want {
		t.Fatalf("test-send signature mismatch: v1=%s recomputed=%s", v1, want)
	}

	// A delivery row was recorded (terminal "ok", subject webhook.test).
	rows := getDeliveries(t, app, "acme", ep.ID, "")
	if len(rows) != 1 || rows[0].Status != "ok" || rows[0].Subject != testSubject {
		t.Fatalf("test-send must record one ok row for %s, got %+v", testSubject, rows)
	}
}

// TestRotateSecretInvalidatesOld proves rotate-secret reveals a NEW secret once, and that
// after rotation deliveries sign with the new secret while the old one no longer verifies.
func TestRotateSecretInvalidatesOld(t *testing.T) {
	app := mountApp(t)

	received := make(chan capture, 4)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- capture{sigHdr: r.Header.Get("X-Webhook-Signature"), body: body}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	mounted.disp.http = srv.Client()

	ep := createEP(t, app, "acme", map[string]any{"url": srv.URL})
	oldSecret := ep.Secret

	// Before rotation the old secret verifies.
	if code, _ := do(t, app, http.MethodPost, "/v1/webhooks/"+ep.ID+"/test", "acme", nil); code != http.StatusOK {
		t.Fatalf("pre-rotate test-send want 200, got %d", code)
	}
	pre := <-received
	ts, v1 := parseSig(t, pre.sigHdr)
	if signPayload(oldSecret, ts, pre.body) != v1 {
		t.Fatal("old secret must verify before rotation")
	}

	// Rotate: a NEW secret is revealed once.
	code, body := do(t, app, http.MethodPost, "/v1/webhooks/"+ep.ID+"/rotate-secret", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("rotate want 200, got %d (%s)", code, body)
	}
	var rotated Endpoint
	if err := json.Unmarshal(body, &rotated); err != nil {
		t.Fatalf("rotate json: %v (%s)", err, body)
	}
	if rotated.Secret == "" || rotated.Secret == oldSecret {
		t.Fatalf("rotate must reveal a new, different secret (old=%q new=%q)", oldSecret, rotated.Secret)
	}
	// A subsequent get must NOT leak the new secret (reveal-once, like create).
	if getEP(t, app, "acme", ep.ID).Secret != "" {
		t.Fatal("get must not return the rotated secret")
	}

	// After rotation the NEW secret verifies and the OLD one does not.
	if code, _ := do(t, app, http.MethodPost, "/v1/webhooks/"+ep.ID+"/test", "acme", nil); code != http.StatusOK {
		t.Fatalf("post-rotate test-send want 200, got %d", code)
	}
	post := <-received
	ts, v1 = parseSig(t, post.sigHdr)
	if signPayload(rotated.Secret, ts, post.body) != v1 {
		t.Fatal("new secret must verify after rotation")
	}
	if signPayload(oldSecret, ts, post.body) == v1 {
		t.Fatal("old secret must NOT verify after rotation")
	}
}

// TestUsageCounters proves list/get carry deliveries7d + failures7d computed from the
// delivery log: 0s when empty, terminal ok/failed counted, "retrying" excluded.
func TestUsageCounters(t *testing.T) {
	app := mountApp(t)
	ep := createEP(t, app, "acme", map[string]any{"url": "https://hooks.acme.test/in"})

	// Fresh endpoint: 0/0 on both get and create.
	if e := getEP(t, app, "acme", ep.ID); e.Deliveries7d != 0 || e.Failures7d != 0 {
		t.Fatalf("fresh endpoint must be 0/0, got %d/%d", e.Deliveries7d, e.Failures7d)
	}

	// 3 ok + 1 failed = 4 completed, 1 failure; the "retrying" row must NOT be counted.
	for i := 0; i < 3; i++ {
		seedDelivery(t, "acme", ep.ID, "ok")
	}
	seedDelivery(t, "acme", ep.ID, "failed")
	seedDelivery(t, "acme", ep.ID, "retrying")

	e := getEP(t, app, "acme", ep.ID)
	if e.Deliveries7d != 4 || e.Failures7d != 1 {
		t.Fatalf("usage want 4 deliveries / 1 failure, got %d/%d", e.Deliveries7d, e.Failures7d)
	}

	// The list response carries the same counters for this endpoint.
	code, body := do(t, app, http.MethodGet, "/v1/webhooks", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	var listResp struct {
		Data []Endpoint `json:"data"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		t.Fatalf("list json: %v (%s)", err, body)
	}
	if len(listResp.Data) != 1 || listResp.Data[0].Deliveries7d != 4 || listResp.Data[0].Failures7d != 1 {
		t.Fatalf("list usage mismatch: %+v", listResp.Data)
	}
}
