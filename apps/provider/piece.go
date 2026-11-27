package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/client"
	peer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/piece"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// piece.go makes every piece in the catalog (package piece) a connector.
//
// The catalog is generated from the pieces source, so a piece is registered
// here from what it DECLARES, never from a hand-written entry:
//
//   - an oauth2 auth becomes an authorization-code flow through the one engine
//     in oauth2.go, against the app credential of its identity host — every
//     Google piece reads GOOGLE_CLIENT_ID, every Microsoft one
//     MICROSOFT_CLIENT_ID — so one registered app lights up every piece that
//     signs in there;
//   - a secret_text, basic_auth or custom_auth becomes a credential form, sealed
//     into the org's KMS namespace like every other connector's;
//   - a piece that needs nothing connects to nothing and is ready as it is.
//
// A piece whose program is vendored also runs its actions, here and only here:
// the credential never leaves this process. Every other app — the automation
// engine, an agent through the plane — asks this one to act.
//
// The catalog is DATA (piece/catalog) and this package is linked by every app
// that asks it anything, so it does not import the data: the composition roots
// that serve connectors hand it over (UsePieces), and every other process
// carries none of it.
//
// A piece whose id a native connector already holds (slack, github, stripe)
// does not replace it: the native one keeps its connect flow, and its card
// gains the piece's logo, actions and triggers.

// noneKind marks a connector that needs no credential: a piece with no auth.
// It is ready without connecting, and connecting it is refused as meaningless.
const noneKind = "none"

// pieceSecret is the KMS secret a credential piece is sealed under: the whole
// auth value, as the piece reads it at run time.
const pieceSecret = "auth"

// Pieces is the connector catalog a process serves, and how it runs one.
type Pieces struct {
	// Catalog is every piece.
	Catalog []piece.Piece
	// Logo is a piece's logo and its content type.
	Logo func(id string) ([]byte, string, bool)
	// Run executes one action with a resolved credential. nil in a process
	// that asks the provider app to run instead.
	Run func(ctx context.Context, org, id, action string, auth any, input map[string]any) (any, error)
	// Check runs a piece's own credential check against the value validate()
	// takes; checked is false when the piece declares none.
	Check func(ctx context.Context, id, kind string, auth any) (checked bool, err error)
}

var pieces struct {
	sync.Mutex
	set    *Pieces
	merged bool
}

// UsePieces installs the catalog this process serves. A composition root calls
// it from init — after every package's own init, so every native connector has
// already claimed its id and a piece never displaces one.
func UsePieces(p Pieces) {
	pieces.Lock()
	pieces.set, pieces.merged = &p, false
	pieces.Unlock()
	withPieces()
}

// installed is the catalog UsePieces installed, or nil.
func installed() *Pieces {
	pieces.Lock()
	defer pieces.Unlock()
	return pieces.set
}

// withPieces merges the installed catalog into the registry, once. Every reader
// of the registry from outside this package's own init calls it first.
func withPieces() {
	pieces.Lock()
	defer pieces.Unlock()
	if pieces.set == nil || pieces.merged {
		return
	}
	pieces.merged = true
	tools := map[string]bool{}
	for id, p := range registry {
		for name := range p.Actions {
			tools[id+"_"+name] = true
		}
	}
	for i := range pieces.set.Catalog {
		pc := &pieces.set.Catalog[i]
		if p, ok := registry[pc.ID]; ok {
			p.Piece = pc
			continue
		}
		p := pieceProvider(pc)
		// A tool name is "<provider>_<action>" and both halves may hold an
		// underscore, so a piece action that would answer to a name another
		// connector already owns is left out rather than shadowing it.
		for name := range p.Actions {
			if tools[pc.ID+"_"+name] {
				delete(p.Actions, name)
			}
		}
		registry[pc.ID] = p
	}
}

