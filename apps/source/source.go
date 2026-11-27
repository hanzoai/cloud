package source

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

const (
	// interval is the heartbeat period the reply asks for, in seconds.
	interval = 30
	// level is what a source reports. Slice 1 has one level; usage is not here.
	level = "metadata"
	// online is how long after a heartbeat a source still reads online.
	online = 90 * time.Second
	// skew is how far a heartbeat's signed `at` may sit from the server clock.
	skew = 5 * time.Minute

	maxName    = 128
	maxText    = 64
	maxZap     = 512
	maxProv    = 32
	maxPlan    = 64
	appSubject = "app:"
)

type state struct {
	stores  *cloud.OrgStore[*Store]
	audit   *audit.Recorder
	kms     secret.Store
	now     func() time.Time
	compute func(ctx context.Context, org, subject string) ([]client.ComputeMachine, error)
}

type service = cloud.Service[state]

var mounted *service

func newService(deps cloud.Deps) *service {
	b := cloud.NewBase(deps, "source")
	return &service{Base: b, State: state{
		stores:  cloud.NewOrgStore[*Store](b, "sources", openStore),
		audit:   deps.Audit,
		kms:     deps.KMS,
		now:     time.Now,
		compute: defaultFetchCompute,
	}}
}

// Use mounts /v1/sources (backward-compatible alias for linked sources).
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("source.Use:  nil app")
	}
	if cloud.DataDir() == "" {
		return errors.New("source.Use:  empty DataDir")
	}
	s := newService(deps)
	mounted = s
	sourceRoutes(app, s)
	exposeOwns()
	s.Base.Log.Info("source mounted", "prefixes", []string{"/v1/sources"})
	return nil
}

// UseMachines mounts /v1/machines (canonical unified surface: linked personal
// machines + cloud compute VMs/GPUs).
func UseMachines(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("source.UseMachines:  nil app")
	}
	if cloud.DataDir() == "" {
		return errors.New("source.UseMachines:  empty DataDir")
	}
	s := newService(deps)
	mounted = s
	machineRoutes(app, s)
	exposeOwns()
	s.Base.Log.Info("machines mounted", "prefixes", []string{"/v1/machines"})
	return nil
}

// Shutdown closes every org's store. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
	mounted = nil
	return err
}

// Owns is nil when subject, in org, owns the source id and it is not revoked. It is the
// ownership rule the registry itself applies, for a caller that serves a source's
// session: another person's source and an absent one are the same answer.
func Owns(ctx context.Context, org, subject, id string) error {
	if mounted == nil {
		return errors.New("source: not mounted")
	}
	sto, err := mounted.State.stores.For(namespaceOf(org))
	if err != nil {
		return err
	}
	r, err := sto.Get(ctx, subject, id)
	if err != nil {
		return err
	}
	if r.RevokedAt != 0 {
		return errors.New("source revoked")
	}
	return nil
}

// zipdoc lifts each typed op's doc comment into zipdoc_gen.go, the only way that
// prose reaches the published document and the tool list.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

func routes(app cloud.Router, s *service) {
	machineRoutes(app, s)
	sourceRoutes(app, s)
}

func machineRoutes(app cloud.Router, s *service) {
	// Bridge parks the validated org and subject on the context a typed op gets,
	// ahead of every leaf.
	o := ops{s: s}

	// Canonical unified surface: /v1/machines (linked personal machines + cloud compute).
	m := app.Group("/v1/machines")
	m.Use(cloud.Bridge())
	ma := app.Group("/v1/machines/accounts")
	ma.Use(cloud.Bridge())
	ma.Get("", o.accounts)
	ma.Post("", o.createAccount, zip.WithStatus(http.StatusCreated))
	ma.Delete("/:id", o.deleteAccount)

	m.Post("", o.link)
	m.Get("", o.listMachines)
	m.Get("/usage", o.usage)
	m.Get("/:id", o.getMachine)
	m.Delete("/:id", o.revoke)
	m.Post("/:id/heartbeat", o.heartbeat)
	m.Post("/:id/dispatch", o.dispatch)
	m.Delete("/:id/dispatch/:lease", o.release)
}

