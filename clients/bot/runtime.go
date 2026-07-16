package bot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// runtime.go is the IN-PROCESS client for the bot runtime's own lifecycle API —
// the ONE wire seam cloud uses to DRIVE the runtime (halt a live run), as
// distinct from RunCodingTask (hand it a coding job) and the /v1/bot/* proxy
// (relay an inbound request). Here cloud ORIGINATES the call server-side, having
// already decided — on its own records — that the caller's org owns the run, so
// it mints the identity headers itself and presents the shared gateway service
// token as the pod-boundary bearer.
//
// The runtime is the EXECUTOR, never the authority: it is told which run to stop,
// it does not decide who may stop it. clients/bots makes that call against the
// org-scoped run record before this client is ever reached.

// runtimeCallTimeout bounds a single stop round-trip so a hung runtime cannot
// stall a control-plane request. On timeout the caller reports a clean 502 rather
// than claiming a run was stopped.
const runtimeCallTimeout = 15 * time.Second

// runtimeErrBodyCap bounds the non-2xx body read for an error message.
const runtimeErrBodyCap = 8 << 10

var runtimeHTTP = &http.Client{Timeout: runtimeCallTimeout}

// StopRun asks the bot runtime to halt org's run. It returns nil when the runtime
// tore the run down AND when the runtime has no such live run (404): a run the
// executor is not running is already stopped, so the control plane may close its
// record. A transport failure or any other non-2xx is an error — the run may
// still be live, and the caller must not claim otherwise.
//
// The 404 arm rests on the runtime SERVING this route. A runtime build that does
// not implement it answers 404 for every run, indistinguishable at the wire from
// "no such run" — under which a stop closes the record while the sandbox runs on
// until its own timeout. The runtime shipping this route is therefore a
// precondition of stop being truthful, not merely of it being useful.
func StopRun(ctx context.Context, org, runID string) error {
	base := botURL()
	if base == "" {
		return fmt.Errorf("bot: runtime not configured")
	}
	target := base + "/v1/bots/" + url.PathEscape(runID) + "/stop"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return fmt.Errorf("bot: build stop request: %w", err)
	}
	// Server-originated identity: cloud resolved the tenant and authorized the stop
	// against its own record, so it mints the header the runtime scopes by. The
	// service bearer is the shared gateway token (KMS-injected env); absent => the
	// runtime fails the request closed at its auth gate.
	req.Header.Set("X-Org-Id", org)
	if tok := getenv("BOT_GATEWAY_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := runtimeHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("bot: runtime unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, runtimeErrBodyCap))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300, resp.StatusCode == http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("bot: runtime rejected stop (%d): %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
}
