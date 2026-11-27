package email

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/audit"
)

// The findings of Red's review of the ledger, each held as a test: the PoCs it
// sent (scratchpad red-email-poc_test.go.txt), adopted as assertions.

// R1: a SendGrid bounce's diagnostic names the address; neither the stream nor the
// event record carries it.
func TestRed_ADiagnosticNeverCarriesTheAddress(t *testing.T) {
	raw := json.RawMessage(`{"email":"jane@acme.com","event":"bounce","type":"bounce","sg_event_id":"e1","sg_message_id":"p1.x","timestamp":1700000000,"reason":"550 5.1.1 <jane@acme.com>: Recipient address rejected: User unknown","campaign":"fall jane@acme.com"}`)
	e, err := sendGridEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range statement(e, nil).Attributes {
		if strings.Contains(a.Value, "@") {
			t.Errorf("the stream carries an address in %s: %q", a.Name, a.Value)
		}
	}
	if got := scrub(e.Reason); got != "550 5.1.1 <<address>>: Recipient address rejected: User unknown" {
		t.Errorf("scrubbed %q", got)
	}
	if !e.Suppress {
		t.Error("a hard bounce no longer suppresses")
	}
}

// R2: only a drop about the address suppresses it.
func TestRed_OnlyAnAddressDropSuppresses(t *testing.T) {
	for reason, want := range map[string]bool{
		"Bounced Address": true, "Unsubscribed Address": true, "Spam Reporting Address": true, "Invalid": true,
		"Recipient List over Package Quota": false, "Spam Content (if spam checker app enabled)": false, "Invalid SMTPAPI header": false,
	} {
		e, err := sendGridEvent(json.RawMessage(fmt.Sprintf(`{"email":"bob@acme.com","event":"dropped","reason":%q,"sg_event_id":"e2","sg_message_id":"p2.x","timestamp":1700000000}`, reason)))
		if err != nil {
			t.Fatal(err)
		}
		if e.Suppress != want || (want && e.Kind != KindBounced) || (!want && e.Kind != KindRejected) {
			t.Errorf("dropped for %q: kind %s suppress %v, want suppress %v", reason, e.Kind, e.Suppress, want)
		}
	}
}

// R3: a custom arg of another JSON type reads as text, and one event that does not
// read is skipped while the rest of the signed delivery is filed, 200.
func TestRed_AnOddEventIsSkippedNotTheDelivery(t *testing.T) {
	e, err := sendGridEvent(json.RawMessage(`{"email":"a@b.c","event":"open","sg_event_id":"e3","campaign":2024,"message":{"x":1}}`))
	if err != nil || e.Campaign != "2024" || e.Message != "" {
		t.Fatalf("a numeric campaign: %+v %v", e, err)
	}
	w := newWorld(t)
	body := fmt.Sprintf(`[{"event":"open","sg_event_id":"o1","timestamp":%d,"email":"bounced@acme.io","sg_message_id":"sg-1.f","message":"%s"},`+
		`{"event":"open","sg_event_id":"o2","timestamp":"yesterday","email":"bounced@acme.io"}]`, at(1), worldMsg)
	req := httptest.NewRequest(http.MethodPost, "/v1/email/webhooks/sendgrid?org=acme", strings.NewReader(body))
	for k, v := range w.delivery(t, body, w.ec, signedAt) {
		req.Header.Set(k, v)
	}
	resp, err := w.app.Test(req, deadline)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != `{"recorded":1,"skipped":1}` {
		t.Fatalf("answered %d %s, want the good event filed and the odd one skipped", resp.StatusCode, got)
	}
	if st := w.status(t); st != StatusOpened {
		t.Errorf("the good event did not move the message: %q", st)
	}
}

