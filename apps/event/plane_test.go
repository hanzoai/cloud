package event

// plane_test.go — the wiring that makes a published fact QUERYABLE: the door publishes
// (capture.go), the stream keeps it (bus.go), the sink lands it (warehouse.go).
//
// Everything here was already written and none of it was connected. normalize had no
// production caller, publish had no production caller, and the drain was never
// constructed — so every browser error reached the legacy wide table and stopped, and
// event.error, the one table built for it, stayed empty. These tests bind the
// CONNECTIONS rather than the pieces — and since the flip they also hold the wide
// table OUT: the fact is the only storage projection, so the ingest path executing
// any warehouse statement at all is the regression.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/commerce/infra"
	"github.com/hanzoai/pubsub-go/jetstream"
	luxlog "github.com/luxfi/log"
)

// factOf returns the single fact this batch published, failing when the count is not
// one. Most of these tests post one event and care what it BECAME.
func factOf(t *testing.T, w *warehouse) fact {
	t.Helper()
	if len(w.facts) != 1 {
		t.Fatalf("published %d facts, want exactly 1: %+v", len(w.facts), w.facts)
	}
	return w.facts[0]
}

// TestBrowserErrorReachesTheErrorPlane is THE regression, written from the production
// failure it is named for: a chunk-load error thrown on hanzo.ai reached hanzo.events
// and stopped there, because nothing on the ingest path ever published a fact. The error
// signal is the one signal the wide table has no shape for — no message, no class, no
// frames, no group — so "stored in hanzo.events" and "queryable as an error" were never
// the same claim, and the second one was false.
//
// It asserts the SIGNAL and the TENANT together. Either alone is passable and useless: a
// fact on the error plane with no org is readable by every tenant, and a fact with an org
// on the product plane is not an error to anyone reading errors.
func TestBrowserErrorReachesTheErrorPlane(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)
	// The batch envelope, because that is the wire that can CARRY a signal: a bare
	// object is the strict four-field canonical Event, which has no type and no error
	// on it at all (decodeIngest, event.go).
	const body = `{"batch":[{"type":"error","error":{"type":"ChunkLoadError",` +
		`"message":"Loading chunk 7192 failed. (error: https://hanzo.ai/_next/static/chunks/7192-dc40722f9dcfd64c.js)"},` +
		`"url":"https://hanzo.ai/pricing"}]}`
	if code, resp := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", body); code != http.StatusOK {
		t.Fatalf("error ingest = %d (%s), want 200", code, resp)
	}

	f := factOf(t, w)
	if f.signal != signalError {
		t.Fatalf("a thrown error published signal %q, want %q — it lands in %s, and that is the "+
			"whole reason that table exists", f.signal, signalError, factTable)
	}
	if f.org != "acme" {
		t.Fatalf("fact org = %q, want acme — an unattributed error row is readable by every tenant", f.org)
	}
	if f.signal != signalError {
		t.Fatalf("signal = %q, want error", f.signal)
	}
	if !strings.Contains(f.message, "Loading chunk 7192 failed") {
		t.Errorf("fault message = %q, want the thrown text", f.message)
	}
	if f.class != "ChunkLoadError" {
		t.Errorf("fault class = %q, want ChunkLoadError", f.class)
	}
	if strings.TrimSpace(f.issue) == "" {
		t.Error("fault carries no issue — issue is what an issue list groups by, so an ungrouped " +
			"error is one an issue list cannot assemble")
	}

	// The fact is the ONLY copy: the wide hanzo.events projection is retired, so the
	// ingest path must touch warehouseExec NOT AT ALL — no DDL, no INSERT. The error
	// lens (/v1/event/errors) reads event.error, where the sink lands exactly this fact.
	if len(w.stmts) != 0 {
		t.Fatalf("ingest executed %d warehouse statements (%v), want 0 — the fact publish "+
			"is the one write path; a statement here is the wide double-write growing back", len(w.stmts), w.stmts)
	}
}

