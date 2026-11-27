package o11y

import (
	"context"
	"fmt"
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
		as[i] = alert{Labels: map[string]string{"alertname": "Flood", "severity": "warning", "instance": fmt.Sprintf("node-%d", i)}}
	}
	got := slackText(&webhook{Receiver: "r", Status: "firing"}, as)
	if n := strings.Count(got, "• "); n > 20 {
		t.Errorf("page carried %d alert lines, want at most 20", n)
	}
	if !strings.Contains(got, "and 80 more") {
		t.Errorf("the dropped alerts must be counted in the page:\n%s", got)
	}
}

// An alert reported by four series that differ only in a label the page does not
// print — the pod that pushed it — is one line, counted, not four
// (hanzo-inc/cloud#828). Alerts that read differently stay separate lines.
func TestSlackTextSaysOneAlertOnce(t *testing.T) {
	var as []alert
	for _, pod := range []string{"cloud-a", "cloud-b", "cloud-c", "cloud-d"} {
		as = append(as, alert{
			Labels:      map[string]string{"alertname": "BackupFailed", "severity": "critical", "entry": "cron-sql-backup", "service_instance_id": pod},
			Annotations: map[string]string{"summary": "Backup cron-sql-backup failed its last 16 run(s)"},
		})
	}
	as = append(as, alert{
		Labels:      map[string]string{"alertname": "BackupFailed", "severity": "critical", "entry": "cron-kv-backup"},
		Annotations: map[string]string{"summary": "Backup cron-kv-backup failed its last 1 run(s)"},
	})
	got := slackText(&webhook{Receiver: "hanzo-critical", Status: "firing"}, as)
	if n := strings.Count(got, "cron-sql-backup failed"); n != 1 {
		t.Fatalf("one alert said %d times:\n%s", n, got)
	}
	for _, want := range []string{"2 alert(s)", "cron-sql-backup failed its last 16 run(s) (×4)", "cron-kv-backup failed its last 1 run(s)\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("page missing %q:\n%s", want, got)
		}
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
