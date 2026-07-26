// Package flags is cloud's NATIVE feature-flag engine: definitions live in
// per-(org, project) SQLite (cloud.OrgDB — {DataDir}/orgs/{org}/projects/{project}/
// flags.db, encrypted at rest via cek) and evaluation runs in-process through the
// embedded hanzo-flags Rust evaluator (native/flags, FFI) with PostHog-compatible
// semantics: rollout hash, full property-operator set, variants, payloads. Stateless
// and scalable by construction — no KV, no network hop, every pod evaluates from its
// own hot in-memory copy of the definitions.
//
// TWO surfaces, ONE engine:
//
//   - /v1/flags — the product API (org-scoped via the gateway principal): evaluate
//     flags for a distinct_id + properties, manage definitions, read the activity
//     log. PostHog-shaped responses so existing SDK consumers port 1:1.
//
//   - the PLATFORM switches — the launch/ops knobs the SuperAdmin flips from
//     admin.hanzo.ai (registry below). They evaluate from the reserved
//     platform/platform store through the same engine. A switch with no stored
//     definition falls back env -> literal default — exactly the pre-engine
//     behavior, zero regression; the first cockpit write creates the definition
//     and takes effect within one cache TTL (default 15s), no redeploy.
//
// FAIL-SAFE. When the native engine is absent (!cgo) or a store read fails,
// evaluation degrades to env fallback -> literal default and the HTTP surface says
// so honestly — never fail-wrong.
//
// ── THE POLICY PRIMITIVE ─────────────────────────────────────────────────────────
//
// This engine IS Hanzo's runtime decision primitive: (Principal, context) -> verdict,
// evaluated in-process, stateless, hot. It knows ONLY flags — definitions, evaluation,
// and the platform-switch registry. It has ZERO knowledge of any specific policy that
// rides on it: no host→service map, no waitlist, no service registry. Those COMPOSE
// this engine from the OUTSIDE, one-way (they import flags; flags imports none of them):
//
//   - the launch waitlist gate (clients/admission) — a service's mode IS the switch
//     waitlist.<svc>; admission owns the host→service registry + Enforce and reads the
//     mode through flags.Bool. It USED to live in this package; extracting it is the
//     PROOF the engine composes its tenants rather than absorbing them.
//   - authz        (access policy)          — (Principal, resource+action) -> allow/deny
//   - entitlements (product-access policy)   — (Principal, feature/plan)    -> granted/denied
//
// Every one is (Principal, context) -> verdict. Folding them onto this evaluator makes
// Policy ONE composable primitive with one audit log and one hot-apply path. authz and
// entitlements are NOT built on it yet — this note only names the target so the seam
// stays visible; the launch waitlist (now external) is the first, proven composition.
package flags

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Type is the switch kind the cockpit renders (and how the value is decoded).
type Type string

const (
	TypeBool   Type = "bool"
	TypeInt    Type = "int"
	TypeString Type = "string"
)

// The reserved store the platform switches evaluate from.
const (
	platformOrg     = "platform"
	platformProject = "platform"
)