// TestThePublishedFaultIsScrubbed guards the change that made the error fact possible.
// The fold used to ERASE the exception after copying it into the property bag, and the
// fix keeps it — redacted — so faultOf can build the fault from it. Keeping a field that
// was previously nilled is exactly the shape of change that carries free text somewhere
// new, and here "somewhere new" is a durable column and a bus every consumer reads.
//
// The legitimate text must SURVIVE. Without that half the case also passes when the
// fault carries nothing at all, which is the cheapest way to make a redaction assertion
// vacuous.
func TestThePublishedFaultIsScrubbed(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)
	const body = `{"batch":[{"type":"error","error":{"type":"TypeError",` +
		`"message":"login failed for z@hanzo.ai",` +
		`"stack":"at fetch (https://hanzo.ai/api?access_token=abcdef0123456789)"}}]}`
	if code, resp := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", body); code != http.StatusOK {
		t.Fatalf("error ingest = %d (%s), want 200", code, resp)
	}
	f := factOf(t, w)
	published := f.message + " " + strings.Join(frameFiles(f), " ")
	for _, secret := range []string{"z@hanzo.ai", "abcdef0123456789"} {
		if strings.Contains(published, secret) {
			t.Errorf("%q reached the published fault: %q", secret, published)
		}
	}
	if !strings.Contains(f.message, "login failed") {
		t.Errorf("redaction ate the message itself: %q", f.message)
	}
	if f.class != "TypeError" {
		t.Errorf("fault class = %q, want TypeError", f.class)
	}
}

// frameFiles is every file path the fault's frames carry — the other place free text
// reaches a column, since a bundler emits a source URL with a query string on it.
func frameFiles(f fact) []string {
	out := make([]string, 0, len(f.frames))
	for _, fr := range f.frames {
		out = append(out, fr.file)
	}
	return out
}

// TestOneAdmissionOneProjection pins the flip's core claim: one admission decision
// (normalize) and ONE storage projection (the fact). Its predecessor
// (TestOneAdmissionTwoProjections) asserted a wide hanzo.events INSERT beside the
// facts; that second projection is retired, so the pin now holds the write path to
// exactly the facts — a wide insert reappearing is the failure, not the expectation.
func TestOneAdmissionOneProjection(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)
	const body = `{"batch":[{"event":"signup_completed"},{"type":"pageview","url":"https://hanzo.ai/"},` +
		`{"type":"error","error":{"type":"TypeError","message":"x is not a function"}}]}`
	code, resp := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", body)
	if code != http.StatusOK {
		t.Fatalf("batch ingest = %d (%s), want 200", code, resp)
	}
	if !strings.Contains(string(resp), `"accepted":3`) {
		t.Fatalf("receipt = %s, want accepted:3", resp)
	}
	if len(w.facts) != 3 {
		t.Fatalf("published %d facts for 3 admitted events, want 3", len(w.facts))
	}
	if len(w.stmts) != 0 {
		t.Fatalf("ingest executed %d warehouse statements, want 0 — the facts are the only projection", len(w.stmts))
	}
	got := map[signal]int{}
	for _, f := range w.facts {
		got[f.signal]++
		if f.org != "acme" {
			t.Errorf("fact %q carries org %q, want acme", f.id, f.org)
		}
	}
	if got[signalAct] != 2 || got[signalError] != 1 {
		t.Errorf("signals published = %v, want 2 event + 1 error", got)
	}
}

// TestAnUnpublishableBatchWritesNothing pins the commit's honesty: the publish IS the
// commit, so a batch the plane cannot take is a 503 with NO residue anywhere — the
// retry it asks for is safe because every event.* table is a ReplacingMergeTree keyed
// on the fact id, and there is no second, non-idempotent store left for a retry to
// duplicate into (that was hanzo.events, and it is retired from this path).
func TestAnUnpublishableBatchWritesNothing(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)
	publish = func(context.Context, []fact) error {
		return busErr(errors.New("event plane unavailable"))
	}
	code, resp := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", `{"event":"signup_completed"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("ingest with an unavailable plane = %d (%s), want 503 — a fact that was admitted "+
			"and could not be made durable is the caller's business", code, resp)
	}
	if len(w.stmts) != 0 {
		t.Fatalf("executed %d warehouse statements after the publish failed — nothing may land "+
			"beside a commit that answered 503", len(w.stmts))
	}
}

// ── the signal with nowhere to land ──────────────────────────────────────────

