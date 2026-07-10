// Package featureflags is cloud's runtime evaluation seam over the Hanzo Insights
// feature-flag engine (hanzoai/insights `rust/feature-flags`, the PostHog-compatible
// /flags + /decide evaluator). It is NOT a second flag store: Insights OWNS flag
// definitions, targeting, rollout %, and the change/activity log; cloud only EVALUATES
// flags at runtime and surfaces the platform switches to the admin cockpit.
//
// ONE flag engine, EVERY switch. The PLATFORM launch switches (waitlist, public
// signup, subsystem activation, gateway limits, network ids) are Insights feature
// flags; this package NAMES them (the registry below — key + category + the env var
// that supplies the fallback default) and reads their live value. Env is only the
// FALLBACK default; the Insights flag overrides. A subsystem calls Bool/Int/String and
// gets the hot value — a flip in the Insights flag UI takes effect within one cache TTL
// (default 15s) with NO redeploy.
//
// FAIL-SAFE. When Insights is not configured (INSIGHTS_FLAGS_URL / INSIGHTS_PROJECT_
// TOKEN unset) OR unreachable, evaluation degrades to the env fallback -> literal
// default — exactly today's behavior, zero regression. No secret is read here; the
// project token is injected from KMS into the process env by the deployment (the same
// WAITLIST_URL env convention clients/base's waitlist plugin reads), never hardcoded.
package featureflags

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// Def is ONE platform switch: an Insights feature-flag key qualified by the metadata
// the cockpit shows and the env var that provides the fallback default. This table is
// the ONE place the platform switches are named; Insights evaluates them.
type Def struct {
	Key      string // the Insights feature-flag key (snake_case)
	Category string // Launch | Signup | Subsystems | Gateway | Network
	Label    string
	Desc     string
	Type     Type
	Env      string // env var supplying the fallback default (may be "")
	Default  string // literal fallback when neither Insights nor env has a value
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

// snapshot is one cached read of the Insights /flags response.
type snapshot struct {
	flags    map[string]json.RawMessage // featureFlags[key]  (bool | variant string)
	payloads map[string]json.RawMessage // featureFlagPayloads[key] (arbitrary JSON)
	at       time.Time
	ok       bool
}

// Client evaluates Insights feature flags over the PostHog-compatible /flags endpoint,
// caching the whole response for one TTL (the hot-apply bound). Reads are lock-guarded
// and degrade to env/default when Insights is unconfigured or unreachable.
type Client struct {
	base       string
	token      string
	distinctID string
	hc         *http.Client
	ttl        time.Duration

	mu   sync.RWMutex
	snap snapshot
}

var mounted *Client

func (c *Client) configured() bool { return c != nil && c.base != "" && c.token != "" }

// resolve returns a switch's live string value and its source. Insights wins when it
// has a value; else the env fallback; else the literal default. Nil-safe.
func (c *Client) resolve(def Def) (value string, source string) {
	if c.configured() {
		fv, pv, present := c.lookup(def.Key)
		if present {
			switch def.Type {
			case TypeBool:
				if b, ok := asBool(fv); ok {
					return strconv.FormatBool(b), "insights"
				}
			case TypeInt:
				if n, ok := asInt(pv); ok {
					return strconv.Itoa(n), "insights"
				}
				if n, ok := asInt(fv); ok {
					return strconv.Itoa(n), "insights"
				}
			case TypeString:
				if s, ok := asString(pv); ok && s != "" {
					return s, "insights"
				}
				if s, ok := asString(fv); ok && s != "" {
					return s, "insights"
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

// ensureFresh refreshes the cached snapshot when older than the TTL. On a fetch error
// the previous snapshot is kept (graceful degradation) and the clock is stamped so a
// persistent failure is retried at most once per TTL, never on every read.
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
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	snap, err := c.fetch(ctx)
	if err != nil {
		c.snap.at = time.Now() // keep last values; bound retry to one per TTL
		return
	}
	c.snap = snap
}

func (c *Client) fetch(ctx context.Context) (snapshot, error) {
	body, _ := json.Marshal(map[string]string{"token": c.token, "distinct_id": c.distinctID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/flags", bytes.NewReader(body))
	if err != nil {
		return snapshot{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return snapshot{}, fmt.Errorf("insights /flags: status %d", resp.StatusCode)
	}
	var out struct {
		FeatureFlags        map[string]json.RawMessage `json:"featureFlags"`
		FeatureFlagPayloads map[string]json.RawMessage `json:"featureFlagPayloads"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return snapshot{}, err
	}
	if out.FeatureFlags == nil {
		out.FeatureFlags = map[string]json.RawMessage{}
	}
	if out.FeatureFlagPayloads == nil {
		out.FeatureFlagPayloads = map[string]json.RawMessage{}
	}
	return snapshot{flags: out.FeatureFlags, payloads: out.FeatureFlagPayloads, at: time.Now(), ok: true}, nil
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

// Bool returns the live boolean value of a registered switch (Insights -> env -> default).
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
// where it came from (insights | env | default).
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

// BoardView is the full control-plane read board: the engine status + a deep-link to
// the Insights flag manager (the ONE place a switch is edited) and its activity log
// (the native change audit), plus every switch's live value.
type BoardView struct {
	Engine     string       `json:"engine"`
	Configured bool         `json:"configured"`
	ManageURL  string       `json:"manageUrl"`
	AuditURL   string       `json:"auditUrl"`
	Switches   []SwitchView `json:"switches"`
}

// Board evaluates every registered switch live and returns the cockpit read board.
func Board() BoardView {
	appURL := strings.TrimRight(strings.TrimSpace(os.Getenv("INSIGHTS_APP_URL")), "/")
	proj := firstNonEmpty(os.Getenv("INSIGHTS_PROJECT_ID"), "1")
	var manage, audit string
	if appURL != "" {
		manage = fmt.Sprintf("%s/project/%s/feature_flags", appURL, proj)
		audit = fmt.Sprintf("%s/project/%s/activity?scope=FeatureFlag", appURL, proj)
	}
	list := Defs()
	sw := make([]SwitchView, 0, len(list))
	for _, d := range list {
		val, src := mounted.resolve(d)
		sw = append(sw, SwitchView{
			Key: d.Key, Category: d.Category, Label: d.Label, Description: d.Desc,
			Type: string(d.Type), Value: val, Source: src, Env: d.Env, ReadOnly: d.ReadOnly,
		})
	}
	return BoardView{Engine: "insights", Configured: mounted.configured(), ManageURL: manage, AuditURL: audit, Switches: sw}
}

// ── lifecycle ────────────────────────────────────────────────────────────────

// Mount builds the evaluation client from env (Insights base + KMS-injected project
// token) and installs it as the process-wide seam. It serves NO HTTP routes — it is the
// in-process read plane that subsystems and clients/admin consume. Disabled config is a
// no-op (env fallback only), never an error.
func Mount(_ *zip.App, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("featureflags.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "featureflags")
	c := &Client{
		base:       strings.TrimRight(strings.TrimSpace(os.Getenv("INSIGHTS_FLAGS_URL")), "/"),
		token:      strings.TrimSpace(os.Getenv("INSIGHTS_PROJECT_TOKEN")),
		distinctID: firstNonEmpty(os.Getenv("INSIGHTS_FLAGS_DISTINCT_ID"), "hanzo-platform:"+firstNonEmpty(deps.Brand, "hanzo")),
		hc:         &http.Client{Timeout: 4 * time.Second},
		ttl:        ttlFromEnv(),
	}
	mounted = c
	log.Info("featureflags evaluation seam ready", "engine", "insights", "configured", c.configured(), "ttlSeconds", int(c.ttl.Seconds()), "switches", len(Defs()))
	return nil
}

func ttlFromEnv() time.Duration {
	if v := strings.TrimSpace(os.Getenv("INSIGHTS_FLAGS_TTL_SECONDS")); v != "" {
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