func sourceRoutes(app cloud.Router, s *service) {
	o := ops{s: s}

	// Backward-compatible alias: /v1/sources.
	g := app.Group("/v1/sources")
	g.Use(cloud.Bridge())
	// Provider accounts: keys sealed into KMS and the logins sources report. Declared
	// ahead of /:id so `accounts` is never read as an id.
	a := app.Group("/v1/sources/accounts")
	a.Use(cloud.Bridge())
	a.Get("", o.accounts)
	a.Post("", o.createAccount, zip.WithStatus(http.StatusCreated))
	a.Delete("/:id", o.deleteAccount)

	g.Post("", o.link)
	g.Get("", o.list)
	g.Get("/usage", o.usage)
	g.Get("/:id", o.get)
	g.Delete("/:id", o.revoke)
	g.Post("/:id/heartbeat", o.heartbeat)
	g.Post("/:id/dispatch", o.dispatch)
	g.Delete("/:id/dispatch/:lease", o.release)
	// The usage report is raw: its signature covers the exact body bytes and rides
	// in a header, which a typed op cannot see.
	g.Raw(http.MethodPost, "/:id/usage", o.report)
}

// ops is the receiver the source.v1 ops hang off.
type ops struct{ s *service }

// who is the validated org and person behind the call, from the token only. A
// machine token (an application, or any credential naming no stable person) has
// no sources to own and is refused.
func who(ctx context.Context) (org, subject string, err error) {
	org, err = principal.Acting(ctx)
	if err != nil {
		return "", "", err
	}
	subject, ok := principal.SubjectFrom(ctx)
	subject = strings.TrimSpace(subject)
	if !ok || subject == "" || strings.HasPrefix(subject, appSubject) {
		return "", "", zip.ErrForbidden("sources belong to a person: a machine token cannot link or read one")
	}
	return org, subject, nil
}

func (o ops) store(ctx context.Context) (*Store, string, string, error) {
	org, subject, err := who(ctx)
	if err != nil {
		return nil, "", "", err
	}
	sto, err := o.open(org)
	return sto, org, subject, err
}

func (o ops) open(org string) (*Store, error) {
	sto, err := o.s.State.stores.For(namespaceOf(org))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	return sto, nil
}

// whoReq is who for a raw route, which has the request and no typed context: the
// same two facts from the same boundary, read off the request.
func whoReq(c *zip.Ctx) (org, subject string, err error) {
	org, ok := principal.Org(c)
	if !ok {
		return "", "", principal.Refused(c)
	}
	if p, ran := principal.Minted(c); ran {
		subject, ok = principal.SubjectOf(p)
	} else {
		subject, ok = strings.TrimSpace(c.User()), true
	}
	subject = strings.TrimSpace(subject)
	if !ok || subject == "" || strings.HasPrefix(subject, appSubject) {
		return "", "", zip.ErrForbidden("sources belong to a person: a machine token cannot link or read one")
	}
	return org, subject, nil
}

// SourceProvider is the state of one provider instance on a source. It is state
// only: there is no token, email or key field.
type SourceProvider struct {
	// Provider is the provider's catalog name, for example `claude`.
	Provider string `json:"provider"`
	// Instance names this login or key on the source, for example `claude-1`.
	Instance string `json:"instance"`
	// Kind is `login` (a subscription session that stays on the source) or `key`.
	Kind string `json:"kind"`
	// State is `ready`, `needs_login` or `limited`.
	State string `json:"state"`
	// Plan is the plan label the provider reports, for example `max`. May be empty.
	Plan string `json:"plan,omitempty"`
	// Shared is the person's local choice to offer this login to their other surfaces
	// (default off). It is reported, never decided here.
	Shared bool `json:"shared"`
}