// pieceProvider builds the connector a piece declares.
func pieceProvider(pc *piece.Piece) *Provider {
	p := &Provider{
		ID:          pc.ID,
		Name:        pc.Name,
		Description: pc.Description,
		Category:    pieceCategory(pc),
		Piece:       pc,
		generated:   true,
	}
	var oauth, cred *piece.Auth
	for i := range pc.Auth {
		a := &pc.Auth[i]
		switch a.Type {
		case piece.OAuth2:
			if oauth == nil {
				oauth = a
			}
		case piece.SecretText, piece.BasicAuth, piece.CustomAuth:
			if cred == nil {
				cred = a
			}
		}
	}
	if sp, ok := pieceSpec(pc, oauth); ok {
		o := oauthProvider(sp)
		p.Scopes, p.RedirectPath, p.Secrets = o.Scopes, o.RedirectPath, o.Secrets
		p.Configured, p.Creds = o.Configured, o.Creds
		p.Authorize, p.Exchange, p.Revoke = o.Authorize, o.Exchange, o.Revoke
	}
	if cred != nil {
		p.Verify = pieceVerify(pc, *cred)
		p.Fields = pieceFields(*cred)
		p.Secrets = append(p.Secrets, pieceSecret)
		// The credential leg is always available: the org brings the account.
		// An OAuth leg beside it still gates on its own app credential, in
		// connect, exactly as Cloudflare's does.
		p.Configured = func() bool { return true }
		if p.Authorize == nil {
			p.Kind = apiKeyKind
			p.Creds = func() OAuthConfig { return OAuthConfig{} }
		}
	}
	if p.Authorize == nil && p.Verify == nil {
		// Nothing this deployment can perform: either the piece needs no
		// credential, or the one it declares is not one we connect (OIDC, an
		// OAuth whose endpoints are the account's own, client credentials).
		// The card says which; connect refuses either way.
		p.Kind = noneKind
		p.Configured = func() bool { return len(pc.Auth) == 0 }
		p.Creds = func() OAuthConfig { return OAuthConfig{} }
	}
	if pc.Runnable {
		p.Actions = make(map[string]*Action, len(pc.Actions))
		for _, op := range pc.Actions {
			p.Actions[op.Name] = &Action{
				Name:        op.Name,
				DisplayName: op.DisplayName,
				Description: op.Description,
				Props:       pieceProps(op.Props),
				Run:         pieceAsk(pc.ID, op.Name),
			}
		}
	}
	p.Triggers = make(map[string]*Trigger, len(pc.Triggers))
	for _, op := range pc.Triggers {
		p.Triggers[op.Name] = &Trigger{
			Name:        op.Name,
			DisplayName: op.DisplayName,
			Description: op.Description,
			Strategy:    TriggerStrategy(op.Strategy),
			Props:       pieceProps(op.Props),
		}
	}
	return p
}

// ── OAuth ────────────────────────────────────────────────────────────────────

var placeholder = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// pieceSpec turns an oauth2 declaration into the engine's spec. An endpoint
// templated on the account's own fields ("https://{cloud}/common/...") is
// filled from the field's declared default — which is how every Microsoft
// piece names login.microsoftonline.com — and refused when a field has none,
// because a deployment-wide app cannot serve an endpoint it cannot name.
func pieceSpec(pc *piece.Piece, a *piece.Auth) (grant, bool) {
	if a == nil || a.OAuth == nil {
		return grant{}, false
	}
	o := a.OAuth
	if o.Grant == "CLIENT_CREDENTIALS" {
		return grant{}, false
	}
	defaults := map[string]string{}
	for _, pr := range a.Props {
		if v, ok := pr.Default.(string); ok && v != "" {
			defaults[pr.Name] = v
		}
	}
	authURL, ok1 := substitute(o.AuthURL, defaults)
	tokenURL, ok2 := substitute(o.TokenURL, defaults)
	if !ok1 || !ok2 || !httpsURL(authURL) || !httpsURL(tokenURL) {
		return grant{}, false
	}
	app := oauthApp(authURL)
	if app == "" {
		return grant{}, false
	}
	extra := map[string]string{}
	for k, v := range o.Extra {
		extra[k] = v
	}
	if o.Prompt != "" && o.Prompt != "omit" {
		extra["prompt"] = o.Prompt
	}
	return grant{
		id:              pc.ID,
		name:            pc.Name,
		desc:            pc.Description,
		category:        pieceCategory(pc),
		authURL:         authURL,
		tokenURL:        tokenURL,
		scopes:          o.Scopes,
		clientIDEnv:     app + "_CLIENT_ID",
		clientSecretEnv: app + "_CLIENT_SECRET",
		authExtra:       extra,
		basicAuth:       o.Method == "HEADER",
		pkce:            o.PKCE,
		pkcePlain:       o.PKCEMethod == "plain",
	}, true
}