// R4: SES names a configuration set only when the credential names one; notify's
// SES rail builds through NewSES and is unchanged by the ledger.
func TestRed_SESNamesNoConfigurationSetUnasked(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		_, _ = w.Write([]byte(`{"MessageId":"m"}`))
	}))
	defer srv.Close()
	creds := map[string]string{"access-key-id": "AK", "secret-access-key": "SK", "from-email": "a@b.c", "endpoint": srv.URL}
	if _, err := NewSES(creds, []string{"x@y.z"}).SendWithID(context.Background(), "s", "b"); err != nil || strings.Contains(got, "ConfigurationSetName") {
		t.Errorf("an SES send named a configuration set nobody configured: %s %v", got, err)
	}
	creds["configuration-set"] = "mine"
	if _, err := NewSES(creds, []string{"x@y.z"}).SendWithID(context.Background(), "s", "b"); err != nil || !strings.Contains(got, `"ConfigurationSetName":"mine"`) {
		t.Errorf("the credential's set was not named: %s %v", got, err)
	}
}

// R5: a clicked link keeps no address in its path or its fragment.
func TestRed_ALinkKeepsNoAddress(t *testing.T) {
	for in, want := range map[string]string{
		"https://app.x.io/invite/jane@acme.com?utm_source=email": "https://app.x.io/invite/-?utm_source=email",
		"https://app.x.io/#/verify?email=jane@acme.com":          "https://app.x.io/",
		"https://app.x.io/u/jane%40acme.com/settings":            "https://app.x.io/u/-/settings",
	} {
		if got := trueLink(in); got != want {
			t.Errorf("trueLink(%q) = %q, want %q", in, got, want)
		}
	}
}

// H1: a hard bounce reaches the suppression list before anything that can fail:
// the stream refusing the delivery still leaves the address suppressed.
func TestRed_SuppressionComesFirst(t *testing.T) {
	w := newWorld(t)
	w.plane.mu.Lock()
	w.plane.streamOut = true
	w.plane.mu.Unlock()
	if got := w.post(t, "/v1/email/webhooks/ses?org=acme", js(w.notification("2", nil)), nil); got != http.StatusServiceUnavailable {
		t.Fatalf("answered %d, want 503 for the stream", got)
	}
	w.plane.mu.Lock()
	defer w.plane.mu.Unlock()
	if len(w.plane.suppressed) == 0 || w.plane.suppressed[0].Address != "bounced@acme.io" {
		t.Errorf("the bounce did not reach the list before the stream failed: %v", w.plane.suppressed)
	}
}