// TestLandableIsExactlyTheWriterSet pins that "which signals the door accepts" is DERIVED
// from "which signals the sink can write" and is not a second list. A hand-kept allowlist
// is exactly what lets a door accept a signal the sink silently discards.
func TestLandableIsExactlyTheWriterSet(t *testing.T) {
	if len(landableSignals) != len(writers) {
		t.Fatalf("landableSignals has %d entries for %d writers", len(landableSignals), len(writers))
	}
	for _, w := range writers {
		if !landableSignals[w.signal] {
			t.Errorf("%s has a writer but the door refuses it", w.signal)
		}
	}
	// The one signal deliberately absent, and the reason is a TENANCY defect rather than
	// an unfinished feature: event.metric has no org column at all, and its identity
	// (env, temporality, metric_name, fingerprint) hashes two orgs reporting the same
	// metric name and labels into ONE series. A writer would interleave their samples —
	// a cross-tenant write. See writers (warehouse.go).
	if landableSignals[signalSample] {
		t.Error("the door accepts metrics — event.metric has no org column, so landing one is a " +
			"cross-tenant write and not a feature")
	}
}

// TestMetricIsRefusedNotAccepted is the other half, at the door. Answering 200
// {"accepted":1} to something stored nowhere is a lie whether the discard happens in the
// handler or four hops later on a stream nothing drains; the receipt has to say what
// actually happened.
//
// It used to stay 200, on the argument that the request was well-formed and the batch
// MAY carry other signals that did land. When it does, it still 200s — that is
// TestAMixedBatchDropsOnlyTheMetric below, and the reason the rule is `accepted == 0`
// rather than `dropped > 0`. But when the batch is ONE metric, nothing landed at all,
// and "well-formed" is not the question a caller is asking. `dropped` was the field that
// already meant "this one did not" and no client ever read it, which is how three
// separate ingest outages stayed invisible. So the status carries it: 400, because this
// caller HAS capability and it is the body that has nowhere to go.
func TestMetricIsRefusedNotAccepted(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)
	code, resp := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme",
		`{"batch":[{"type":"metric","metric":{"name":"page_load_ms","value":812}}]}`)
	refused(t, "metric ingest", code, resp, http.StatusBadRequest, "unroutable_events")
	if len(w.facts) != 0 {
		t.Errorf("published %d metric facts onto a subject no writer drains", len(w.facts))
	}
	if len(w.stmts) != 0 {
		t.Errorf("executed %d warehouse statements for a metric — folding a sample into a generic "+
			"product event stores something that is not what was sent", len(w.stmts))
	}
}

// TestAMixedBatchDropsOnlyTheMetric pins that the refusal is per-FACT and not per-batch.
// A 501 for the whole request would take the caller's errors down with its metrics.
func TestAMixedBatchDropsOnlyTheMetric(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)
	code, resp := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme",
		`{"batch":[{"type":"metric","metric":{"name":"page_load_ms","value":812}},{"event":"signup_completed"}]}`)
	if code != http.StatusOK {
		t.Fatalf("mixed batch = %d (%s), want 200", code, resp)
	}
	if !strings.Contains(string(resp), `"accepted":1`) || !strings.Contains(string(resp), `"dropped":1`) {
		t.Fatalf("mixed receipt = %s, want accepted:1 dropped:1", resp)
	}
	if f := factOf(t, w); f.signal != signalAct {
		t.Errorf("surviving fact is %s, want %s", f.signal, signalAct)
	}
}

// ── the plane's retention, and the loss it can no longer hide ────────────────

// TestTheStreamRefusesRatherThanEvicts pins the discard policy, the one setting that
// decides whether a full stream is a LIMIT or a SHREDDER. JetStream's default
// (DiscardOld) drops the oldest messages to make room — and on a hand-off stream the
// oldest are exactly the ones no consumer has reached yet, so it deletes facts the door
// already answered 200 for in order to admit facts it has not answered for, reporting an
// error at neither end.
func TestTheStreamRefusesRatherThanEvicts(t *testing.T) {
	if eventStream.Discard != jetstream.DiscardNew {
		t.Fatal("the event plane discards OLD on a full stream — that is acknowledged data " +
			"deleted to make room for unacknowledged data, with an error reported to nobody")
	}
	if eventStream.MaxBytes != streamBytes || eventStream.MaxAge != streamAge {
		t.Errorf("stream limits drifted from the constants that document them: %d/%s",
			eventStream.MaxBytes, eventStream.MaxAge)
	}
	if eventStream.Retention != jetstream.LimitsPolicy {
		t.Error("retention is not LimitsPolicy — WorkQueue would hand each fact to exactly one " +
			"of the plane's consumers, so five of six would never see it")
	}
	if eventStream.Name != EventStream || len(eventStream.Subjects) != 1 || eventStream.Subjects[0] != plane+".>" {
		t.Errorf("stream identity drifted: %q %v", eventStream.Name, eventStream.Subjects)
	}
}

