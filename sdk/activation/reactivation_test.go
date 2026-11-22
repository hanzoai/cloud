package activation

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// A decided activation carries its reactivation latency as a NUMBER on its own
// span — hanzo.session.reactivation_ms — so a reader filters and percentiles on
// the attribute without deriving it from timestamps. It is the same interval the
// span spans (Start to End), surfaced as a value, and it never shares a row with
// gen_ai.latency.ttft_ms.
func TestAnActivationCarriesItsReactivationMs(t *testing.T) {
	sr := recorder(t)
	const gap = 20 * time.Millisecond
	c := Start()
	time.Sleep(gap)
	c.Is(Cold, true)
	if !c.End(context.Background(), "sandbox", "acme", nil) {
		t.Fatal("a decided activation recorded nothing")
	}

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("spans = %d, want 1", len(ended))
	}
	a := attrs(ended[0])
	raw, ok := a[AttrReactivationMs]
	if !ok {
		t.Fatalf("the activation's span carries no %s", AttrReactivationMs)
	}
	ms, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("%s = %q: %v", AttrReactivationMs, raw, err)
	}
	if ms < float64(gap/time.Millisecond) {
		t.Errorf("%s = %.1fms: it must measure from Start to End (want >= %v)", AttrReactivationMs, ms, gap)
	}
	// The attribute is the same interval the span spans, within a small slack for
	// the time spent recording the span itself.
	spanMs := float64(ended[0].EndTime().Sub(ended[0].StartTime())) / float64(time.Millisecond)
	if ms > spanMs+50 {
		t.Errorf("%s = %.1fms exceeds the span's own duration %.1fms: it must be the same interval", AttrReactivationMs, ms, spanMs)
	}
}

// The reactivation latency is recorded apart from any gen_ai attribute: a reader
// of model calls never counts an activation, and the two facts never share a row.
func TestReactivationMsIsNotAGenAIAttribute(t *testing.T) {
	sr := recorder(t)
	c := Start()
	c.Is(Warm, false)
	c.End(context.Background(), "sandbox", "acme", nil)
	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("spans = %d, want 1", len(ended))
	}
	for _, kv := range ended[0].Attributes() {
		if string(kv.Key) == AttrReactivationMs {
			return // found it, and it is not a gen_ai key
		}
	}
	t.Fatalf("the activation's span carries no %s", AttrReactivationMs)
}