// substitute fills every {name} in s from vals, and reports false when one has
// no value.
func substitute(s string, vals map[string]string) (string, bool) {
	ok := true
	out := placeholder.ReplaceAllStringFunc(s, func(m string) string {
		v, has := vals[m[1:len(m)-1]]
		if !has {
			ok = false
		}
		return v
	})
	// A default may carry a scheme ("https://accounts.zoho.com") where the
	// template supplies one too, or none where the template has none.
	if strings.HasPrefix(out, "https://https://") {
		out = strings.TrimPrefix(out, "https://")
	}
	return out, ok
}

func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// twoPart are the public suffixes of two labels an OAuth host sits under.
var twoPart = map[string]bool{
	"co.uk": true, "com.au": true, "co.jp": true, "co.in": true, "com.br": true, "co.nz": true, "co.za": true,
}

// oauthApp names the app credential an OAuth host signs in with: the host's
// registrable label, upper-cased — accounts.google.com is GOOGLE, so the
// piece reads GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET, the same pair the
// native Google connector reads. One app registration per identity provider,
// never one per piece.
func oauthApp(authURL string) string {
	u, err := url.Parse(authURL)
	if err != nil {
		return ""
	}
	labels := strings.Split(strings.ToLower(u.Hostname()), ".")
	if len(labels) < 2 {
		return ""
	}
	name := labels[len(labels)-2]
	if len(labels) >= 3 && twoPart[strings.Join(labels[len(labels)-2:], ".")] {
		name = labels[len(labels)-3]
	}
	switch name {
	case "microsoftonline":
		name = "microsoft"
	case "squareup":
		name = "square"
	}
	return strings.ToUpper(strings.NewReplacer("-", "_").Replace(name))
}

// ── credentials ──────────────────────────────────────────────────────────────

// pieceFields is the form a credential auth asks for.
func pieceFields(a piece.Auth) []Field {
	switch a.Type {
	case piece.SecretText:
		label := a.DisplayName
		if label == "" || label == "Connection" {
			label = "API key"
		}
		return []Field{{Name: "token", Label: label, Secret: true, Required: true, Help: a.Description}}
	case piece.BasicAuth:
		return []Field{
			{Name: "username", Label: "Username", Required: true, Help: a.Description},
			{Name: "password", Label: "Password", Secret: true, Required: true},
		}
	}
	out := make([]Field, 0, len(a.Props))
	for i, pr := range a.Props {
		if !pr.Input() {
			continue
		}
		f := Field{
			Name:     pr.Name,
			Label:    firstNonEmpty(pr.DisplayName, pr.Name),
			Secret:   pr.Secret(),
			Required: pr.Required,
			Help:     pr.Description,
			Type:     fieldType(pr.Type),
		}
		if i == 0 && f.Help == "" {
			f.Help = a.Description
		}
		for _, o := range pr.Options {
			f.Options = append(f.Options, FieldOption{Label: o.Label, Value: fmt.Sprint(o.Value)})
		}
		if d, ok := pr.Default.(string); ok {
			f.Default = d
		}
		out = append(out, f)
	}
	return out
}

func fieldType(t string) string {
	switch t {
	case "NUMBER":
		return "number"
	case "CHECKBOX":
		return "boolean"
	case "LONG_TEXT", "JSON":
		return "text"
	}
	return ""
}

// maxFieldLen and maxAuthLen bound a pasted credential. Real keys are short;
// a service-account JSON is a few KiB.
const (
	maxFieldLen = 16 << 10
	maxAuthLen  = 64 << 10
)

