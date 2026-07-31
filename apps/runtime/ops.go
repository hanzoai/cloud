// ops.go mounts /v1/bot/* — the runtime's OWN operational paths (health, and the
// surfaces the console Bot module links out to), relayed verbatim. It is the
// runtime's ops face, not a control plane: a liveness probe is not a
// tenant-scoped resource, so it stays a relay rather than being reimplemented in
// Go. Everything a tenant can ACT on is native and lives in its own domain —
// /v1/bots is the run control plane (apps/bots).
//
// Path mapping: the runtime serves bare paths (/health, /v1/chat/completions),
// NOT the /v1/bot/* prefix — the edge strips it. So this face strips /v1/bot too:
// /v1/bot/<rest> → {runtime}/<rest> (e.g. /v1/bot/health → /health).
//
// Order 143 — binds /v1/bot/* before the AI subsystem's /v1/* catch-all (150).
//
// The package doc lives once, in runtime.go.

package runtime

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The relay's prose, declared beside the wire fact that keeps it untyped (Mount).
// This face is ONE All() registration, so there is no per-method registration
// site and no typed op for zipdoc to lift a doc comment from; without this the
// seven operations it publishes would carry an operationId and nothing else — a
// generated SDK method that cannot explain itself and a CLI command with no help.
// The loop covers exactly the methods the document renders (openapi.Methods), and
// a description whose route is not in the router never renders, so this stays
// additive metadata on operations that exist.
func init() {
	const summary = "Relay one of the bot runtime's own operational paths"
	const description = "Forwards a request to the bot runtime — the service that executes " +
		"channels and skills — and hands back its answer unchanged. `/v1/bot` is stripped " +
		"before forwarding, because the runtime serves bare paths: /v1/bot/health reaches it " +
		"as /health.\n\n" +
		"This is the runtime's OPS face, not a control plane. A liveness probe is not a " +
		"tenant-scoped resource, so it stays a relay rather than being reimplemented in Go; " +
		"everything a tenant can ACT on is native and typed at /v1/bots.\n\n" +
		"A validated principal is required and the request is refused with 403 before " +
		"anything is forwarded — the runtime trusts the identity headers it receives as " +
		"gateway-minted, so an unauthenticated call must never be allowed to hand it a victim " +
		"tenant. The caller's Authorization, org, user, email, project and environment headers " +
		"ride along; nothing is minted here. The runtime's own status code and Content-Type " +
		"come back verbatim (frequently not JSON), the body is bounded at 16 MiB, and a " +
		"runtime that cannot be reached is 502.\n\n" +
		"One registration owns this address for every method, so which methods actually " +
		"answer is the runtime's decision, not this edge's."
	for _, m := range openapi.Methods() {
		openapi.Describe("/v1/bot/*", m, summary, description)
	}
}

// identityHeaders are forwarded so the runtime sees the gateway-minted tenant
// context (already sanitized + re-injected by middleware_identity upstream). This
// is the RELAY path — an inbound request carrying its own identity — as opposed to
// a Call, which mints identity for an operation cloud originates.
var identityHeaders = []string{
	"Authorization", "X-Org-Id", "X-User-Id", "X-User-Email", "X-Project-Id", "X-Environment",
}

type service struct {
	target string // runtime base, no trailing slash
	log    luxlog.Logger
	cc     *http.Client
}

// Mount registers the /v1/bot/* surface on app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("runtime.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("runtime.Mount: nil deps.Logger")
	}
	s := &service{
		target: url(),
		log:    deps.Logger.New("subsystem", "runtime"),
		cc:     &http.Client{Timeout: 60 * time.Second},
	}
	// UNTYPED BY DESIGN — and it is the only route here, so this whole subsystem
	// publishes no MCP tool and no CLI command. Its PROSE is declared beside the
	// wire fact instead (openapi.Describe, init above), so the document and the
	// generated SDKs can still say what this face is. Three wire facts make it
	// untypable as it stands, each on its own sufficient:
	//
	//   - it is ONE registration for EVERY method (All), including OPTIONS and
	//     TRACE. zip's typed registrars are per-method and it has no All[In, Out].
	//   - the path is a GREEDY wildcard whose value is a whole sub-path the proxy
	//     re-mounts on the runtime (Params("*") below). fiber calls it `*1` and the
	//     document calls it `{wildcard1}`; no typed In field can be both.
	//   - the response is the runtime's own, verbatim: its status code
	//     (c.Bytes(resp.StatusCode, rb), below) and its Content-Type, which is
	//     frequently not JSON at all. A typed op answers its DECLARED status and
	//     serialises its Out as JSON, so both move.
	//
	// The tenant-actionable surface is native and typed elsewhere: /v1/bots is the
	// run control plane (clients/bots). This face is ops, and it stays a relay.
	app.All("/v1/bot/*", s.proxy)
	s.log.Info("runtime ops surface mounted", "target", s.target, "brand", deps.Brand)
	return nil
}

func (s *service) proxy(c *zip.Ctx) error {
	// Gate on a validated principal before forwarding X-Org-Id to the runtime,
	// which trusts these headers as the gateway-minted tenant context. Off-gateway,
	// the identity middleware restores a forged X-Org-Id but leaves X-User-Id empty;
	// a no-principal request must be refused before it hands the runtime a victim
	// tenant — the same gate every data-plane resolver (crm/kms/ml/…) already ships.
	if !principal.Validated(c) {
		return zip.ErrForbidden("no validated principal")
	}
	// Strip the /v1/bot prefix — the runtime serves bare paths.
	rest := strings.TrimPrefix(c.Fiber().Params("*"), "/")
	target := s.target + "/" + rest
	if q := c.Fiber().Request().URI().QueryString(); len(q) > 0 {
		target += "?" + string(q)
	}

	method := c.Fiber().Method()
	var body io.Reader
	if method != http.MethodGet && method != http.MethodHead {
		body = bytes.NewReader(c.Body())
	}
	req, err := http.NewRequestWithContext(c.Context(), method, target, body)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "runtime: build request: %v", err)
	}
	if ct := c.Header("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range identityHeaders {
		if v := c.Header(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := s.cc.Do(req)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "runtime: unreachable: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.SetHeader("Content-Type", ct)
	}
	return c.Bytes(resp.StatusCode, rb)
}