// Source is one linked Hanzo Dev install.
type Source struct {
	// ID is the source's id, `src_` and 32 hex digits.
	ID string `json:"id"`
	// Kind is `desktop` (an installed Dev app) or `browser` (the Hanzo extension).
	Kind string `json:"kind"`
	// Name is the label the person gave this install.
	Name string `json:"name"`
	// OS is the operating system the install reported.
	OS string `json:"os"`
	// Arch is the CPU architecture the install reported.
	Arch string `json:"arch"`
	// Version is the Dev app version, as of the last link or heartbeat.
	Version string `json:"version"`
	// State is `online` (a heartbeat within 90 seconds), `offline`, or `revoked`.
	State string `json:"state"`
	// LastSeen is the last accepted heartbeat, RFC 3339 UTC. Absent before the first.
	LastSeen string `json:"last_seen,omitempty"`
	// LinkedAt is when the source was linked, RFC 3339 UTC.
	LinkedAt string `json:"linked_at"`
	// Zap is the source's reachable ZAP endpoint. Absent when it has none, in
	// which case it is registered and not routable.
	Zap string `json:"zap,omitempty"`
	// Providers is the provider state from the last heartbeat.
	Providers []SourceProvider `json:"providers"`
}

func namespaceOf(org string) namespace.Namespace {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return namespace.Namespace{}
	}
	return ns
}

func stamp(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func (o ops) view(r row) Source {
	d := Source{
		ID: r.ID, Kind: r.Kind, Name: r.Name, OS: r.OS, Arch: r.Arch, Version: r.Version,
		LastSeen: stamp(r.LastSeen), LinkedAt: stamp(r.LinkedAt), Zap: r.Zap,
		Providers: []SourceProvider{}, State: "offline",
	}
	switch {
	case r.RevokedAt != 0:
		d.State = "revoked"
	case r.LastSeen != 0 && o.s.State.now().Sub(time.Unix(r.LastSeen, 0)) <= online:
		d.State = "online"
	}
	for _, p := range r.Providers {
		d.Providers = append(d.Providers, SourceProvider{p.Provider, p.Instance, p.Kind, p.State, p.Plan, p.Shared})
	}
	return d
}

// record appends one audit row for a privileged act on a source.
func (o ops) record(ctx context.Context, action, id string) {
	o.recordWith(ctx, action, id, "success", "", nil)
}

// recordWith is record with an outcome, a refusal's reason and the facts of the act
// (never a secret). The act has happened by then, so a failure to record is logged
// and never undoes it.
func (o ops) recordWith(ctx context.Context, action, id, result, reason string, after map[string]string) {
	if o.s.State.audit == nil {
		return
	}
	w := cloud.Who(ctx)
	status := http.StatusOK
	if result != "success" {
		status = http.StatusForbidden
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: w.Org, Sub: w.User, Email: w.Email},
		Action:   action,
		Resource: audit.Resource{Type: strings.SplitN(action, ".", 2)[0], ID: id},
		Auth:     audit.AuthContext{Method: "jwt", IsAdmin: false},
		Outcome:  audit.Outcome{Result: result, Status: status, Reason: reason},
	}
	if len(after) > 0 {
		rec.After, _ = json.Marshal(after)
	}
	if _, err := o.s.State.audit.Append(ctx, rec); err != nil {
		o.s.Log.Warn("source: audit not recorded", "action", action, "source", id, "err", err)
	}
}

// SourceLink is a request to link this install to the signed-in person.
type SourceLink struct {
	// Kind is `desktop` or `browser`. Required, and fixed once linked: relinking the
	// same key with another kind is a 409.
	Kind string `json:"kind"`
	// Name is the label to show for this install, up to 128 characters. Required.
	Name string `json:"name"`
	// OS is the operating system, for example `darwin`. Required, up to 64 characters.
	OS string `json:"os"`
	// Arch is the CPU architecture, for example `arm64`. Required, up to 64 characters.
	Arch string `json:"arch"`
	// Version is the Dev app version. Required, up to 64 characters.
	Version string `json:"version"`
	// Key is the install's own ed25519 public device key, 32 bytes as unpadded
	// base64url. Heartbeats are signed with its private half. Required.
	Key string `json:"key"`
	// Zap is the install's reachable ZAP endpoint, an https or wss URL up to 512
	// characters. Optional: without it the source is registered and not routable.
	Zap string `json:"zap"`
}