// TestForeignEnvelopeIsNotCountedAsLoss is what keeps the loss counter WORTH ALARMING ON.
// Two vocabularies share this plane and their subjects overlap — a product event named
// "$error" folds onto event.error, which is also the error signal's subject, and "$error"
// is what a browser error with no name of its own is called. Counting one of those as an
// unlandable fact would hold the counter permanently above zero on the single most common
// event the platform carries, which is the same as having no counter at all.
func TestForeignEnvelopeIsNotCountedAsLoss(t *testing.T) {
	s := &drain{log: luxlog.New("test")}
	before := lostUndecodable.Load()
	// An EventEnvelope: well-formed, carries an org, names no signal.
	msg := &infra.StreamMessage{Subject: "event.error", Data: []byte(`{"org":"acme","id":"e1","name":"$error"}`)}
	if err := s.land(context.Background(), errorWriter(t), msg); err != nil {
		t.Fatalf("a foreign envelope must be acked, got %v — naked it would redeliver forever", err)
	}
	if got := lostUndecodable.Load(); got != before {
		t.Errorf("loss counter moved %d→%d for a message addressed to another consumer", before, got)
	}
}

// TestUndecodableIsCountedAsLoss is the other side: a payload that is not JSON at all is
// acked (it will not parse on the ninth attempt either, and holding the durable behind it
// would stop every well-formed fact on the same table) and is therefore GONE. That is the
// case the counter exists for.
func TestUndecodableIsCountedAsLoss(t *testing.T) {
	s := &drain{log: luxlog.New("test")}
	before := lostUndecodable.Load()
	msg := &infra.StreamMessage{Subject: "event.error", Data: []byte(`{not json`)}
	if err := s.land(context.Background(), errorWriter(t), msg); err != nil {
		t.Fatalf("an undecodable message must be acked, got %v", err)
	}
	if got := lostUndecodable.Load(); got != before+1 {
		t.Fatalf("loss counter %d→%d, want +1 — a dropped fact that increments nothing is a "+
			"silent one", before, got)
	}
	if lossReport().Undecodable != lostUndecodable.Load() {
		t.Error("the health report does not carry the counter it reports")
	}
}

// TestTheAdvisoryIsTheOneTheBusPublishes pins the subject the sink listens on. JetStream
// stops redelivering after maxDeliver and says so ONLY here, so a typo in this string is
// a subscription that matches nothing — which looks exactly like no data being lost.
func TestTheAdvisoryIsTheOneTheBusPublishes(t *testing.T) {
	const pre = "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES"
	if want := pre + "." + EventStream + ".*"; maxDeliverAdvisory != want {
		t.Fatalf("advisory subject = %q, want %q", maxDeliverAdvisory, want)
	}
	s := &drain{log: luxlog.New("test")}
	before := lostExhausted.Load()
	s.exhausted(&infra.Message{
		Subject: pre + "." + EventStream + ".event-error",
		Data:    []byte(`{"stream":"EVENT","consumer":"event-error","stream_seq":42}`),
	})
	if got := lostExhausted.Load(); got != before+1 {
		t.Fatalf("exhausted counter %d→%d, want +1", before, got)
	}
	if lossReport().Exhausted != lostExhausted.Load() {
		t.Error("the health report does not carry the counter it reports")
	}
}

// errorWriter is the sink's event.error writer, looked up by SIGNAL rather than by index
// so these tests keep testing the error path when the writers list is reordered.
func errorWriter(t *testing.T) writer {
	t.Helper()
	for _, w := range writers {
		if w.signal == signalError {
			return w
		}
	}
	t.Fatal("no event.error writer")
	return writer{}
}
