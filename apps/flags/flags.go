package flags

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// Type is the switch kind the cockpit renders (and how the value is decoded).
type Type string

const (
	TypeBool   Type = "bool"
	TypeInt    Type = "int"
	TypeString Type = "string"
)

// Def is ONE platform switch: a flag key qualified by the metadata the cockpit shows
// and the env var that provides the fallback default. This table is the ONE place the
// platform switches are named; the embedded engine evaluates them.
type Def struct {
	Key      string // the flag key (snake_case)
	Category string // Access | Launch | Signup | Subsystems | Gateway | Network
	Label    string
	Desc     string
	Type     Type
	Env      string // env var supplying the fallback default (may be "")
	Default  string // literal fallback when neither the store nor env has a value
	ReadOnly bool   // surfaced read-only (boot-time activation, network ids)
	// Principal marks a switch decided per CALLER — its validated org and user —
	// rather than once for the deployment (access.go). It is a bool, has no env
	// fallback and defaults off, and it is read with [On], never [Bool].
	Principal bool
}

// ── registry (open + extensible) ────────────────────────────────────────────────

var (
	regMu sync.RWMutex
	defs  []Def
	index = map[string]int{}
)

// Register adds a switch to the platform-flag registry. Any subsystem may call it (in
// its init) to surface its own flag in the cockpit — the registry is open, not
// hardcoded. A duplicate key REPLACES the prior def (last registration wins).
//
// A principal switch that could be on with nothing stored is a programming error
// and panics: its only source is a SuperAdmin's definition.
func Register(d Def) {
	if d.Principal && (d.Type != TypeBool || d.Env != "" || d.Default != "false") {
		panic("flags: principal switch " + d.Key + " must be a bool with no env and default false")
	}
	regMu.Lock()
	defer regMu.Unlock()
	if i, ok := index[d.Key]; ok {
		defs[i] = d
		return
	}
	index[d.Key] = len(defs)
	defs = append(defs, d)
}

// Defs returns a snapshot of the registered switches in registration order.
func Defs() []Def {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Def, len(defs))
	copy(out, defs)
	return out
}

func lookupDef(key string) (Def, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	if i, ok := index[key]; ok {
		return defs[i], true
	}
	return Def{}, false
}

// ── evaluation client ──────────────────────────────────────────────────────────

// snapshot is one cached evaluation of the platform project's flags.
type snapshot struct {
	flags    map[string]json.RawMessage // featureFlags[key]  (bool | variant string)
	payloads map[string]json.RawMessage // featureFlagPayloads[key] (arbitrary JSON)
	at       time.Time
	ok       bool
}

// Client is the in-process evaluation client: per-(org, project) SQLite definition
// stores + the embedded native evaluator, with the platform project's evaluation
// cached for one TTL (the hot-apply bound).
type Client struct {
	stores     *cloud.OrgStore[*Store]
	distinctID string
	ttl        time.Duration
	// trail is the platform's audit trail, which a principal switch's write
	// lands on first (access.go record).
	trail *audit.Recorder

	mu   sync.RWMutex
	snap snapshot
}

var mounted *Client

func (c *Client) configured() bool { return c != nil && c.stores != nil }

// storeFor is the ONE way this package reaches a definition store: it names the
// database through orgns.OrgNamespace — the single check a validated org passes
// through — and asks the registry for that name. Nothing else in flags resolves
// a store, so "which file does this evaluation read" has exactly one answer and
// it is derived from exactly one input.
//
// org and project MUST already be validated: the caller's own principal for a
// request, or a server-side resolution an in-process composer states as its
// contract.
//
// It NEVER answers the deployment's own org. The platform switches live at
// OrgNamespace(orgns.Reserved, orgns.Reserved), and a tenant IAM let someone
// name "platform", acting in a project "platform", would otherwise read and
// write them as its own — through /v1/flag, an experiment, any composer. The
// platform store is reached through [Client.platform] alone.
func (c *Client) storeFor(org, project string) (*Store, error) {
	if org == principal.Reserved {
		return nil, zip.ErrForbidden("flags: the org " + principal.Reserved + " is the deployment's own")
	}
	return c.open(org, project)
}

// platform is the reserved store the platform switches live in.
func (c *Client) platform() (*Store, error) { return c.open(principal.Reserved, principal.Reserved) }

func (c *Client) open(org, project string) (*Store, error) {
	if !c.configured() {
		return nil, fmt.Errorf("flags: engine not configured")
	}
	ns, err := principal.OrgNamespace(org, project)
	if err != nil {
		return nil, err
	}
	return c.stores.For(ns)
}