// pieceVerify checks a credential the way the piece itself would, then hands
// back the value to seal.
//
// It fails closed on shape: a required field missing, a field the piece does
// not declare, a value past the bound. Then, when the piece's program runs
// here and its auth declares validate(), the piece's own check runs against
// its own API — a real sign-in, through the outbound client that reaches only
// public addresses — and a refusal stores nothing. A piece that declares no
// check is sealed on its shape alone; its card says so (checks:false), and the
// first action it runs is the check.
func pieceVerify(pc *piece.Piece, a piece.Auth) func(context.Context, VerifyInput) (*ExchangeResult, error) {
	return func(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
		value, check, err := pieceValue(a, in)
		if err != nil {
			return nil, err
		}
		if set := installed(); a.Validate && set != nil && set.Check != nil {
			if _, err := set.Check(ctx, pc.ID, strings.ToUpper(a.Type), check); err != nil {
				return nil, fmt.Errorf("%s refused the credential: %s", pc.Name, scrub(err.Error(), value))
			}
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if len(raw) > maxAuthLen {
			return nil, errors.New("credential too large")
		}
		return &ExchangeResult{
			Tokens:       map[string]string{pieceSecret: string(raw)},
			AccountLabel: pc.Name,
		}, nil
	}
}

// pieceValue builds the two shapes of one credential: the value an action
// reads (ctx.auth — typed, {type, ...}) and the value validate() reads (the
// secret, {username,password}, or the props object).
func pieceValue(a piece.Auth, in VerifyInput) (map[string]any, any, error) {
	get := func(name string) string {
		if name == "token" && in.Token != "" {
			return in.Token
		}
		return strings.TrimSpace(in.Fields[name])
	}
	for _, v := range in.Fields {
		if len(v) > maxFieldLen {
			return nil, nil, errors.New("a field is too large")
		}
	}
	switch a.Type {
	case piece.SecretText:
		tok := get("token")
		if tok == "" {
			return nil, nil, errors.New("the key is required")
		}
		return map[string]any{"type": "SECRET_TEXT", "secret_text": tok}, tok, nil
	case piece.BasicAuth:
		user, pass := get("username"), get("password")
		if user == "" || pass == "" {
			return nil, nil, errors.New("username and password are required")
		}
		v := map[string]any{"username": user, "password": pass}
		return map[string]any{"type": "BASIC_AUTH", "username": user, "password": pass}, v, nil
	}
	declared := map[string]piece.Prop{}
	for _, pr := range a.Props {
		declared[pr.Name] = pr
	}
	for name := range in.Fields {
		if _, ok := declared[name]; !ok {
			// The name is the caller's; it is not repeated back or logged.
			return nil, nil, errors.New("a field the connector does not declare")
		}
	}
	props := map[string]any{}
	for _, pr := range a.Props {
		if !pr.Input() {
			continue
		}
		raw, given := in.Fields[pr.Name]
		raw = strings.TrimSpace(raw)
		if !given || raw == "" {
			if pr.Default != nil {
				props[pr.Name] = pr.Default
				continue
			}
			if pr.Required {
				return nil, nil, fmt.Errorf("%s is required", firstNonEmpty(pr.DisplayName, pr.Name))
			}
			continue
		}
		switch pr.Type {
		case "NUMBER":
			n, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, nil, fmt.Errorf("%s must be a number", firstNonEmpty(pr.DisplayName, pr.Name))
			}
			props[pr.Name] = n
		case "CHECKBOX":
			props[pr.Name] = raw == "true" || raw == "1" || raw == "on"
		default:
			props[pr.Name] = raw
		}
	}
	return map[string]any{"type": "CUSTOM_AUTH", "props": props}, props, nil
}

// pieceAuth is the value an action reads as ctx.auth, from what this org has
// sealed: the credential a form stored, else the token an OAuth flow did.
func pieceAuth(s *cloud.Service[state], org string, pc *piece.Piece) (any, error) {
	if len(pc.Auth) == 0 {
		return nil, nil
	}
	if !kmsReady(s) {
		return nil, errors.New(errCredentialStore)
	}
	path := kmsPath(org, pc.ID)
	if raw, err := kmsGet(s, path, pieceSecret); err == nil && len(raw) > 0 {
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, errors.New("stored credential unreadable")
		}
		return v, nil
	}
	if tok, err := kmsGet(s, path, accessSecret); err == nil && len(tok) > 0 {
		return map[string]any{"type": "OAUTH2", "access_token": string(tok), "data": map[string]any{}, "props": map[string]any{}}, nil
	}
	return nil, fmt.Errorf("%s is not connected for this org", pc.Name)
}

// ── running ──────────────────────────────────────────────────────────────────

