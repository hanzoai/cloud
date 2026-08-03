package o11y

import (
	"context"
	"strings"
	"testing"
)

// TestSlackTextRendersFiringAndResolved pins the page format: the headline
// says which way the incident moved, and each line carries what an on-call
// reads first — name, severity, instance, the human summary.
func TestSlackTextRendersFiringAndResolved(t *testing.T) {
	p := &webhook{Receiver: "hanzo-pager", Status: "firing"}
	as := []alert{{
		Status:      "firing",
		Labels:      map[string]string{"alertname": "PodCrashLooping", "severity": "critical", "instance": "cloud-abc"},
		Annotations: map[string]string{"summary": "cloud is restarting in a loop"},
	}}
	got := slackText(p, as)
	for _, want := range []string{"FIRING", "hanzo-pager", "PodCrashLooping", "critical", "cloud-abc", "cloud is restarting in a loop"} {
		if !strings.Contains(got, want) {
			t.Errorf("firing page missing %q:\n%s", want, got)
		}
	}
	p.Status = "resolved"
	if got := slackText(p, as); !strings.Contains(got, "RESOLVED") {
		t.Errorf("a recovery must page too:\n%s", got)
	}
}

// TestSlackTextBoundsAStorm — a hundred simultaneous alerts must not post a
// hundred-line wall, and the overflow must be COUNTED, never silently dropped.
func TestSlackTextBoundsAStorm(t *testing.T) {
	as := make([]alert, 100)
	for i := range as {
		as[i] = alert{Labels: map[string]string{"alertname": "Flood", "severity": "warning"}}
	}
	got := slackText(&webhook{Receiver: "r", Status: "firing"}, as)
	if n := strings.Count(got, "• "); n > 20 {
		t.Errorf("page carried %d alert lines, want at most 20", n)
	}
	if !strings.Contains(got, "and 80 more") {
		t.Errorf("the dropped alerts must be counted in the page:\n%s", got)
	}
}

// TestDeliverWithoutAnyEgressIsAFailure — the inverse of what this test used to
// assert. "No channel configured means no paging" was treated as an inert,
// acceptable state; it is the state in which every alert in the fleet reaches
// nobody, so deliver() reports it as a failure and the handler answers 503.
func TestDeliverWithoutAnyEgressIsAFailure(t *testing.T) {
	t.Setenv(alertsSlackChannelEnv, "")
	t.Setenv(alertsWebhookEnv, "")
	via, failures := deliver(context.Background(), &webhook{Receiver: "r", Status: "firing"},
		[]alert{{Labels: map[string]string{"alertname": "X"}}})
	if via != "" {
		t.Fatalf("nothing was configured, yet delivery claims egress %q", via)
	}
	if len(failures) != 1 || failures[0].egress != "none" {
		t.Fatalf("want one 'none' failure, got %+v", failures)
	}
	if !strings.Contains(reason(failures), alertsSlackChannelEnv) {
		t.Fatalf("the reason must name the missing configuration: %s", reason(failures))
	}
}