// M2: the filing lock is held for the messages and the counts alone, and a
// delivery whose context ends stops waiting for it.
func TestRed_ALockGivesUpWithItsContext(t *testing.T) {
	w := newWorld(t)
	release, err := w.svc.hold(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	e := &Event{ID: "ses:held", Provider: "ses", Kind: KindOpened, PID: "ses-1", Message: worldMsg, At: signedAt, To: []string{"bounced@acme.io"}}
	if _, err := w.svc.Record(ctx, "acme", []*Event{e}); err == nil {
		t.Fatal("a delivery waited past its context for the lock")
	}
}

// M2: the CRM is told each event with the event's own id, which it keeps once.
func TestRed_TheCRMIsToldTheEvent(t *testing.T) {
	w := newWorld(t)
	var ids []string
	was := eventFn
	eventFn = func(_ context.Context, _, _, event, _ string, _ time.Time) (bool, error) {
		ids = append(ids, event)
		return true, nil
	}
	t.Cleanup(func() { eventFn = was })
	if got := w.ses(t, "n-told", sesOpen(worldMsg)); got != http.StatusOK || len(ids) != 1 || ids[0] != "ses:n-told" {
		t.Fatalf("answered %d, told %v", got, ids)
	}
}

// LOW: an event that names no message is counted once, however often it is filed.
func TestRed_AnEventNamingNoMessageCountsOnce(t *testing.T) {
	w := newWorld(t)
	ev := fmt.Sprintf(`{"event":"delivered","sg_event_id":"loose","timestamp":%d,"email":"x@else.io"}`, at(1))
	w.plane.mu.Lock()
	w.plane.streamOut = true
	w.plane.mu.Unlock()
	if got := w.grid(t, ev); got != http.StatusServiceUnavailable {
		t.Fatalf("answered %d, want 503", got)
	}
	w.plane.mu.Lock()
	w.plane.streamOut = false
	w.plane.mu.Unlock()
	for range 2 {
		if got := w.grid(t, ev); got != http.StatusOK {
			t.Fatalf("answered %d", got)
		}
	}
	if s := w.tally(t); s.Totals.Delivered != 1 {
		t.Errorf("a loose delivery counted %d times", s.Totals.Delivered)
	}
}

// LOW: a provider's refusal is not shown to members in its own words, which name
// the account; and the region credential cannot point the SES host off Amazon.
func TestRed_ProviderWordsStayInTheLog(t *testing.T) {
	l := newLedger(t)
	aws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"User: arn:aws:iam::123456789012:user/ses-sender is not authorized"}`, http.StatusForbidden)
	}))
	defer aws.Close()
	l.creds.Set("acme", "ses", "endpoint", aws.URL)
	st, err := l.svc.GetSESStatus(context.Background(), "acme", "")
	if err != nil || st.QuotaError == "" || strings.Contains(st.QuotaError, "arn:") || strings.Contains(st.QuotaError, "123456789012") {
		t.Errorf("quota error %q %v", st.QuotaError, err)
	}
	for region, want := range map[string]string{"eu-west-1": "eu-west-1", "us-gov-west-1": "us-gov-west-1", "evil.example.com#": "us-east-1", "x/../": "us-east-1"} {
		if got := NewSES(map[string]string{"region": region}, nil).region; got != want {
			t.Errorf("region %q read as %q, want %q", region, got, want)
		}
	}
}

// LOW: a send cut off between its queued record and its provider is sent by a later
// retry of its key, once; one still running is not sent twice.
func TestRed_AStalledSendIsTakenUp(t *testing.T) {
	l := newLedger(t)
	ctx := context.Background()
	if err := ensure(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	id := MessageID("acme", "key:ada@acme.io")
	if _, err := upsert(ctx, "acme", "email", messages, []map[string]any{{
		"id": rid("message", id), "message": id, "origin": originAPI, "provider": "ses", "status": string(StatusQueued),
		"to": []string{"ada@acme.io"}, "tags": []string{}, "idempotency": "key:ada@acme.io", "at": stamp(time.Now().Add(-time.Hour)),
	}}); err != nil {
		t.Fatal(err)
	}
	if r := l.send(t, id, "ada@acme.io", "ses", "", "key:ada@acme.io"); r.Status != StatusSent || l.sends.Load() != 1 {
		t.Fatalf("a stalled send: %+v after %d sends", r, l.sends.Load())
	}
	l.send(t, id, "ada@acme.io", "ses", "", "key:ada@acme.io")
	if l.sends.Load() != 1 {
		t.Errorf("a sent message was sent again: %d sends", l.sends.Load())
	}
}

// LOW: the org's default provider is changed on the audit trail first, and not at
// all without one.
func TestRed_TheDefaultProviderIsAudited(t *testing.T) {
	r := routed(t)
	trail = nil
	if code, _ := r.do(t, http.MethodPut, "/v1/email/provider", "acme", "u_admin", true, `{"provider":"sendgrid"}`); code != http.StatusServiceUnavailable {
		t.Errorf("no audit trail: %d, want 503", code)
	}
	tr, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	trail = tr
	t.Cleanup(func() { trail = nil })
	if code, raw := r.do(t, http.MethodPut, "/v1/email/provider", "acme", "u_admin", true, `{"provider":"sendgrid"}`); code != http.StatusOK {
		t.Fatalf("an admin set the default: %d %s", code, raw)
	}
	rows, _, err := tr.Query(context.Background(), audit.Filter{})
	if err != nil || len(rows) != 1 || rows[0].Action != "email.provider.set" || rows[0].Resource.ID != "acme" {
		t.Errorf("the trail holds %+v %v", rows, err)
	}
}

// M: an org with no commercial rail is told what to set.
func TestRed_ANoRailSendSaysWhatToSet(t *testing.T) {
	r := routed(t)
	code, raw := r.do(t, http.MethodPost, "/v1/email", "tw", "u_1", false, `{"to":["ada@acme.io"],"text":"Hi"}`)
	if code != http.StatusBadRequest || !strings.Contains(string(raw), "PUT /v1/notify/credentials/ses/") {
		t.Errorf("answered %d %s, want 400 naming the credentials to set", code, raw)
	}
}
