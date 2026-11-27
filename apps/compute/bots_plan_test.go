package compute

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The suite's bots are on a paid plan unless a test says otherwise.
func init() {
	payerTier = func(context.Context, string, string) (string, error) { return "pro", nil }
}

func tierIs(t *testing.T, tier string, err error) {
	t.Helper()
	prev := payerTier
	payerTier = func(context.Context, string, string) (string, error) { return tier, err }
	t.Cleanup(func() { payerTier = prev })
}

func launch(t *testing.T) (int, string, *botVM, *fakeAgents) {
	t.Helper()
	f, fa := newBotVM(), newFakeAgents()
	app := mountBots(t, f, fa.server(t).URL)
	code, body := do(t, app, http.MethodPost, "/v1/compute/machines", "acme",
		map[string]any{"kind": "bot", "size": "s-2vcpu-4gb", "region": "sfo3", "name": "helper"})
	return code, string(body), f, fa
}

func TestAFreePayerCannotLaunchABot(t *testing.T) {
	tierIs(t, "free", nil)
	code, body, f, fa := launch(t)
	if code != http.StatusPaymentRequired || !strings.Contains(body, `"paid_plan_required"`) {
		t.Fatalf("free launch = %d %s, want 402 paid_plan_required", code, body)
	}
	if len(f.bots) != 0 || fa.wasCreated("helper") {
		t.Fatalf("a refused launch created something (bots=%d agent=%v)", len(f.bots), fa.wasCreated("helper"))
	}
}

func TestAPaidPayerLaunchesABot(t *testing.T) {
	tierIs(t, "pro", nil)
	if code, body, _, _ := launch(t); code != http.StatusCreated {
		t.Fatalf("paid launch = %d %s, want 201", code, body)
	}
}

func TestAnUnreadableOrUnnamedTierRefuses(t *testing.T) {
	tierIs(t, "", errors.New("commerce down"))
	if code, body, f, _ := launch(t); code != http.StatusServiceUnavailable || len(f.bots) != 0 {
		t.Fatalf("unreadable tier = %d %s, want 503 and no bot", code, body)
	}
	tierIs(t, "", nil)
	if code, body, f, _ := launch(t); code != http.StatusPaymentRequired || len(f.bots) != 0 {
		t.Fatalf("no tier = %d %s, want 402 and no bot", code, body)
	}
}

func TestAFreeDryRunIsStillAQuote(t *testing.T) {
	tierIs(t, "free", nil)
	app := mountBots(t, newBotVM(), "")
	code, _ := do(t, app, http.MethodPost, "/v1/compute/machines", "acme",
		map[string]any{"kind": "bot", "size": "s-2vcpu-4gb", "name": "helper", "dryRun": true})
	if code != http.StatusOK {
		t.Fatalf("dry run = %d, want 200", code)
	}
}