// SourceLinked is the answer to a link.
type SourceLinked struct {
	// ID is the source's id. Relinking the same device key returns the same id.
	ID string `json:"id"`
	// Interval is the heartbeat period in seconds.
	Interval int `json:"interval"`
	// Level is the usage level the person chose: `metadata`.
	Level string `json:"level"`
}

// SourceList is the caller's sources.
type SourceList struct {
	// Sources is every active source of the caller, newest heartbeat first.
	Sources []Source `json:"sources"`
}

// SourceRef addresses one source.
type SourceRef struct {
	// ID is the source, from the path.
	ID string `json:"id"`
}

// SourceRevoked acknowledges a revoke.
type SourceRevoked struct {
	// ID is the source that was revoked.
	ID string `json:"id"`
	// Revoked is true.
	Revoked bool `json:"revoked"`
}

// SourceBeat is one heartbeat.
type SourceBeat struct {
	// ID is the source, from the path.
	ID string `json:"id"`
	// At is when the source made the beat, RFC 3339. It must be within five
	// minutes of the server clock and newer than the last accepted beat.
	At string `json:"at"`
	// Version is the Dev app version now.
	Version string `json:"version"`
	// Zap is the source's reachable ZAP endpoint now. Empty clears it.
	Zap string `json:"zap"`
	// Providers is the state of each provider instance on the source, replacing
	// the last set. State only; at most 32.
	Providers []SourceProvider `json:"providers"`
	// Busy is how many runs the source is executing.
	Busy int `json:"busy"`
	// Sig is the ed25519 signature by the device key over the beat, unpadded base64url.
	// The signed message is `hanzo-source-heartbeat/1`, then one line each (LF
	// terminated) for id, at, version, zap, busy, and per provider in order
	// `provider,instance,kind,state,plan,shared` (shared as `true` or `false`).
	Sig string `json:"sig"`
}

// SourceBeatReply is the answer to a heartbeat.
type SourceBeatReply struct {
	// Interval is the heartbeat period in seconds.
	Interval int `json:"interval"`
	// Level is the usage level the person chose.
	Level string `json:"level"`
	// Revoked is false: a revoked source is answered 410 instead.
	Revoked bool `json:"revoked"`
}

func b64(s string, n int) ([]byte, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return b, err == nil && len(b) == n
}

func text(name, v string, max int, required bool) error {
	v = strings.TrimSpace(v)
	if required && v == "" {
		return zip.ErrBadRequest(name + " is required")
	}
	if len(v) > max {
		return zip.ErrBadRequest(name + " is too long")
	}
	return nil
}

func zapURL(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > maxZap {
		return zip.ErrBadRequest("zap is too long")
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "wss") || u.Host == "" || u.User != nil {
		return zip.ErrBadRequest("zap must be an https or wss URL")
	}
	return nil
}

