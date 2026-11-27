package notify

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"text/template"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/secret"
	ntypes "github.com/hanzoai/notify/pkg/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Mount order is the row position in manifest/apps.go: /v1/notify/* must bind after
// the product control planes and BEFORE the AI /v1/* catch-all, so the send
// routes resolve here rather than in the AI fallthrough.

// notifier is the minimal delivery contract every notifyd provider satisfies
// (github.com/hanzoai/notify.Notifier). Declaring it locally lets this package
// depend only on the concrete service/* provider packages, not the library root.
type notifier interface {
	Send(ctx context.Context, subject, message string) error
}

// alternative is an email provider that carries a plain-text part beside the HTML
// body (hanzoai/notify mail and twilioemail).
type alternative interface {
	Alternative(text string)
}

// service holds the mount-time dependencies. send is the ONE delivery function:
// production points it at sendReal; tests inject a fake to assert the route
// contract without touching a real provider.
type service struct {
	log  luxlog.Logger
	kms  secret.Store
	send func(ctx context.Context, org, channel, provider string, to []string, subject, body string) (usedProvider string, err error)
}

// Mount registers the native /v1/notify/* send surface on app.
//
// Every route here is a TYPED op — the one registration REST, OpenAPI, the MCP
// tool list, the CLI and the by-name call plane all project from. Typing the
// send routes is what lets a sibling process (IAM's OTP sender) reach them as a
// typed zip.Call instead of hand-rolling HTTP against an undeclared shape.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("notify: nil app")
	}
	log := luxlog.Default()
	if log != nil {
		log = log.New("subsystem", "notify")
	}
	s := &service{log: log, kms: deps.KMS}
	s.send = func(ctx context.Context, org, channel, provider string, to []string, subject, body string) (string, error) {
		svc, _, err := s.sendReal(ctx, org, channel, provider, to, subject, body, "", nil)
		return svc, err
	}
	routes(app, s)
	// Publish delivery on the internal plane so a sibling subsystem can send for
	// ANY tenant without holding a credential — see send_rpc.go. The HTTP surface
	// above is untouched: principal-derived org, customers only.
	exposeSend(s)

	if log != nil {
		log.Info("notify send surface mounted", "prefix", "/v1/notify")
	}
	return nil
}

