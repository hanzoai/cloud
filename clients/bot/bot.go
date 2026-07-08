// Package botsvc mounts /v1/bot/* — a reverse proxy to the in-cluster
// bot-gateway (the OpenAI-compatible agent gateway that owns channels, skills,
// and the agent API). The console Bot module probes /v1/bot/health and links
// out to the operational surfaces; without this the route 404s "not routed on
// this host". No bot logic is reimplemented here — bot-gateway owns it.
//
// Path mapping: bot-gateway serves bare paths (/health, /v1/chat/completions),
// NOT the /v1/bot/* prefix — the edge strips it. So this facade strips /v1/bot
// too: /v1/bot/<rest> → {bot-gateway}/<rest> (e.g. /v1/bot/health → /health).
//
// Order 143 — binds /v1/bot/* before the AI subsystem's /v1/* catch-all (150).
package bot

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// identityHeaders are forwarded so bot-gateway sees the gateway-minted tenant
// context (already sanitized + re-injected by middleware_identity upstream).
var identityHeaders = []string{
	"Authorization", "X-Org-Id", "X-User-Id", "X-User-Email", "X-Project-Id", "X-Environment",
}

type service struct {
	target string // bot-gateway base, no trailing slash
	log    luxlog.Logger
	cc     *http.Client
}

func botURL() string {
	return strings.TrimRight(firstNonEmpty(getenv("BOT_GATEWAY_URL"), "http://bot-gateway.hanzo.svc"), "/")
}

// Mount registers the /v1/bot/* surface on app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("bot.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("bot.Mount: nil deps.Logger")
	}
	s := &service{
		target: botURL(),
		log:    deps.Logger.New("subsystem", "bot"),
		cc:     &http.Client{Timeout: 60 * time.Second},
	}
	app.All("/v1/bot/*", s.proxy)
	s.log.Info("bot surface mounted", "target", s.target, "brand", deps.Brand)
	return nil
}

func (s *service) proxy(c *zip.Ctx) error {
	// Gate on a validated principal before forwarding X-Org-Id to bot-gateway,
	// which trusts these headers as the gateway-minted tenant context. Off-gateway,
	// the identity middleware restores a forged X-Org-Id but leaves X-User-Id empty;
	// a no-principal request must be refused before it hands bot-gateway a victim
	// tenant — the same gate every data-plane resolver (crm/kms/ml/…) already ships.
	if !principal.Validated(c) {
		return zip.ErrForbidden("no validated principal")
	}
	// Strip the /v1/bot prefix — bot-gateway serves bare paths.
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
		return zip.Errorf(http.StatusInternalServerError, "bot: build request: %v", err)
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
		return zip.Errorf(http.StatusBadGateway, "bot: gateway unreachable: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.SetHeader("Content-Type", ct)
	}
	return c.Bytes(resp.StatusCode, rb)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func getenv(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func init() {
	cloud.Register("bot", 143, cloud.Typed(Mount))
}