// defined reports whether org has a definition store in its default project,
// without opening or creating one.
func (c *Client) defined(org string) (bool, error) {
	if !c.configured() {
		return false, fmt.Errorf("flags: engine not configured")
	}
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return false, err
	}
	return c.stores.Has(ns), nil
}

// evaluateProject runs the engine over one (org, project) store for one
// evaluation context. The definitions read is a local SQLite scan; the evaluation
// is a pure in-memory function call.
func (c *Client) evaluateProject(org, project string, ctx []byte) (json.RawMessage, error) {
	st, err := c.storeFor(org, project)
	if err != nil {
		return nil, err
	}
	return evaluate(st, ctx)
}

// evaluate runs the engine over every definition in st for one context.
func evaluate(st *Store, ctx []byte) (json.RawMessage, error) {
	defsJSON, n, err := st.DefsJSON()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return json.RawMessage(`{"featureFlags":{},"featureFlagPayloads":{},"errorsWhileComputingFlags":false}`), nil
	}
	return engineEvaluate(defsJSON, ctx)
}

// resolve returns a switch's live string value and its source. A stored platform flag
// wins when it has a value; else the env fallback; else the literal default. Nil-safe.
//
// A principal switch has no deployment-wide value: evaluating it here would answer
// for the deployment's own distinct id, a caller nobody is. It resolves to its
// default, off, and is read per caller with [On].
func (c *Client) resolve(def Def) (value string, source string) {
	if def.Principal {
		return def.Default, "default"
	}
	if c.configured() {
		fv, pv, present := c.lookup(def.Key)
		if present {
			switch def.Type {
			case TypeBool:
				if b, ok := asBool(pv); ok {
					return strconv.FormatBool(b), "flags"
				}
				if b, ok := asBool(fv); ok {
					return strconv.FormatBool(b), "flags"
				}
			case TypeInt:
				if n, ok := asInt(pv); ok {
					return strconv.Itoa(n), "flags"
				}
				if n, ok := asInt(fv); ok {
					return strconv.Itoa(n), "flags"
				}
			case TypeString:
				if s, ok := asString(pv); ok && s != "" {
					return s, "flags"
				}
				if s, ok := asString(fv); ok && s != "" {
					return s, "flags"
				}
			}
		}
	}
	if def.Env != "" {
		if ev := env.Or(def.Env, ""); ev != "" {
			return ev, "env"
		}
	}
	return def.Default, "default"
}

func (c *Client) lookup(key string) (fv, pv json.RawMessage, present bool) {
	c.ensureFresh()
	c.mu.RLock()
	defer c.mu.RUnlock()
	var inFlags, inPayloads bool
	fv, inFlags = c.snap.flags[key]
	pv, inPayloads = c.snap.payloads[key]
	return fv, pv, inFlags || inPayloads
}