// routes is the ONE place the surface is declared, so a test drives the same
// router the binary serves rather than a reconstruction of it.
func routes(app cloud.Router, s *service) {
	g := app.Group("/v1/notify")
	g.Get("/health", s.health)
	g.Post("/send", s.sendAny)
	g.Post("/send/sms", s.sendSMS)
	g.Post("/send/email", s.sendEmail)
	g.Put("/credentials/:provider/:key", s.setCredential)
	g.Delete("/credentials/:provider/:key", s.removeCredential, zip.WithStatus(http.StatusNoContent))
}

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query. GET carries no request body (zip's hasBody), so this publishes nothing.
type noIn struct{}

// notifyHealth is the GET /v1/notify/health body — notifyd's verbatim, so probes and
// clients that keyed on it keep working unchanged.
type notifyHealth struct {
	// Service names the subsystem answering — always "notify".
	Service string `json:"service"`
	// Status is "ok"; the route answers 200 whenever the subsystem is mounted.
	Status string `json:"status"`
}

// health reports that the notify send surface is mounted.
//
// It is a pure liveness probe: it answers 200 whenever this subsystem is mounted
// and checks nothing downstream, so an "ok" here says the routes are reachable, not
// that any provider credential is configured. The body is notifyd's verbatim, so
// probes and clients that keyed on the standalone service keep working unchanged.
func (s *service) health(ctx context.Context, _ *noIn) (*notifyHealth, error) {
	return &notifyHealth{Service: "notify", Status: "ok"}, nil
}

// notifySend is the send contract — notifyd's SendRequest, restated field by field so
// the published schema can describe itself (an imported type's prose is invisible
// to zipdoc). The fields this fold ignores (idempotency_key, send_at, options) are
// deliberately not declared: a body carrying them still decodes, and a schema that
// listed them would promise machinery the fold does not run.
type notifySend struct {
	// To is the destination address per recipient — a phone number for sms, an
	// email address for email. Several recipients fan out into one provider call
	// each, and the response shape follows the count (see the items field).
	To []string `json:"to"`
	// Channel selects the delivery channel, sms or email. The per-channel routes
	// (/send/sms, /send/email) pin it, overriding whatever the body names; on the
	// generic route it is required.
	Channel string `json:"channel"`
	// Provider pins a provider service name (twilio, plivo, twilio_email, mail).
	// Empty picks the one whose org credentials are actually configured in KMS.
	Provider string `json:"provider,omitempty"`
	// Subject is the message subject, carried on the email channel only.
	Subject string `json:"subject,omitempty"`
	// Body is the message text, sent verbatim when present — the no-template path.
	Body string `json:"body,omitempty"`
	// TemplateID selects a built-in template when Body is empty.
	TemplateID string `json:"template_id,omitempty"`
	// TemplateVars carries the values the selected template renders against, as
	// a raw JSON object. Raw on purpose: the by-name call plane computes an
	// input's layout before reading any payload and refuses a map field
	// outright, which would make every typed call to these ops fail — and that
	// call is the reason they are typed at all (IAM's OTP sender). deliver
	// decodes it right before the template renders, so REST bodies decode
	// byte-identically to the map this replaced.
	TemplateVars json.RawMessage `json:"template_vars,omitempty"`
	// Event is the event name, which doubles as the template id when TemplateID is
	// empty — the IAM OTP path sends event=iam.otp_sent and nothing else.
	Event string `json:"event,omitempty"`
	// Sync must be exactly "true": delivery here is synchronous, and anything else
	// answers 503 because the queue plane that would run an async dispatch is owned
	// elsewhere. Over REST it rides as ?sync=true (the URL binds over the body); a
	// by-name call states it in its arguments.
	Sync string `json:"sync,omitempty"`
}

// notifyOutcome is one recipient's outcome — notifyd's SendResponse as the sync
// fold has always answered it, so IAM's decoder keeps working unchanged.
type notifyOutcome struct {
	// MessageID is the opaque per-recipient message handle this service minted.
	MessageID string `json:"message_id"`
	// Status is "sent" on success and "failed" on a terminal provider failure —
	// which is still a 200, never a transport error, so a batch reports every
	// recipient's outcome instead of dying on the first.
	Status string `json:"status"`
	// Error carries the provider's failure reason when Status is "failed".
	Error string `json:"error,omitempty"`
}

// notifyDelivery is the send answer, and it keeps the bytes notifyd shipped: ONE
// recipient answers the bare outcome object ({message_id,status}), several answer
// this envelope. MarshalJSON states that fold once, so every JSON projection —
// REST, MCP — writes the exact bytes the raw handler always wrote, while a typed
// caller on the call plane reads the one declared shape.
type notifyDelivery struct {
	// Items is the per-recipient outcome, in request order. A single-recipient
	// send answers items[0] BARE — the object itself, not this envelope.
	Items []notifyOutcome `json:"items"`
}

// MarshalJSON folds a single-recipient delivery to its bare outcome, which is the
// shape IAM's OTP sender has always decoded.
func (d *notifyDelivery) MarshalJSON() ([]byte, error) {
	if len(d.Items) == 1 {
		return json.Marshal(d.Items[0])
	}
	type envelope notifyDelivery // sheds the method, so the envelope marshals plainly
	return json.Marshal((*envelope)(d))
}

// SendAny delivers one transactional message by email or SMS through the caller
// org's own provider credential.
//
// The channel comes from the body — sms or email — and the provider credential is
// read from KMS at orgs/<org>/notify/<service>/<key>, never from the environment.
// The org is the validated principal's, never a client-supplied value, so a caller
// can only ever send as their own tenant; an unauthenticated caller gets 401.
// Naming no provider picks the one whose credentials are actually configured
// (Twilio, then Plivo for SMS; Twilio Email, then SMTP for email) and fails closed
// when none is. Delivery is synchronous and per recipient: one recipient answers
// the bare {message_id,status} outcome, several answer the {items:[…]} envelope. A
// terminal provider failure is a 200 whose status is failed with the reason in
// error, never a transport error. sync=true is REQUIRED — an async dispatch
// answers 503, because the queue plane that would run it is owned elsewhere. The
// message body wins verbatim when present; otherwise template_id (or the event
// name) selects a built-in template rendered against template_vars.
func (s *service) sendAny(ctx context.Context, in *notifySend) (*notifyDelivery, error) {
	return s.deliver(ctx, in, "")
}

// Delivers one transactional SMS through the caller org's own provider
// credential.
//
// It is the channel-pinned form of the generic send: identical in every respect
// except that the channel is fixed to sms, OVERRIDING whatever the body names —
// so a body that says email still goes out as a text message. The provider is the
// org's own SMS credential from KMS (Twilio, then Plivo), resolved for the
// validated principal's org; an unauthenticated caller gets 401.
func (s *service) sendSMS(ctx context.Context, in *notifySend) (*notifyDelivery, error) {
	return s.deliver(ctx, in, string(ntypes.ChannelSMS))
}

// SendEmail delivers one transactional email through the caller org's own
// provider credential.
//
// It is the channel-pinned form of the generic send: identical in every respect
// except that the channel is fixed to email, OVERRIDING whatever the body names —
// so a body that says sms still goes out as mail. The provider is the org's own
// email credential from KMS (Twilio Email, then SMTP), resolved for the validated
// principal's org; an unauthenticated caller gets 401. Subject is carried on the
// email channel only.
func (s *service) sendEmail(ctx context.Context, in *notifySend) (*notifyDelivery, error) {
	return s.deliver(ctx, in, string(ntypes.ChannelEmail))
}

// deliver is the ONE send path behind all three routes. pinned is set on the
// per-channel routes and empty on the generic one, which reads the channel from
// the body. Every refusal is the raw handler's, status for status.
func (s *service) deliver(ctx context.Context, in *notifySend, pinned string) (*notifyDelivery, error) {
	// The org is the VALIDATED principal, never a client value — the whole point
	// of moving the trust boundary into cloud (see package doc). Both facts come
	// off the context cloud.Bridge parked, so the CLI's request-less invoke is
	// refused with the same 401 an anonymous REST call gets.
	if !principal.ValidatedFrom(ctx) {
		return nil, zip.ErrUnauthorized("notify: authentication required")
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok || org == "" {
		return nil, zip.ErrUnauthorized("notify: org scope required")
	}

	// The raw template_vars object becomes the map the renderer reads. A body
	// whose template_vars is not an object is the same 400 the map field gave.
	var vars map[string]any
	if len(in.TemplateVars) > 0 {
		if err := json.Unmarshal(in.TemplateVars, &vars); err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "notify: template_vars: %v", err)
		}
	}
	req := ntypes.SendRequest{
		To: in.To, Channel: ntypes.Channel(in.Channel), Provider: in.Provider,
		Subject: in.Subject, Body: in.Body,
		TemplateID: in.TemplateID, TemplateVars: vars, Event: in.Event,
	}
	if pinned != "" {
		req.Channel = ntypes.Channel(pinned)
	}
	if len(req.To) == 0 {
		return nil, zip.Errorf(http.StatusBadRequest, "notify: 'to' is required")
	}
	channel := string(req.Channel)
	if channel == "" {
		return nil, zip.Errorf(http.StatusBadRequest, "notify: channel is required (in the body or via /send/{sms,email})")
	}

	subject, body, err := render(&req)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "notify: %v", err)
	}

	// Sync-only fold: async requires the Temporal worker plane, which is not
	// folded. Fail closed and loud, exactly as notifyd does without a worker —
	// never a silent sync fallback that would mask a misconfiguration.
	if in.Sync != "true" {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"notify: async dispatch is not available in the cloud fold — call POST /v1/notify/send?sync=true")
	}

	out := make([]notifyOutcome, 0, len(req.To))
	for _, to := range req.To {
		resp := notifyOutcome{MessageID: newMessageID()}
		usedProvider, sendErr := s.send(ctx, org, channel, req.Provider, []string{to}, subject, body)
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
	return &notifyDelivery{Items: out}, nil
}