// pieceAsk is a piece action's body in EVERY process: ask the provider app to
// run it. The org is the caller's and never an argument: on a request the call
// forwards the request's own identity, and off one — a flow step running in the
// background — it states the run's validated org, which is the only case zip
// reads a stated caller.
func pieceAsk(id, action string) func(context.Context, RunContext) (any, error) {
	return func(ctx context.Context, rc RunContext) (any, error) {
		ctx = cloud.For(ctx, rc.Org)
		input, err := json.Marshal(rc.Input)
		if err != nil {
			return nil, err
		}
		out, err := peer.IntegrationsRun(ctx, &client.PieceRunIn{Provider: id, Action: action, Input: input})
		if err != nil {
			return nil, err
		}
		if out.Error != "" {
			return nil, errors.New(out.Error)
		}
		var v any
		if len(out.Output) > 0 {
			if err := json.Unmarshal(out.Output, &v); err != nil {
				return nil, err
			}
		}
		return v, nil
	}
}

// runLimit bounds concurrent piece runs per org in this process, so one
// tenant's burst cannot take every goroutine the connectors share.
const runLimit = 16

var running = struct {
	sync.Mutex
	n map[string]int
}{n: map[string]int{}}

func acquireRun(org string) bool {
	running.Lock()
	defer running.Unlock()
	if running.n[org] >= runLimit {
		return false
	}
	running.n[org]++
	return true
}

func releaseRun(org string) {
	running.Lock()
	defer running.Unlock()
	if running.n[org]--; running.n[org] <= 0 {
		delete(running.n, org)
	}
}

// runPiece runs one action of a piece for org, with org's own credential,
// here — the one process that holds it.
func runPiece(ctx context.Context, s *cloud.Service[state], org, id, action string, input map[string]any) (any, error) {
	p, ok := orgProvider(s, id)
	if !ok || p.Piece == nil {
		return nil, zip.ErrNotFound("unknown connector")
	}
	set := installed()
	if _, ok := p.Actions[action]; !ok || set == nil || set.Run == nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s has no action %q that runs here", p.Name, action)
	}
	if !acquireRun(org) {
		return nil, zip.Errorf(http.StatusTooManyRequests, "too many connector actions running for this org")
	}
	defer releaseRun(org)
	auth, err := pieceAuth(s, org, p.Piece)
	if err != nil {
		return nil, zip.Errorf(http.StatusFailedDependency, "%v", err)
	}
	if input == nil {
		input = map[string]any{}
	}
	out, err := set.Run(ctx, org, id, action, auth, input)
	s.Log.Info("connector action", "provider", id, "action", action, "org", org, "ok", err == nil)
	if err != nil {
		// A failed call can name the request it made, and a piece that puts its
		// key in the URL puts the key in that message. Nothing the org sealed
		// leaves in an error.
		return nil, errors.New(scrub(err.Error(), auth))
	}
	return out, nil
}

// scrub removes every credential value in auth from msg. A value shorter than
// four characters is not a secret anyone could recover from a message, and
// removing it would shred the message itself.
func scrub(msg string, auth any) string {
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			if len(x) >= 4 && x != "SECRET_TEXT" && x != "BASIC_AUTH" && x != "CUSTOM_AUTH" && x != "OAUTH2" {
				msg = strings.ReplaceAll(msg, x, "[redacted]")
				msg = strings.ReplaceAll(msg, url.QueryEscape(x), "[redacted]")
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(auth)
	return msg
}

// exposeAct publishes piece execution on the internal plane. Mount calls it.
func exposeAct() {
	cloud.Plane().Post("/integrations/run", planeRun,
		zip.WithOperationID(client.IntegrationsRun),
		zip.WithSummary("Run one connector action as the caller's org, with the org's own credential"))
}

// planeRun runs a connector action for the CALLER'S org, read off the peer
// context and never from the request: an org a caller could name is a
// credential a caller could spend. A failure of the action itself is an
// answer (Error), not a transport error, so a flow step records why. A named
// function, not a closure, so zipdoc lifts this prose into the registry.
func planeRun(ctx context.Context, in *client.PieceRunIn) (*client.PieceRunOut, error) {
	s := mounted
	if s == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "connector run: integrations is not serving")
	}
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("connector run: no org on the call")
	}
	var input map[string]any
	if len(in.Input) > 0 {
		if err := json.Unmarshal(in.Input, &input); err != nil {
			return nil, zip.ErrBadRequest("connector run: input is not a JSON object")
		}
	}
	out, err := runPiece(ctx, s, org, in.Provider, in.Action, input)
	if err != nil {
		var ze *zip.HTTPError
		if errors.As(err, &ze) {
			return nil, err
		}
		return &client.PieceRunOut{Error: err.Error()}, nil
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &client.PieceRunOut{Output: raw}, nil
}