// Def is ONE platform switch: a flag key qualified by the metadata the cockpit shows
// and the env var that provides the fallback default. This table is the ONE place the
// platform switches are named; the embedded engine evaluates them.
type Def struct {
	Key      string // the flag key (snake_case)
	Category string // Launch | Signup | Subsystems | Gateway | Network
	Label    string
	Desc     string
	Type     Type
	Env      string // env var supplying the fallback default (may be "")
	Default  string // literal fallback when neither the store nor env has a value
	ReadOnly bool   // surfaced read-only (boot-time activation, network ids)
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
func Register(d Def) {
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

// Client is the in-process evaluation seam: per-(org, project) SQLite definition
// stores + the embedded native evaluator, with the platform project's evaluation
// cached for one TTL (the hot-apply bound).
type Client struct {
	stores     *cloud.OrgStore[*Store]
	distinctID string
	ttl        time.Duration

	mu   sync.RWMutex
	snap snapshot
}

var mounted *Client

func (c *Client) configured() bool { return c != nil && c.stores != nil && engineAvailable }

// evaluateProject runs the native engine over one (org, project) store for one
// evaluation context. The definitions read is a local SQLite scan; the evaluation
// is a pure in-memory FFI call.
func (c *Client) evaluateProject(org, project string, ctx []byte) (json.RawMessage, error) {
	if !c.configured() {
		return nil, fmt.Errorf("flags: engine not configured")
	}
	st, err := c.stores.For(org, project)
	if err != nil {
		return nil, err
	}
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
func (c *Client) resolve(def Def) (value string, source string) {
	if c.configured() {
		fv, pv, present := c.lookup(def.Key)
		if present {
			switch def.Type {
			case TypeBool:
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
		if ev := strings.TrimSpace(os.Getenv(def.Env)); ev != "" {
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
	res, err := c.evaluateProject(platformOrg, platformProject, ctx)
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
// cockpit's ONE write path) and applies it immediately in this pod.
func SetPlatformSwitch(key string, definition json.RawMessage, actor string) error {
	c := mounted
	if c == nil || c.stores == nil {
		return fmt.Errorf("flags: not mounted")
	}
	st, err := c.stores.For(platformOrg, platformProject)
	if err != nil {
		return err
	}
	if err := st.Upsert(key, definition, actor); err != nil {
		return err
	}
	c.invalidate()
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

// ── typed live accessors (the in-process evaluation seam) ───────────────────────

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
	Key         string `json:"key"`
	Category    string `json:"category"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Value       string `json:"value"`
	Source      string `json:"source"`
	Env         string `json:"env,omitempty"`
	ReadOnly    bool   `json:"readOnly"`
}

// BoardView is the full control-plane read board: the engine status plus every
// switch's live value. Definitions are edited in place over /v1/flags (the cockpit
// writes through SetPlatformSwitch); the activity log is the native change audit.
type BoardView struct {
	Engine     string       `json:"engine"`
	Configured bool         `json:"configured"`
	ManageURL  string       `json:"manageUrl"`
	AuditURL   string       `json:"auditUrl"`
	Switches   []SwitchView `json:"switches"`
}

// Board evaluates every registered switch live and returns the cockpit read board.
func Board() BoardView {
	list := Defs()
	sw := make([]SwitchView, 0, len(list))
	for _, d := range list {
		val, src := mounted.resolve(d)
		sw = append(sw, SwitchView{
			Key: d.Key, Category: d.Category, Label: d.Label, Description: d.Desc,
			Type: string(d.Type), Value: val, Source: src, Env: d.Env, ReadOnly: d.ReadOnly,
		})
	}
	return BoardView{Engine: "hanzo-flags", Configured: mounted.configured(), ManageURL: "/v1/flags/defs", AuditURL: "/v1/flags/activity", Switches: sw}
}

// ── lifecycle ────────────────────────────────────────────────────────────────

type state struct {
	client *Client
}

// Mount opens the per-org definition stores, installs the process-wide evaluation
// seam, and registers the /v1/flags surface. The native engine being absent (!cgo)
// degrades every switch to env/default and the HTTP surface reports it — never an
// error at boot.
func Mount(app *zip.App, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("flags.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("flags.Mount: empty deps.DataDir")
	}
	log := deps.Logger.New("subsystem", "flags")
	c := &Client{
		stores:     cloud.NewOrgStore[*Store](deps.DataDir, "flags", openStore),
		distinctID: firstNonEmpty(os.Getenv("FLAGS_PLATFORM_DISTINCT_ID"), "hanzo-platform:"+firstNonEmpty(deps.Brand, "hanzo")),
		ttl:        ttlFromEnv(),
	}
	mounted = c
	// Install this engine as the process-wide switch reader so the EDGE can read a
	// platform switch. serve.go's middleware cannot import this package (flags
	// imports the root package, root imports routers), so the root package holds the
	// seam and this is the one call that fills it.
	cloud.SetSwitchReader(Bool)
	b := cloud.NewBase(deps, "flags")
	svc := &cloud.Service[state]{Base: b, State: state{client: c}}
	routes(app, svc)
	log.Info("flags engine ready", "engine", "hanzo-flags", "native", engineAvailable, "ttlSeconds", int(c.ttl.Seconds()), "switches", len(Defs()))
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
	if v := strings.TrimSpace(os.Getenv("FLAGS_TTL_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 15 * time.Second
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
