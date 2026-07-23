package integrations

import "context"

// automation_trigger.go is the outbound seam from the inbound-event surfaces
// (provider webhooks, chat channels) to the automations engine. integrations cannot
// import clients/automations (automations imports integrations for per-org credential
// custody — a cycle), so the composition root (apps.wire_seams) injects
// automations.Deliver here as a primitive-typed func. Until it does — and whenever the
// automations subsystem is disabled — the seam is nil and every fire is a safe no-op.

// TriggerFunc delivers a signature-VERIFIED inbound event to the automations engine.
// org is the resolved tenant (OrgForExternalID / a verified principal) — never a
// client-supplied field. depth is the causation depth (0 for an external-origin webhook).
// It returns how many flows the event started.
type TriggerFunc func(ctx context.Context, org, source, name, dedupeKey string, depth int, payload map[string]any) (int, error)

// fireAutomation is the wired seam (nil until apps.wire_seams sets it).
var fireAutomation TriggerFunc

// SetAutomationTrigger wires the automations Deliver seam. Called once at the
// composition root, before serving.
func SetAutomationTrigger(f TriggerFunc) { fireAutomation = f }

// fireTrigger delivers a verified event to subscribed automation flows, best-effort:
// a nil seam (automations disabled) or an empty org is a no-op, and a dispatch error
// never fails the caller's webhook ack — the provider must still get its 200. org MUST
// already be the verified tenant. An external-origin webhook passes depth 0.
func fireTrigger(ctx context.Context, org, source, name, dedupeKey string, depth int, payload map[string]any) {
	if fireAutomation == nil || org == "" {
		return
	}
	_, _ = fireAutomation(ctx, org, source, name, dedupeKey, depth, payload)
}