// runIn is one connector action to run for the caller's org.
type runIn struct {
	// Provider is the connector, from the path.
	Provider string `json:"provider"`
	// Action is the action's name, as GET /v1/provider/{provider} lists it.
	Action string `json:"action"`
	// Input is the action's props, by name.
	Input map[string]any `json:"input"`
}

// runOut is what an action answered.
type runOut struct {
	// Ok is whether the action ran to completion.
	Ok bool `json:"ok"`
	// Output is the action's result when it ran; its shape is the action's own.
	Output any `json:"output,omitempty"`
	// Error is why the action failed, when it did.
	Error string `json:"error,omitempty"`
}

// Run runs one action of a connector as the caller's org, with the credential
// the org connected, and answers what the action returned. The credential is
// read from the org's KMS namespace here and never leaves this service. An
// action that ran and failed is ok:false with the reason; an unknown connector
// is 404, an action that does not run on this deployment 422, and a connector
// the org has not connected 424.
//
// Example: {"provider":"bitly","action":"create_bitlink","input":{"long_url":"https://hanzo.ai"}}
// Response: {"ok":true,"output":{"link":"https://bit.ly/3xyz"}}
func (o ops) run(ctx context.Context, in *runIn) (*runOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Action) == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "action is required")
	}
	out, err := runPiece(ctx, o.s, org, strings.TrimSpace(in.Provider), in.Action, in.Input)
	if err != nil {
		var ze *zip.HTTPError
		if errors.As(err, &ze) {
			return nil, err
		}
		return &runOut{Error: err.Error()}, nil
	}
	return &runOut{Ok: true, Output: out}, nil
}

// ── logos ────────────────────────────────────────────────────────────────────