// Link links this install to the signed-in person, or refreshes the install that
// already holds the same device key and answers with its existing id. The person
// and org come from the validated token and never from the body; a machine token
// is refused. A person holds at most 25 active sources and may link 10 new ones
// an hour.
//
// Example: {"kind": "desktop", "name": "Zach's MacBook", "os": "darwin", "arch": "arm64", "version": "1.8.4", "key": "dGhpcyBpcyBhIDMyIGJ5dGUgZGV2aWNlIGtleSEhISE", "zap": "https://source.example/zap"}
func (o ops) link(ctx context.Context, in *SourceLink) (*SourceLinked, error) {
	sto, org, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	if in.Kind != "desktop" && in.Kind != "browser" {
		return nil, zip.ErrBadRequest("kind must be desktop or browser")
	}
	for _, f := range []struct {
		n, v string
		max  int
	}{{"name", in.Name, maxName}, {"os", in.OS, maxText}, {"arch", in.Arch, maxText}, {"version", in.Version, maxText}} {
		if err := text(f.n, f.v, f.max, true); err != nil {
			return nil, err
		}
	}
	if _, ok := b64(in.Key, ed25519.PublicKeySize); !ok {
		return nil, zip.ErrBadRequest("key must be a 32 byte ed25519 public key, base64url")
	}
	if err := zapURL(in.Zap); err != nil {
		return nil, err
	}
	r, created, err := sto.Link(ctx, org, row{
		Subject: subject, Kind: in.Kind, Name: strings.TrimSpace(in.Name), OS: strings.TrimSpace(in.OS),
		Arch: strings.TrimSpace(in.Arch), Version: strings.TrimSpace(in.Version), Key: in.Key, Zap: in.Zap,
	}, mint.ID("src"), o.s.State.now())
	switch {
	case errors.Is(err, errKind):
		return nil, zip.Errorf(http.StatusConflict, "this key is already linked as another kind")
	case errors.Is(err, errCap):
		return nil, zip.Errorf(http.StatusConflict, "at most %d sources can be linked: revoke one first", maxActive)
	case errors.Is(err, errLinkRate):
		return nil, zip.Errorf(http.StatusTooManyRequests, "at most %d sources can be linked an hour", maxLinksPerHour)
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	if created {
		cloud.Created(ctx)
		o.record(ctx, "source.link", r.ID)
	}
	return &SourceLinked{ID: r.ID, Interval: interval, Level: level}, nil
}

// ListSources returns the signed-in person's own active sources, newest
// heartbeat first, each with its online state and provider state. There is no
// read of another person's sources: not for an org admin, not for a SuperAdmin.
func (o ops) list(ctx context.Context, _ *cloud.Unit) (*SourceList, error) {
	sto, _, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := sto.List(ctx, subject)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	out := &SourceList{Sources: []Source{}}
	for _, r := range rows {
		out.Sources = append(out.Sources, o.view(r))
	}
	return out, nil
}

// GetSource returns one of the signed-in person's own sources, including a
// revoked one, whose state reads `revoked`. Another person's id and an unknown id
// answer the same 404.
//
// Example: {"id": "src_0123456789abcdef0123456789abcdef"}
func (o ops) get(ctx context.Context, in *SourceRef) (*Source, error) {
	sto, _, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	r, err := sto.Get(ctx, subject, in.ID)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("source not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	d := o.view(r)
	return &d, nil
}

// RevokeSource unlinks one of the signed-in person's own sources and drops its
// provider state, so nothing routes through it. The act is audited. The source's
// next heartbeat answers 410 and it forgets the link. Revoking twice is the same
// answer as once.
//
// Example: {"id": "src_0123456789abcdef0123456789abcdef"}
func (o ops) revoke(ctx context.Context, in *SourceRef) (*SourceRevoked, error) {
	sto, _, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	r, err := sto.Revoke(ctx, subject, in.ID, o.s.State.now())
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("source not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	o.record(ctx, "source.revoke", r.ID)
	return &SourceRevoked{ID: r.ID, Revoked: true}, nil
}

// beatMessage is the exact byte string a heartbeat's signature covers.
func beatMessage(id string, b *SourceBeat) []byte {
	var m strings.Builder
	m.WriteString("hanzo-source-heartbeat/1\n")
	for _, v := range []string{id, b.At, b.Version, b.Zap, strconv.Itoa(b.Busy)} {
		m.WriteString(v + "\n")
	}
	for _, p := range b.Providers {
		m.WriteString(strings.Join([]string{p.Provider, p.Instance, p.Kind, p.State, p.Plan, strconv.FormatBool(p.Shared)}, ",") + "\n")
	}
	return []byte(m.String())
}

func providerState(ps []SourceProvider) ([]prov, error) {
	if len(ps) > maxProv {
		return nil, zip.ErrBadRequest("too many providers")
	}
	seen := map[string]bool{}
	out := make([]prov, 0, len(ps))
	for _, p := range ps {
		if err := text("provider", p.Provider, maxText, true); err != nil {
			return nil, err
		}
		if err := text("instance", p.Instance, maxText, true); err != nil {
			return nil, err
		}
		if err := text("plan", p.Plan, maxPlan, false); err != nil {
			return nil, err
		}
		if p.Kind != "login" && p.Kind != "key" {
			return nil, zip.ErrBadRequest("provider kind must be login or key")
		}
		if p.State != "ready" && p.State != "needs_login" && p.State != "limited" {
			return nil, zip.ErrBadRequest("provider state must be ready, needs_login or limited")
		}
		if seen[p.Instance] {
			return nil, zip.ErrBadRequest("provider instance repeated")
		}
		seen[p.Instance] = true
		out = append(out, prov{p.Provider, p.Instance, p.Kind, p.State, p.Plan, p.Shared})
	}
	return out, nil
}

// Heartbeat records that a source is alive and what its providers can do, and
// answers the interval and usage level. The beat is signed by the source's device
// key, so a stolen token alone cannot impersonate it: a bad or missing signature is
// 401. A revoked source is answered 410 and should forget the link. A source is
// online for 90 seconds after a beat; beats closer than 10 seconds are refused 429.
// Providers are state only and carry no token, email or key material.
//
// Example: {"id": "src_0123456789abcdef0123456789abcdef", "at": "2026-11-20T10:00:00Z", "version": "1.8.4", "zap": "", "providers": [{"provider": "claude", "instance": "claude-1", "kind": "login", "state": "ready", "plan": "max"}], "busy": 0, "sig": "c2lnbmF0dXJl"}
func (o ops) heartbeat(ctx context.Context, in *SourceBeat) (*SourceBeatReply, error) {
	sto, _, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	r, err := sto.Get(ctx, subject, in.ID)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("source not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	if r.RevokedAt != 0 {
		return nil, zip.Errorf(http.StatusGone, "source revoked")
	}
	pub, _ := b64(r.Key, ed25519.PublicKeySize)
	sig, ok := b64(in.Sig, ed25519.SignatureSize)
	if !ok || !ed25519.Verify(pub, beatMessage(in.ID, in), sig) {
		return nil, zip.ErrUnauthorized("heartbeat signature invalid")
	}
	now := o.s.State.now()
	at, err := time.Parse(time.RFC3339, in.At)
	if err != nil {
		return nil, zip.ErrBadRequest("at must be RFC 3339")
	}
	if d := now.Sub(at); d > skew || d < -skew {
		return nil, zip.ErrBadRequest("at is too far from the server clock")
	}
	if err := text("version", in.Version, maxText, true); err != nil {
		return nil, err
	}
	if err := zapURL(in.Zap); err != nil {
		return nil, err
	}
	ps, err := providerState(in.Providers)
	if err != nil {
		return nil, err
	}
	err = sto.Beat(ctx, subject, in.ID, beat{At: at.Unix(), Version: strings.TrimSpace(in.Version), Zap: in.Zap, Providers: ps}, now)
	switch {
	case errors.Is(err, errRevoked):
		return nil, zip.Errorf(http.StatusGone, "source revoked")
	case errors.Is(err, errTooSoon):
		return nil, zip.Errorf(http.StatusTooManyRequests, "heartbeat too soon")
	case errors.Is(err, errStale):
		return nil, zip.Errorf(http.StatusConflict, "heartbeat is not newer than the last")
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	return &SourceBeatReply{Interval: interval, Level: level, Revoked: false}, nil
}