// ensureFresh re-evaluates the platform project when the snapshot is older than the
// TTL. On an evaluation error the previous snapshot is kept (graceful degradation)
// and the clock is stamped so a persistent failure is retried at most once per TTL.
func (c *Client) ensureFresh() {
	c.mu.RLock()
	fresh := c.snap.ok && time.Since(c.snap.at) < c.ttl
	c.mu.RUnlock()
	if fresh {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snap.ok && time.Since(c.snap.at) < c.ttl {
		return
	}
	ctx, _ := json.Marshal(map[string]string{"distinct_id": c.distinctID})
	st, err := c.platform()
	if err != nil {
		c.snap.at = time.Now() // keep last values; bound retry to one per TTL
		return
	}
	res, err := evaluate(st, ctx)
	if err != nil {
		c.snap.at = time.Now() // keep last values; bound retry to one per TTL
		return
	}
	var out struct {
		FeatureFlags        map[string]json.RawMessage `json:"featureFlags"`
		FeatureFlagPayloads map[string]json.RawMessage `json:"featureFlagPayloads"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		c.snap.at = time.Now()
		return
	}
	if out.FeatureFlags == nil {
		out.FeatureFlags = map[string]json.RawMessage{}
	}
	if out.FeatureFlagPayloads == nil {
		out.FeatureFlagPayloads = map[string]json.RawMessage{}
	}
	c.snap = snapshot{flags: out.FeatureFlags, payloads: out.FeatureFlagPayloads, at: time.Now(), ok: true}
}

// invalidate drops the cached platform snapshot so the next read re-evaluates
// (a cockpit write applies immediately in this pod; peers converge within TTL).
func (c *Client) invalidate() {
	c.mu.Lock()
	c.snap = snapshot{}
	c.mu.Unlock()
}

// SetPlatformSwitch stores/overwrites a platform switch's definition (the admin
// cockpit's ONE write path) and applies it immediately in this pod. A principal
// switch takes the cockpit's targeting shape and nothing wider (targetable).
//
// The principal switch's definition is stored as targetable re-encodes it, never
// as sent: the engine evaluates exactly the document that was checked.
func SetPlatformSwitch(key string, definition json.RawMessage, actor string) error {
	if d, ok := lookupDef(key); ok && d.Principal {
		canon, err := targetable(definition)
		if err != nil {
			return fmt.Errorf("flags: %s: %w", key, err)
		}
		definition = canon
	}
	st, err := mounted.platform()
	if err != nil {
		return err
	}
	if err := st.Upsert(key, definition, actor); err != nil {
		return err
	}
	mounted.invalidate()
	return nil
}

// ── value parsers (tolerant of PostHog bool/variant/payload shapes) ─────────────

func asBool(raw json.RawMessage) (bool, bool) {
	t := strings.TrimSpace(string(raw))
	if t == "true" {
		return true, true
	}
	if t == "false" {
		return false, true
	}
	if t == "" || t == "null" {
		return false, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "1", "on", "yes":
			return true, true
		case "false", "0", "off", "no":
			return false, true
		case "":
			return false, false
		default:
			return true, true // enabled with a variant
		}
	}
	return false, false
}

func asInt(raw json.RawMessage) (int, bool) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
		if f, err := n.Float64(); err == nil {
			return int(f), true
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if i, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return i, true
		}
	}
	return 0, false
}

func asString(raw json.RawMessage) (string, bool) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	return t, true
}

// ── typed live accessors (the in-process evaluation client) ───────────────────────

// Bool returns the live boolean value of a registered switch (flags -> env -> default).
func Bool(key string) bool {
	def, ok := lookupDef(key)
	if !ok {
		return false
	}
	v, _ := mounted.resolve(def)
	b, _ := strconv.ParseBool(strings.TrimSpace(v))
	return b
}

// Int returns the live integer value of a registered switch.
func Int(key string) int {
	def, ok := lookupDef(key)
	if !ok {
		return 0
	}
	v, _ := mounted.resolve(def)
	n, _ := strconv.Atoi(strings.TrimSpace(v))
	return n
}

// String returns the live string value of a registered switch.
func String(key string) string {
	def, ok := lookupDef(key)
	if !ok {
		return ""
	}
	v, _ := mounted.resolve(def)
	return v
}

// ── admin control-plane board ──────────────────────────────────────────────────

// SwitchView is one platform switch as the admin cockpit renders it: the live value +
// where it came from (flags | env | default).
type SwitchView struct {
	// Key is the registry key, which is also the flag key the engine evaluates and
	// the path segment a write addresses: snake_case for the seeded platform set
	// (waitlist_open, public_signup), waitlist.<service> for a launch gate.
	Key string `json:"key"`
	// Category is the board's grouping: Access, Launch, Signup, Subsystems,
	// Gateway or Network. Any subsystem may register a switch with a category of
	// its own, so treat it as a label to group by, not a fixed set.
	Category string `json:"category"`
	// Label is the switch's title on the board.
	Label string `json:"label"`
	// Description is what flipping this switch does, written by whoever registered
	// it — including the preconditions for flipping it, where there are any.
	Description string `json:"description"`
	// Type is how Value is to be read back: bool, int or string.
	Type string `json:"type"`
	// Value is the value IN FORCE right now, always as a string whatever Type says
	// — "true"/"false" for a bool, decimal digits for an int. It is what the
	// running deployment is using, not what is stored: an unstored switch reports
	// its env or literal fallback here.
	Value string `json:"value"`
	// Source is where Value came from, in the order they are tried: "flags" (a
	// stored definition — the only one a cockpit write produces), "env" (the Env
	// var below), or "default" (the registered literal). Anything but "flags"
	// means nothing has ever been written for this key.
	Source string `json:"source"`
	// Env is the environment variable that supplies the fallback. Present only on
	// the boot-time switches, where env IS the mechanism; a runtime switch carries
	// none on purpose, so that this surface is the one place its value comes from.
	Env string `json:"env,omitempty"`
	// ReadOnly marks a switch the process reads once at BOOT (subsystem activation,
	// the network ids). Writing it is not refused and the stored value is real, but
	// the running process keeps its boot value until it is restarted — so a flip
	// here is a request for a redeploy rather than a hot change.
	ReadOnly bool `json:"readOnly"`
	// Principal marks a switch decided per caller — against the caller's validated
	// org and user — rather than once for the deployment. Its Value is then whether
	// a stored definition is in force at all ("true" when one is stored and active);
	// who it is on for is in Definition, and a caller reads its own answer at
	// GET /v1/flag/platform.
	Principal bool `json:"principal"`
	// Definition is the stored definition of a principal switch — the engine's
	// document, kept byte-for-byte: its targeting groups (rules on the "org" and
	// "user" properties, a rollout percentage) say who the switch is on for.
	// Absent when nothing is stored, which is off for everyone, and on every switch
	// that is not a principal one.
	Definition json.RawMessage `json:"definition,omitempty"`
	// Users is who a principal switch's rules name by IAM id, read off
	// Definition: the people a grant let in. Absent on every other switch.
	Users []string `json:"users,omitempty"`
	// Orgs is the orgs a principal switch's rules name, read off Definition.
	Orgs []string `json:"orgs,omitempty"`
}

// BoardView is the full control-plane read board: the engine status plus every
// switch's live value. Definitions are edited in place over /v1/flag (the cockpit
// writes through SetPlatformSwitch); the activity log is the native change audit.
type BoardView struct {
	// Engine names the evaluator serving these values: "hanzo-flags", running
	// in-process in this pod.
	Engine string `json:"engine"`
	// Configured is whether the definition stores opened. When false every switch
	// below is resolving from env or its registered default — the values are still
	// the ones in force, but nothing stored can be reaching them and a write will
	// fail. That is the fail-safe posture, not an outage.
	Configured bool `json:"configured"`
	// ManageURL is where the definitions behind these switches are read and
	// written: /v1/flag/defs.
	ManageURL string `json:"manageUrl"`
	// AuditURL is where the change log for those writes lives: /v1/flag/activity.
	AuditURL string `json:"auditUrl"`
	// Switches is every registered switch with the value in force, in registration
	// order — the seeded platform set first, then whatever each subsystem added at
	// init. The registry is open, so this is the deployment's switchboard rather
	// than a fixed list.
	Switches []SwitchView `json:"switches"`
}

// Board evaluates every registered switch live and returns the cockpit read board.
func Board() BoardView {
	list := Defs()
	sw := make([]SwitchView, 0, len(list))
	for _, d := range list {
		val, src := mounted.resolve(d)
		v := SwitchView{
			Key: d.Key, Category: d.Category, Label: d.Label, Description: d.Desc,
			Type: string(d.Type), Value: val, Source: src, Env: d.Env, ReadOnly: d.ReadOnly,
			Principal: d.Principal,
		}
		if d.Principal {
			v.Definition, v.Value, v.Source = mounted.targeting(d.Key)
			v.Users, v.Orgs = named(v.Definition)
		}
		sw = append(sw, v)
	}
	return BoardView{Engine: "hanzo-flags", Configured: mounted.configured(), ManageURL: "/v1/flag/defs", AuditURL: "/v1/flag/activity", Switches: sw}
}

// ── lifecycle ────────────────────────────────────────────────────────────────

type state struct {
	client *Client
	// audit is the platform trail a staged key's write lands on first (staged.go).
	audit *audit.Recorder
}

// Mount opens the per-org definition stores, installs the process-wide evaluation
// client, and registers the /v1/flag surface. A store that cannot be opened
// degrades every switch to env/default and the HTTP surface reports it — never an
// error at boot.
func Use(app cloud.Router, deps cloud.Deps) error {
	if cloud.DataDir() == "" {
		return fmt.Errorf("flags.Use:  empty cloud.DataDir()")
	}
	b := cloud.NewBase(deps, "flags")
	log := b.Log
	c := &Client{
		stores:     cloud.NewOrgStore[*Store](b, "flags", openStore),
		distinctID: env.Or("FLAGS_PLATFORM_DISTINCT_ID", "hanzo-platform:"+cmp.Or(strings.TrimSpace(cloud.Brand()), "hanzo")),
		ttl:        ttlFromEnv(),
		trail:      deps.Audit,
	}
	mounted = c
	// Install this engine as the process-wide switch reader so the EDGE can read a
	// platform switch. serve.go's middleware cannot import this package (flags
	// imports the root package, root imports routers), so the root package holds the
	// client and this is the one call that fills it.
	host.SetSwitchReader(Bool)
	c.carried(deps.Audit)
	svc := &cloud.Service[state]{Base: b, State: state{client: c, audit: deps.Audit}}
	routes(app, svc)
	exposeHold()
	exposeValue()
	exposeBoard()
	exposeOn()
	exposeGrant()
	log.Info("flags engine ready", "engine", "hanzo-flags", "ttlSeconds", int(c.ttl.Seconds()), "switches", len(Defs()))
	return nil
}

// Shutdown closes every open per-org definitions store.
func Shutdown(_ context.Context) error {
	if mounted == nil || mounted.stores == nil {
		return nil
	}
	return mounted.stores.CloseAll()
}

func ttlFromEnv() time.Duration {
	if v := env.Or("FLAGS_TTL_SECONDS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 15 * time.Second
}