// logo serves a connector's logo from the catalog. PUBLIC: an <img> carries no
// bearer, and a logo is not the org's. It is served from the embedded catalog
// only, never fetched, with a policy that runs nothing — an SVG opened directly
// is a picture, not a page.
func logo(_ *cloud.Service[state], c *zip.Ctx) error {
	set := installed()
	if set == nil || set.Logo == nil {
		return c.Bytes(http.StatusNotFound, nil)
	}
	b, kind, ok := set.Logo(providerParam(c))
	if !ok {
		return c.Bytes(http.StatusNotFound, nil)
	}
	c.SetHeader("Content-Type", kind)
	c.SetHeader("X-Content-Type-Options", "nosniff")
	c.SetHeader("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	c.SetHeader("Cache-Control", "public, max-age=86400")
	return c.Bytes(http.StatusOK, b)
}

// logoPath is where a connector's logo is served, "" when it has none.
func logoPath(p *Provider) string {
	if p.Piece == nil || p.Piece.Logo == "" {
		return ""
	}
	return "/v1/provider/" + p.ID + "/logo"
}

// ── presentation ─────────────────────────────────────────────────────────────

var categoryNames = map[string]string{
	"ARTIFICIAL_INTELLIGENCE": "AI",
	"UNIVERSAL_AI":            "AI",
	"PRODUCTIVITY":            "Productivity",
	"MARKETING":               "Marketing",
	"COMMUNICATION":           "Communication",
	"SALES_AND_CRM":           "Sales",
	"DEVELOPER_TOOLS":         "Developer",
	"CONTENT_AND_FILES":       "Content",
	"BUSINESS_INTELLIGENCE":   "Analytics",
	"CORE":                    "Core",
	"FLOW_CONTROL":            "Core",
	"COMMERCE":                "Commerce",
	"FORMS_AND_SURVEYS":       "Forms",
	"ACCOUNTING":              "Finance",
	"PAYMENT_PROCESSING":      "Payments",
	"CUSTOMER_SUPPORT":        "Support",
	"HUMAN_RESOURCES":         "HR",
}

// pieceCategory is the card's group: the piece's first category, in the
// console's words.
func pieceCategory(pc *piece.Piece) string {
	for _, c := range pc.Categories {
		if n, ok := categoryNames[c]; ok {
			return n
		}
	}
	if pc.Core {
		return "Core"
	}
	return "Other"
}

// pieceNote says why a connector cannot be connected here, when it cannot.
func pieceNote(p *Provider) string {
	if !p.generated || p.Kind != noneKind || len(p.Piece.Auth) == 0 {
		return ""
	}
	for _, a := range p.Piece.Auth {
		if a.Type == piece.OIDC {
			return "Signs in with OIDC, which this deployment does not connect yet."
		}
	}
	return "Signs in with an OAuth app of the account's own, which this deployment does not register."
}

// pieceProps is a piece's input declaration as the tool plane's props.
func pieceProps(in []piece.Prop) []PropSpec {
	out := make([]PropSpec, 0, len(in))
	for _, pr := range in {
		if !pr.Input() {
			continue
		}
		out = append(out, PropSpec{
			Name:        pr.Name,
			DisplayName: pr.DisplayName,
			Type:        propKind(pr.Type),
			Required:    pr.Required,
			Description: pr.Description,
		})
	}
	return out
}

// propKind maps a piece PropertyType onto the JSON type a caller sends.
func propKind(t string) string {
	switch t {
	case "NUMBER":
		return "number"
	case "CHECKBOX":
		return "boolean"
	case "ARRAY", "MULTI_SELECT_DROPDOWN", "STATIC_MULTI_SELECT_DROPDOWN":
		return "array"
	case "JSON", "OBJECT", "DYNAMIC", "CUSTOM", "DATE_RANGE":
		return "object"
	}
	return "string"
}

// opNames lists a connector's actions or triggers for its card: the piece's
// own declaration when there is one, else what the registry holds.
func opNames(p *Provider, triggers bool) []opView {
	var out []opView
	switch {
	case p.Piece != nil && triggers:
		for _, op := range p.Piece.Triggers {
			out = append(out, opView{Name: op.Name, DisplayName: op.DisplayName})
		}
	case p.Piece != nil:
		for _, op := range p.Piece.Actions {
			out = append(out, opView{Name: op.Name, DisplayName: op.DisplayName})
		}
	case triggers:
		for _, t := range p.Triggers {
			out = append(out, opView{Name: t.Name, DisplayName: t.DisplayName})
		}
	default:
		for _, a := range p.Actions {
			out = append(out, opView{Name: a.Name, DisplayName: a.DisplayName})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ── the catalogue a flow composes ────────────────────────────────────────────

// Connectors is every connector a flow can compose — each one the catalog
// carries, and each one with actions of its own — sorted by id.
func Connectors() []*Provider {
	withPieces()
	out := make([]*Provider, 0, len(registry))
	for _, p := range registry {
		if p.Piece != nil || len(p.Actions) > 0 {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AuthKind is how a connector is credentialed, in the catalogue's words:
// "none", "oauth2", "api_key", or the piece's own auth type ("custom_auth").
func AuthKind(p *Provider) (kind string, required bool) {
	if p.generated && len(p.Piece.Auth) > 0 {
		a := p.Piece.Auth[0]
		return a.Type, a.Required
	}
	switch {
	case p.Authorize != nil || p.Adopt != nil:
		return "oauth2", true
	case p.Verify != nil:
		return "api_key", true
	}
	return "none", false
}

// Logo is where a connector's logo is served, "" when it has none.
func Logo(p *Provider) string { return logoPath(p) }

// PieceProps is a piece's input declaration as action props — the one mapping
// from a piece PropertyType to the JSON type a caller sends.
func PieceProps(in []piece.Prop) []PropSpec { return pieceProps(in) }

// opDetail is opNames with each operation's description and inputs.
func opDetail(p *Provider, triggers bool) []opView {
	var out []opView
	switch {
	case p.Piece != nil:
		ops := p.Piece.Actions
		if triggers {
			ops = p.Piece.Triggers
		}
		for _, op := range ops {
			out = append(out, opView{Name: op.Name, DisplayName: op.DisplayName, Description: op.Description, Props: pieceProps(op.Props)})
		}
	case triggers:
		for _, t := range p.Triggers {
			out = append(out, opView{Name: t.Name, DisplayName: t.DisplayName, Description: t.Description, Props: t.Props})
		}
	default:
		for _, a := range p.Actions {
			out = append(out, opView{Name: a.Name, DisplayName: a.DisplayName, Description: a.Description, Props: a.Props})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
