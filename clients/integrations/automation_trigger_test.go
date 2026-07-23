package integrations

import (
	"context"
	"testing"
)

// TestAutomationTriggerSeam proves the inbound-event seam: a wired seam receives the
// verified event verbatim, an empty org is a fail-closed no-op, and an unwired seam
// (automations disabled) never panics.
func TestAutomationTriggerSeam(t *testing.T) {
	orig := fireAutomation
	t.Cleanup(func() { fireAutomation = orig })

	type call struct {
		org, source, name, dedupe string
		payload                   map[string]any
	}
	var got []call
	SetAutomationTrigger(func(_ context.Context, org, source, name, dedupe string, payload map[string]any) (int, error) {
		got = append(got, call{org, source, name, dedupe, payload})
		return 1, nil
	})

	fireTrigger(context.Background(), "acme", "github", "push", "sha1", map[string]any{"repo": "r"})
	if len(got) != 1 {
		t.Fatalf("seam fired %d times, want 1", len(got))
	}
	if c := got[0]; c.org != "acme" || c.source != "github" || c.name != "push" || c.dedupe != "sha1" || c.payload["repo"] != "r" {
		t.Fatalf("seam passed wrong args: %+v", c)
	}

	// Empty org is a fail-closed no-op even with the seam wired.
	got = nil
	fireTrigger(context.Background(), "", "github", "push", "sha2", nil)
	if len(got) != 0 {
		t.Fatalf("empty-org fire must be a no-op, got %d", len(got))
	}

	// Unwired seam (automations disabled) is a safe no-op — must not panic.
	fireAutomation = nil
	fireTrigger(context.Background(), "acme", "github", "push", "sha3", nil)
}
