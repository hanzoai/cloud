// Package promptsvc mounts the Hanzo Cloud /v1/prompts/* surface: a thin facade
// over the console (Langfuse) public prompts API — the same console the eval
// facade composes. Prompt versions, labels, tags, and history live in the
// console; this proxies list / get / create to /api/public/v2/prompts under the
// project-scoped console key pair (HTTP Basic), so the console2 Prompts module
// resolves REAL prompts instead of an honest-empty "not routed on this host" state.
//
// No prompt logic is reimplemented here — the console owns storage, versioning,
// and labels. Auth mirrors evalsvc: the public/secret key pair (per-org via KMS,
// else the global console-keys secret) IS the org → console-project binding, so
// there is no separate projectId to thread.
//
// Order 144: binds /v1/prompts/* BEFORE the AI subsystem's /v1/* catch-all (150),
// the same slot the eval facade uses (145). The composition root auto-registers
// GET /v1/prompts/health for every subsystem in the registry.
package prompt

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// config is resolved once at Mount from the canonical console env (the
// console-keys / console-langfuse-keys KMS-synced secret). Endpoint defaults to
// the real in-cluster console Service DNS; keys have no default and fail closed.
type config struct {
	consoleURL string // console base, no trailing slash
	publicKey  string // console (Langfuse) public key — global fallback
	secretKey  string // console (Langfuse) secret key — global fallback
}

func loadConfig() config {
	return config{
		consoleURL: strings.TrimRight(firstNonEmpty(
			getenv("CONSOLE_HOST"),
			getenv("consoleEndpoint"),
			"http://console.hanzo.svc.cluster.local",
		), "/"),
		publicKey: firstNonEmpty(getenv("CONSOLE_PUBLIC_KEY"), getenv("LANGFUSE_PUBLIC_KEY")),
		secretKey: firstNonEmpty(getenv("CONSOLE_SECRET_KEY"), getenv("LANGFUSE_SECRET_KEY")),
	}
}

type service struct {
	cfg config
	log luxlog.Logger
	kms cloud.KMSClient // per-org key override; nil when KMS is not wired in-process
	cc  *http.Client    // console client
}

// Mount registers the /v1/prompts/* surface on app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("prompt.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("prompt.Mount: nil deps.Logger")
	}
	s := &service{
		cfg: loadConfig(),
		log: deps.Logger.New("subsystem", "prompts"),
		kms: deps.KMS,
		cc:  &http.Client{Timeout: 30 * time.Second},
	}

	// Collection: list (what the Prompts module loads) + create. Item: get by
	// name (detail/history). The console (Langfuse) public v2 prompts API is the
	// source of truth; bodies/queries pass through verbatim so console validation
	// surfaces honestly.
	collection := func(c *zip.Ctx) string { return "/api/public/v2/prompts" }
	app.Get("/v1/prompts", s.proxy(http.MethodGet, collection))
	app.Post("/v1/prompts", s.proxy(http.MethodPost, collection))
	app.Get("/v1/prompts/:name", s.proxy(http.MethodGet, func(c *zip.Ctx) string {
		return "/api/public/v2/prompts/" + url.PathEscape(c.Fiber().Params("name"))
	}))

	s.log.Info("prompts surface mounted",
		"console", s.cfg.consoleURL,
		"consoleKey", s.cfg.publicKey != "",
		"brand", deps.Brand,
	)
	return nil
}

// proxy forwards method to the console path (computed per-request from route
// params) under HTTP Basic, resolving the project key pair from the request's
// tenant and returning the console's status + body unchanged.
func (s *service) proxy(method string, pathOf func(*zip.Ctx) string) func(c *zip.Ctx) error {
	return func(c *zip.Ctx) error {
		pk, sk, err := s.resolveKeys(c.Context(), tenant(c))
		if err != nil {
			return zip.Errorf(http.StatusServiceUnavailable, "%s", err.Error())
		}
		target := s.cfg.consoleURL + pathOf(c)
		if q := c.Fiber().Request().URI().QueryString(); len(q) > 0 {
			target += "?" + string(q)
		}
		var body io.Reader
		if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
			body = bytes.NewReader(c.Body())
		}
		req, err := http.NewRequestWithContext(c.Context(), method, target, body)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "prompts: build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Basic "+basic(pk, sk))
		resp, err := s.cc.Do(req)
		if err != nil {
			return zip.Errorf(http.StatusBadGateway, "prompts: console unreachable: %v", err)
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(resp.StatusCode, rb)
	}
}

// resolveKeys returns the console key pair for the tenant: per-org KMS keys
// (console-pk-{org}/console-sk-{org}) when KMS is wired in-process, else the
// global key pair from env. Missing keys fail closed with a precise error.
func (s *service) resolveKeys(ctx context.Context, org string) (publicKey, secretKey string, err error) {
	if s.kms != nil && org != "" {
		if pkb, e := s.kms.GetSecret(ctx, "console-pk-"+org); e == nil && len(pkb) > 0 {
			if skb, e2 := s.kms.GetSecret(ctx, "console-sk-"+org); e2 == nil && len(skb) > 0 {
				return string(pkb), string(skb), nil
			}
		}
	}
	if s.cfg.publicKey != "" && s.cfg.secretKey != "" {
		return s.cfg.publicKey, s.cfg.secretKey, nil
	}
	return "", "", fmt.Errorf(
		"prompts: no console API key for org %q: set CONSOLE_PUBLIC_KEY/CONSOLE_SECRET_KEY (KMS-synced secret 'console-keys') or per-org KMS 'console-pk-%s'/'console-sk-%s'",
		org, org, org)
}

// tenant resolves the org slug used to scope console keys, preferring the
// canonical X-Project-Id sub-scope (what console2 stamps), then X-Org-Id.
func tenant(c *zip.Ctx) string {
	if v := c.Header("X-Project-Id"); v != "" {
		return v
	}
	if v := c.Header("X-Org-Id"); v != "" {
		return v
	}
	return c.Org()
}

func basic(pk, sk string) string { return base64.StdEncoding.EncodeToString([]byte(pk + ":" + sk)) }

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
	cloud.Register("prompts", 144, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("prompt.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}
