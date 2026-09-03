package cloud

// The dead-card regression: a refusal a handler PROPAGATES must reach the client
// as the status the refusal chose, carrying a code the console can branch on.
// Before ErrorHandler every one of these rendered 500 {"error":"…"} unless the
// handler remembered to call Denied — which is why an unfunded chat drew "something
// went wrong" where a paywall belonged.
//
// They drive real requests through the zip/fiber stack against an app configured
// exactly as Serve configures it.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/metering"
	"github.com/zap-proto/zip"
)

// newErrApp serves ONE route that returns err — the propagating handler.
func newErrApp(err error) *zip.App {
	app := zip.New(zip.Config{ErrorHandler: ErrorHandler})
	app.Get("/v1/thing", func(c *zip.Ctx) error { return err })
	return app
}

func getThing(t *testing.T, err error) (int, string) {
	t.Helper()
	resp, rerr := newErrApp(err).Test(httptest.NewRequest(http.MethodGet, "/v1/thing", nil))
	if rerr != nil {
		t.Fatalf("Test request: %v", rerr)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(body)
}

// TestPropagatedRefusalsKeepTheirStatusAndCode is the whole defect: each of these
// errors already NAMES a status, and every one of them used to render 500.
func TestPropagatedRefusalsKeepTheirStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		// The money sentinels — statuses spelled as errors. This is the unfunded
		// chat/console surface.
		{"out of funds", metering.ErrInsufficientBalance, http.StatusPaymentRequired, "insufficient_balance"},
		{"over a scope cap", metering.ErrSpendCapExceeded, http.StatusPaymentRequired, "spend_cap_exceeded"},
		// Wrapped, because a handler almost always adds context on the way up.
		{"wrapped out of funds", fmt.Errorf("chat: %w", metering.ErrInsufficientBalance), http.StatusPaymentRequired, "insufficient_balance"},
		// An upstream that chose a status keeps it, and gains a code where it sent
		// none. This is the shape zip rebuilds from a plane callee (call.go
		// remoteError) and the shape Guard refuses with.
		{"upstream 403", zip.ErrForbidden("admin required"), http.StatusForbidden, "forbidden"},
		{"upstream 401", zip.ErrUnauthorized("sign in"), http.StatusUnauthorized, "unauthorized"},
		{"upstream 402", zip.Errorf(http.StatusPaymentRequired, "upgrade to continue"), http.StatusPaymentRequired, "payment_required"},
		// An upstream's OWN code always wins over the status-derived one.
		{
			"upstream 402 with a code", &zip.HTTPError{Status: http.StatusPaymentRequired, Code: "card_required", Msg: "add a card"},
			http.StatusPaymentRequired, "card_required",
		},
		// Wrapped across a hop: errors.As still finds it, so the number survives.
		{
			"wrapped upstream 402", fmt.Errorf("commerce: %w", &zip.HTTPError{Status: http.StatusPaymentRequired, Code: "insufficient_balance", Msg: "no funds"}),
			http.StatusPaymentRequired, "insufficient_balance",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := getThing(t, tc.err)
			if status != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", status, tc.status, body)
			}
			if !strings.Contains(body, `"code":"`+tc.code+`"`) {
				t.Fatalf("body %s missing code %q", body, tc.code)
			}
		})
	}
}

// TestWithoutTheClientAnUnfundedRequestIs500 is the bug, kept executable: zip's
// DEFAULT handler reads only a *zip.HTTPError, so the sentinel that means 402
// renders as an internal fault with no code on it — the dead card. It is why
// Serve installs ErrorHandler, and it fails the moment anyone stops.
func TestWithoutTheClientAnUnfundedRequestIs500(t *testing.T) {
	app := zip.New(zip.Config{}) // no ErrorHandler: zip's own
	app.Get("/v1/thing", func(c *zip.Ctx) error { return metering.ErrInsufficientBalance })
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/thing", nil))
	if err != nil {
		t.Fatalf("Test request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("default handler status = %d, want 500 — if this changed, the client may no longer be needed", resp.StatusCode)
	}
	if status, _ := getThing(t, metering.ErrInsufficientBalance); status != http.StatusPaymentRequired {
		t.Fatalf("with the client status = %d, want 402", status)
	}
}

// TestGenuineFaultsStayInternal: the client must not launder a fault into something
// a client is invited to retry or pay for. 500 is the ABSENCE of a decision.
func TestGenuineFaultsStayInternal(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"plain error", errors.New("boom")},
		{"typed 500", zip.ErrInternal("store unavailable")},
		{"wrapped typed 500", fmt.Errorf("read: %w", zip.ErrInternal("store unavailable"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := getThing(t, tc.err)
			if status != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (body %s)", status, body)
			}
			if strings.Contains(body, `"code":"payment_required"`) || strings.Contains(body, `"code":"forbidden"`) {
				t.Fatalf("a fault rendered as an actionable refusal: %s", body)
			}
		})
	}
}

// TestOutagesAreNotRefusals holds the fail-closed line. An unreachable peer comes
// back as zip's own 502 for the call; reading that as a refusal would answer the
// money wire with a transport message where "Billing temporarily unavailable"
// belongs, and would tell a console to draw a paywall for an outage.
func TestOutagesAreNotRefusals(t *testing.T) {
	unreachable := fmt.Errorf("gate: commerce unreachable: %w",
		zip.Errorf(http.StatusBadGateway, "zip: call finance_authorize at /run/commerce.sock: connection refused"))

	// The money wire answers its ONE unknown-balance shape, never the transport's.
	if status, code, _ := denial(unreachable); status != http.StatusServiceUnavailable || code != "balance_unavailable" {
		t.Fatalf("denial(unreachable) = (%d,%q), want (503,\"balance_unavailable\")", status, code)
	}
	// The renderer keeps the upstream's own 5xx and adds no actionable code to it.
	status, body := getThing(t, unreachable)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", status, body)
	}
	if strings.Contains(body, `"code":"`) {
		t.Fatalf("an outage gained a refusal code: %s", body)
	}
}

// TestRefusalIsNotMutatedByRendering: refused returns a COPY, so rendering a
// process-wide sentinel cannot rewrite the value every later caller reads.
func TestRefusalIsNotMutatedByRendering(t *testing.T) {
	upstream := &zip.HTTPError{Status: http.StatusForbidden, Msg: "admin required"}
	if _, _ = getThing(t, upstream); upstream.Code != "" {
		t.Fatalf("rendering wrote Code=%q back onto the caller's error", upstream.Code)
	}
}

// TestDenialKeepsAnUpstreamStatus: denial and ErrorHandler read ONE classifier, so
// the money wire no longer re-decides a 402 commerce already chose into its 503
// "balance unknown" — which is the same dead card with a different number.
func TestDenialKeepsAnUpstreamStatus(t *testing.T) {
	status, code, _ := denial(fmt.Errorf("gate: %w", &zip.HTTPError{Status: http.StatusPaymentRequired, Code: "insufficient_balance", Msg: "no funds"}))
	if status != http.StatusPaymentRequired || code != "insufficient_balance" {
		t.Fatalf("denial = (%d,%q), want (402,\"insufficient_balance\")", status, code)
	}
	// Its own fallback is unchanged: an undecidable gate is still 503, never free.
	if status, code, _ := denial(errors.New("dial tcp: connection refused")); status != http.StatusServiceUnavailable || code != "balance_unavailable" {
		t.Fatalf("denial(unknown) = (%d,%q), want (503,\"balance_unavailable\")", status, code)
	}
}
