// Package notify folds the Hanzo Notify SEND surface into the unified cloud
// binary (HIP-0106), mounting /v1/notify/* natively in-process — the native
// replacement for the standalone notifyd (github.com/hanzoai/notify) Deployment.
//
// SCOPE — the one live contract. notifyd's ONLY production consumer is Hanzo IAM,
// which POSTs OTP sends to POST /v1/notify/send?sync=true with the wire body
//
//	{"to":["…"],"channel":"sms|email","event":"iam.otp_sent",
//	 "template_vars":{"otp":"…","recipient":"…","app":"…"}}
//
// and treats a response status of "sent"/"delivered" as success (see
// hanzoai/iam object/notify_delivery_http.go). This subsystem serves that exact
// contract natively. Everything else notifyd carries — the tenants/templates/
// providers/preferences/unsubscribe/metering/events/messages collections and the
// hanzoai/tasks (Temporal) async worker — has NO live consumer (the live tenant's
// template/provider/event tables are empty and only IAM calls /send), so it is
// deliberately NOT folded. The Temporal notify-send queue plane is owned elsewhere
// and is not touched here; async sends (no ?sync=true) return 503, exactly as
// notifyd does when it runs without a connected worker.
//
// DRY — no reimplementation of provider plumbing. The actual provider
// implementations and the wire structs are notifyd's OWN public packages, imported
// directly: github.com/hanzoai/notify/service/{twilio,twilioemail,plivo,mail} and
// github.com/hanzoai/notify/pkg/types. Only the thin credential→constructor glue
// (constructProvider) — which lives in notifyd's internal/ and is therefore not
// importable across the module boundary — is mirrored here, matching
// internal/tenant/tenant.go verbatim.
//
// SECURITY — the trust boundary moves with the code. notifyd was ClusterIP-internal
// and trusted a raw X-Org-Id header. Mounted here, /v1/notify/send is reachable via
// the public gateway (api.hanzo.ai forwards every path to cloud), so — like
// clients/auto did when it folded the auto engine — this gates on a VALIDATED
// principal and derives the org from principal.Org (the identity middleware's
// trusted, gateway-minted X-Org-Id), never from a client-supplied header. An
// unauthenticated caller gets 401; a signed-in caller can only send scoped to their
// OWN org.
//
// CREDENTIALS — KMS only, never env, never plaintext, never logged. Provider
// credentials are read EXCLUSIVELY from cloud's embedded KMS via cloud.Deps.KMS,
// at the org-scoped, rotatable ref orgs/<org>/notify/<service>/<key> — the SAME
// /orgs/<org> namespace clients/integrations uses, so a cred is writable and
// rotatable through POST /v1/kms/orgs/:org/secrets with a validated org token
// (no operator-injected env Secret, no restart to rotate). The org is the
// VALIDATED principal's tenant (never a client header). A missing key yields an
// empty value and constructProvider fails closed; no secret is ever hard-coded,
// read from the environment, or logged.
package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	ntypes "github.com/hanzoai/notify/pkg/types"
	"github.com/hanzoai/notify/service/mail"
	"github.com/hanzoai/notify/service/plivo"
	"github.com/hanzoai/notify/service/twilio"
	"github.com/hanzoai/notify/service/twilioemail"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Mount order is the slice position in apps.Wire(): /v1/notify/* must bind after
// the product control planes and BEFORE the AI /v1/* catch-all, so the send
// routes resolve here rather than in the AI fallthrough.

// notifier is the minimal delivery contract every notifyd provider satisfies
// (github.com/hanzoai/notify.Notifier). Declaring it locally lets this package
// depend only on the concrete service/* provider packages, not the library root.
type notifier interface {
	Send(ctx context.Context, subject, message string) error
}

// service holds the mount-time dependencies. send is the delivery seam:
// production wires sendReal; tests inject a fake to assert the handler contract
// without touching a real provider.
type service struct {
	log  luxlog.Logger
	kms  cloud.KMSClient
	send func(ctx context.Context, org, channel, provider string, to []string, subject, body string) (usedProvider string, err error)
}

// Mount registers the native /v1/notify/* send surface on app.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("notify.Mount: nil app")
	}
	log := deps.Logger
	if log != nil {
		log = log.New("subsystem", "notify")
	}
	s := &service{log: log, kms: deps.KMS}
	s.send = s.sendReal

	g := app.Group("/v1/notify")
	g.Get("/health", s.health)
	g.Post("/send", s.handleSend(""))
	g.Post("/send/sms", s.handleSend(string(ntypes.ChannelSMS)))
	g.Post("/send/email", s.handleSend(string(ntypes.ChannelEmail)))

	if log != nil {
		log.Info("notify send surface mounted", "prefix", "/v1/notify")
	}
	return nil
}

// health mirrors notifyd's GET /v1/notify/health body verbatim so probes and
// clients that keyed on it keep working unchanged.
func (s *service) health(c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]string{"service": "notify", "status": "ok"})
}

// handleSend returns the POST /v1/notify/send handler. pinnedChannel is set on the
// per-channel convenience routes (/send/sms, /send/email) and left empty on the
// generic route, which reads the channel from the body.
func (s *service) handleSend(pinnedChannel string) zip.Handler {
	return func(c *zip.Ctx) error {
		// The org is the VALIDATED principal, never a client header — the whole
		// point of moving the trust boundary into cloud (see package doc).
		if !principal.Validated(c) {
			return zip.ErrUnauthorized("notify: authentication required")
		}
		org, ok := principal.Org(c)
		if !ok || org == "" {
			return zip.ErrUnauthorized("notify: org scope required")
		}

		var req ntypes.SendRequest
		if err := c.Bind(&req); err != nil {
			return zip.Errorf(http.StatusBadRequest, "notify: malformed request body: %v", err)
		}
		if pinnedChannel != "" {
			req.Channel = ntypes.Channel(pinnedChannel)
		}
		if len(req.To) == 0 {
			return zip.Errorf(http.StatusBadRequest, "notify: 'to' is required")
		}
		channel := string(req.Channel)
		if channel == "" {
			return zip.Errorf(http.StatusBadRequest, "notify: channel is required (in the body or via /send/{sms,email})")
		}

		subject, body, err := render(&req)
		if err != nil {
			return zip.Errorf(http.StatusBadRequest, "notify: %v", err)
		}

		// Sync-only fold: async requires the Temporal worker plane, which is not
		// folded. Fail closed and loud, exactly as notifyd does without a worker —
		// never a silent sync fallback that would mask a misconfiguration.
		if c.Query("sync") != "true" {
			return zip.Errorf(http.StatusServiceUnavailable,
				"notify: async dispatch is not available in the cloud fold — call POST /v1/notify/send?sync=true")
		}

		out := make([]ntypes.SendResponse, 0, len(req.To))
		for _, to := range req.To {
			resp := ntypes.SendResponse{MessageID: newMessageID()}
			usedProvider, sendErr := s.send(c.Context(), org, channel, req.Provider, []string{to}, subject, body)
			if sendErr != nil {
				// Sync terminal failure is a 200 body with status "failed" (the
				// notifyd contract IAM decodes), not a transport error.
				resp.Status = "failed"
				resp.Error = sendErr.Error()
				if s.log != nil {
					s.log.Warn("notify send failed", "org", org, "channel", channel, "provider", usedProvider, "err", sendErr)
				}
			} else {
				resp.Status = "sent"
			}
			out = append(out, resp)
		}

		if len(out) == 1 {
			return c.JSON(http.StatusOK, out[0])
		}
		return c.JSON(http.StatusOK, map[string]any{"items": out})
	}
}

// sendReal resolves the provider, constructs it from credentials, and delivers
// synchronously. Returns the provider service name it used (for the audit log).
func (s *service) sendReal(ctx context.Context, org, channel, provider string, to []string, subject, body string) (string, error) {
	svc := provider
	if svc == "" {
		var err error
		svc, err = s.defaultProvider(ctx, org, channel)
		if err != nil {
			return "", err
		}
	}
	creds := s.creds(ctx, org, svc)
	n, err := constructProvider(svc, creds, to)
	if err != nil {
		return svc, err
	}
	if err := n.Send(ctx, subject, body); err != nil {
		return svc, err
	}
	return svc, nil
}

// defaultProvider picks the provider service for a channel from the credentials
// that are actually configured — mirroring notifyd's env-fallback preference
// order (the providers table is empty in production, so env creds are the live
// path). Twilio is preferred (the notify-twilio Secret), then Plivo/SMTP.
func (s *service) defaultProvider(ctx context.Context, org, channel string) (string, error) {
	switch channel {
	case string(ntypes.ChannelSMS), string(ntypes.ChannelVoice), string(ntypes.ChannelWhatsApp):
		if hasKeys(s.creds(ctx, org, "twilio"), "account-sid", "auth-token", "from-number") {
			return "twilio", nil
		}
		if hasKeys(s.creds(ctx, org, "plivo"), "auth-id", "auth-token") {
			return "plivo", nil
		}
		return "", fmt.Errorf("notify: no SMS provider configured for org %q", org)
	case string(ntypes.ChannelEmail):
		if hasKeys(s.creds(ctx, org, "twilio_email"), "account-sid", "auth-token", "from-email") {
			return "twilio_email", nil
		}
		if hasKeys(s.creds(ctx, org, "mail"), "smtp-host", "sender-email") {
			return "mail", nil
		}
		return "", fmt.Errorf("notify: no email provider configured for org %q", org)
	default:
		return "", fmt.Errorf("notify: channel %q is not supported by the cloud fold (sms|email only)", channel)
	}
}

// creds resolves a provider's credential bag from KMS ONLY — never env, never
// plaintext, never logged. Each key is read from cloud's embedded KMS
// (cloud.Deps.KMS) at the org-scoped, rotatable ref orgs/<org>/notify/<svc>/<key>,
// the same /orgs/<org> convention clients/integrations uses (so a cred is
// writable + rotatable via POST /v1/kms/orgs/:org/secrets). A nil store or a
// missing key leaves the value empty; constructProvider then fails closed.
func (s *service) creds(ctx context.Context, org, svc string) map[string]string {
	out := make(map[string]string, 4)
	if s.kms == nil {
		return out
	}
	for _, k := range credKeys(svc) {
		ref := "orgs/" + org + "/notify/" + svc + "/" + k
		if v, err := s.kms.GetSecret(ctx, ref); err == nil && len(v) > 0 {
			out[k] = string(v)
		}
	}
	return out
}

// ---- provider credential + construction glue (mirrors notifyd internal/tenant) ----

// credKeys is the KMS key set per provider service — mirrors
// internal/tenant.credKeysForService.
func credKeys(svc string) []string {
	switch svc {
	case "plivo":
		return []string{"auth-id", "auth-token", "from-number"}
	case "twilio":
		return []string{"account-sid", "auth-token", "from-number"}
	case "twilio_email":
		return []string{"account-sid", "auth-token", "from-email", "from-name"}
	case "mail":
		return []string{"smtp-host", "smtp-port", "smtp-user", "smtp-password", "sender-email", "sender-name"}
	default:
		return nil
	}
}

// constructProvider builds the notifyd library provider for one service, targeting
// `to`. Mirrors internal/tenant.constructProvider verbatim (that function is
// internal to hanzoai/notify and cannot be imported); the provider IMPLs are the
// real, imported service/* packages, so only this ~40-line switch is duplicated.
func constructProvider(svc string, c map[string]string, to []string) (notifier, error) {
	switch svc {
	case "plivo":
		if c["auth-id"] == "" || c["auth-token"] == "" {
			return nil, errors.New("notify: plivo requires auth-id and auth-token")
		}
		p, err := plivo.New(
			&plivo.ClientOptions{AuthID: c["auth-id"], AuthToken: c["auth-token"]},
			&plivo.MessageOptions{Source: c["from-number"]},
		)
		if err != nil {
			return nil, err
		}
		p.AddReceivers(to...)
		return p, nil
	case "twilio":
		if c["account-sid"] == "" || c["auth-token"] == "" || c["from-number"] == "" {
			return nil, errors.New("notify: twilio requires account-sid, auth-token and from-number")
		}
		t, err := twilio.New(c["account-sid"], c["auth-token"], c["from-number"])
		if err != nil {
			return nil, err
		}
		t.AddReceivers(to...)
		return t, nil
	case "twilio_email":
		if c["account-sid"] == "" || c["auth-token"] == "" || c["from-email"] == "" {
			return nil, errors.New("notify: twilio_email requires account-sid, auth-token and from-email")
		}
		te := twilioemail.New(c["account-sid"], c["auth-token"], c["from-email"], c["from-name"])
		te.AddReceivers(to...)
		return te, nil
	case "mail":
		if c["smtp-host"] == "" || c["sender-email"] == "" {
			return nil, errors.New("notify: mail requires smtp-host and sender-email")
		}
		port := c["smtp-port"]
		if port == "" {
			port = "587"
		}
		m := mail.New(c["sender-email"], c["smtp-host"]+":"+port)
		m.AuthenticateSMTP("", c["smtp-user"], c["smtp-password"], c["smtp-host"])
		m.AddReceivers(to...)
		return m, nil
	default:
		return nil, fmt.Errorf("notify: provider %q not wired in the cloud fold", svc)
	}
}

// ---- template rendering ----

// tmplKey keys the built-in template registry by (id, channel). A channel of ""
// matches any channel.
type tmplKey struct {
	id      string
	channel string
}

// builtinTemplate is a subject+body Go text/template pair rendered against the
// caller's template_vars.
type builtinTemplate struct {
	subject string
	body    string
}

// builtinTemplates ships the templates the live surface needs. notifyd resolves
// per-tenant published templates from its store, but the production store is empty
// (an OTP send there would 400 with "no published template"); shipping the OTP
// template in code makes the fold strictly more available than notifyd is today,
// with no runtime seeding step. Extend this map, never fork the render path.
var builtinTemplates = map[tmplKey]builtinTemplate{
	{"iam.otp_sent", "email"}: {
		subject: "Your {{.app}} verification code",
		body:    "Your {{.app}} verification code is {{.otp}}.\n\nIt expires in 10 minutes. If you did not request it, you can safely ignore this message.",
	},
	{"iam.otp_sent", "sms"}: {
		body: "{{.app}}: your verification code is {{.otp}}. It expires in 10 minutes.",
	},
}

// render produces the (subject, body) to deliver. A raw Body wins verbatim (the
// no-template path). Otherwise the template id is TemplateID or, failing that, the
// Event name (the IAM OTP path sends event=iam.otp_sent with no template_id) and is
// rendered from the built-in registry against TemplateVars.
func render(req *ntypes.SendRequest) (subject, body string, err error) {
	if strings.TrimSpace(req.Body) != "" {
		return req.Subject, req.Body, nil
	}
	id := req.TemplateID
	if id == "" {
		id = req.Event
	}
	if id == "" {
		return "", "", errors.New("body or template_id/event is required")
	}
	t, ok := builtinTemplates[tmplKey{id, string(req.Channel)}]
	if !ok {
		t, ok = builtinTemplates[tmplKey{id, ""}]
	}
	if !ok {
		return "", "", fmt.Errorf("no built-in template %q for channel %q", id, req.Channel)
	}
	vars := normalizeVars(req.TemplateVars)
	if t.subject != "" {
		if subject, err = execTemplate(t.subject, vars); err != nil {
			return "", "", err
		}
	}
	if body, err = execTemplate(t.body, vars); err != nil {
		return "", "", err
	}
	return subject, body, nil
}

// normalizeVars copies the caller's vars and fills brand-neutral defaults so a
// template never renders an empty {{.app}}.
func normalizeVars(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	if s, _ := out["app"].(string); strings.TrimSpace(s) == "" {
		out["app"] = "Hanzo"
	}
	return out
}

// execTemplate renders one text/template. Missing keys render empty (the OTP path
// always supplies otp+app), never error.
func execTemplate(tmpl string, vars map[string]any) (string, error) {
	t, err := template.New("notify").Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := t.Execute(&sb, vars); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// ---- helpers ----

// newMessageID mints a 16-byte hex message id, the notifyd-shaped opaque handle
// returned to the caller.
func newMessageID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "msg-" + fmt.Sprint(len(b))
	}
	return hex.EncodeToString(b[:])
}

// hasKeys reports whether every listed key is present and non-empty in c.
func hasKeys(c map[string]string, keys ...string) bool {
	for _, k := range keys {
		if strings.TrimSpace(c[k]) == "" {
			return false
		}
	}
	return true
}