// sendReal resolves the provider, constructs it from credentials, and delivers
// synchronously. Returns the provider service name it used (for the audit log).
//
// text is an email's plain-text part, sent beside the HTML body by a provider that
// carries one (every email provider wired here does); empty sends the body alone.
// A provider that carries none still delivers the HTML, which holds every word.
func (s *service) sendReal(ctx context.Context, org, channel, provider string, to []string, subject, body, text string, headers map[string]string) (svc, id string, err error) {
	svc = provider
	if svc == "" {
		if svc, err = s.pick(ctx, org, channel); err != nil {
			return "", "", err
		}
	}
	creds := s.creds(ctx, org, svc)
	n, err := open(svc, creds, to)
	if err != nil {
		return svc, "", err
	}
	if a, ok := n.(alternative); ok && text != "" && channel == "email" {
		a.Alternative(text)
	}
	if len(headers) > 0 && channel == "email" {
		switch h := n.(type) {
		case headed:
			return svc, "", h.SendWithHeaders(ctx, subject, body, headers)
		case setter:
			h.SetHeaders(headers)
		}
	}
	if m, ok := n.(numbered); ok {
		id, err = m.SendWithID(ctx, subject, body)
		return svc, id, err
	}
	return svc, "", n.Send(ctx, subject, body)
}

// headed is a provider that sends with extra MIME headers in one call
// (twilio_email).
type headed interface {
	SendWithHeaders(ctx context.Context, subject, message string, headers map[string]string) error
}

// setter is a provider that is given its extra MIME headers before it sends
// (ses, sendgrid).
type setter interface {
	SetHeaders(map[string]string)
}

// numbered is a provider that answers its own id for the message (ses,
// sendgrid), which its delivery events name it by.
type numbered interface {
	SendWithID(ctx context.Context, subject, body string) (string, error)
}

// pick chooses the provider for a channel when the caller pinned none: the
// registered providers of that channel in preference order, first one whose org
// credentials are actually present in KMS. Transactional mail may ride any
// provider the org holds, SES and SendGrid included when they are its only ones:
// the fence is the other way round, commercial mail never rides notify. It fails
// CLOSED — a channel no provider declares, and a
// channel whose providers are all unconfigured for this org, are both refused
// rather than attempted.
func (s *service) pick(ctx context.Context, org, channel string) (string, error) {
	candidates := forChannel(ntypes.Channel(channel))
	if len(candidates) == 0 {
		return "", fmt.Errorf("notify: channel %q is not served by any provider in the cloud fold", channel)
	}
	for _, p := range candidates {
		if hasKeys(s.creds(ctx, org, p.ID), p.Needs...) {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("notify: no %s provider configured for org %q", channel, org)
}

// creds resolves a provider's credential bag from KMS ONLY — never env, never
// plaintext, never logged. Each key is read from cloud's embedded KMS
// (cloud.Deps.KMS) at the org-scoped, rotatable ref orgs/<org>/notify/<svc>/<key>
// (credentialRef), where setCredential seals it. A nil store or a missing key
// leaves the value empty; open then fails closed.
//
// The reads act for org, the tenant the refs name: the store serves orgs/<org>/…
// only to a call acting for <org> (apps/kms attest.go admit). ctx is whatever
// reached notify, and a code sent before sign-in states no tenant, which the store
// answers as the deployment's own. org is already the validated one — the
// principal's on the HTTP route, the peer's stated tenant on the plane — so the
// reads state it rather than inherit ctx's. ctx still bounds them.
func (s *service) creds(ctx context.Context, org, svc string) map[string]string {
	out := make(map[string]string, 4)
	if s.kms == nil {
		return out
	}
	kctx := cloud.For(context.Background(), org)
	if d, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		kctx, cancel = context.WithDeadline(kctx, d)
		defer cancel()
	}
	for _, k := range keysFor(svc) {
		if v, err := s.kms.GetSecret(kctx, credentialRef(org, svc, k)); err == nil && len(v) > 0 {
			out[k] = string(v)
		}
	}
	return out
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
	maps.Copy(out, in)
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
