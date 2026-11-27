package websearch

// engines.go — the SuperAdmin surface over which engines search runs.
//
// Four typed ops under /v1/websearch/engines, so each is a REST route, a CLI
// command, an SDK method and — for the two that touch no key — an MCP tool
// (client/surface.go withholds a mutating verb on a key from agents). Each asks
// for platform authority in its own preamble, because MCP, the call plane and the
// CLI invoke the op with no route and therefore no middleware; the writes ask the
// anti-forgery control too, as every platform write does. Every call is on the
// audit trail: AuditTrail records each request made with platform authority, read
// or write, and the settings app records the stored document before and after.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/hanzoai/cloud/client"
	settings "github.com/hanzoai/cloud/client/settings"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/zap-proto/zip"
)

// maxKey bounds a key. Vendor keys are tens of bytes; this is a bound on what a
// request may hand the store, not a guess at a format.
const maxKey = 4096

// searchEngineOptions are the values one engine is asked with. A field the engine
// does not read is absent, and so is a language or region that is not set.
type searchEngineOptions struct {
	// Count is how many hits the engine is asked for per search. 0 or absent on
	// a write means the engine's default; a read reports the value in force.
	Count int `json:"count,omitempty"`
	// Language is the default language the engine is asked in, as a tag like
	// `en` or `en-US`. A search that names its own language overrides it.
	Language string `json:"language,omitempty"`
	// Region is a two-letter country code the engine favours, like `GB`.
	// Stored upper case.
	Region string `json:"region,omitempty"`
}

// searchEngineOption is one option an engine reads.
type searchEngineOption struct {
	// Name is the option: `count`, `language` or `region`.
	Name string `json:"name"`
	// Param is the engine's own query parameter the option sets, such as `t`
	// or `rb` for Mojeek.
	Param string `json:"param"`
	// Min is the least count the engine accepts. Absent for a text option.
	Min int `json:"min,omitempty"`
	// Max is the most count the engine accepts. Absent for a text option.
	Max int `json:"max,omitempty"`
	// Default is the count used when none is set. Absent for a text option,
	// whose default is unset.
	Default int `json:"default,omitempty"`
}

// searchEngine is one registered engine and how the platform runs it.
type searchEngine struct {
	// Name is the engine: `mojeek`, `bing`, `brave` or `ddg`.
	Name string `json:"name"`
	// Enabled is whether searches run it.
	Enabled bool `json:"enabled"`
	// Order is its place among the enabled engines, from 1; 0 when disabled.
	Order int `json:"order"`
	// AcceptsKey is whether the engine takes an API key at all.
	AcceptsKey bool `json:"acceptsKey"`
	// NeedsKey is whether the engine answers only with a key.
	NeedsKey bool `json:"needsKey"`
	// Keyed is whether the platform holds a key for it in KMS. The key itself is
	// never returned by any operation.
	Keyed bool `json:"keyed"`
	// Available is whether the engine answers from this cluster as things
	// stand: it needs no key there (bing, ddg), or its key is held. Mojeek's
	// keyless page still runs as a fallback but does not count, because it does
	// not answer from this cluster's egress.
	Available bool `json:"available"`
	// Options are the values the engine is asked with, defaults filled in.
	Options searchEngineOptions `json:"options"`
	// Supports lists the options the engine reads, with each one's bounds.
	Supports []searchEngineOption `json:"supports"`
}

// searchEngines is every registered engine and how the platform runs it.
type searchEngines struct {
	// Saved is whether a setting is stored. False means the default is in
	// force: Mojeek alone when its key is held, else bing, ddg and Mojeek.
	Saved bool `json:"saved"`
	// Engines are the enabled engines in the order they run, then the rest by
	// name.
	Engines []searchEngine `json:"engines"`
}

// searchEnginesSet is the platform's engine setting: what PUT takes and what the
// settings store holds for product `websearch`.
type searchEnginesSet struct {
	// Enabled are the engines searches run, in order. At least one, each a
	// registered engine named once, and at least one of them must be available.
	Enabled []string `json:"enabled"`
	// Options are each engine's values, by engine name — for any registered
	// engine, enabled or not. An engine or a field left out takes its default:
	// the setting replaces the stored one whole.
	Options map[string]searchEngineOptions `json:"options,omitempty"`
}

// searchEngineKey is an engine's API key to seal.
type searchEngineKey struct {
	// Name is the engine, from the path.
	Name string `json:"name"`
	// Key is the API key. Sealed in KMS and never returned, logged or echoed.
	// Never read from the query string.
	Key string `json:"key" url:"-"`
}

// searchEngineRef names one engine.
type searchEngineRef struct {
	// Name is the engine, from the path.
	Name string `json:"name"`
}

// searchEngineKeyed is whether the platform holds a key for one engine, after a
// key operation.
type searchEngineKeyed struct {
	// Name is the engine.
	Name string `json:"name"`
	// Keyed is whether a key is now held.
	Keyed bool `json:"keyed"`
}

// super is the one gate on this surface: platform authority, owner == "admin".
// An org admin is refused like any member — administering one org is not
// configuring the platform's search.
func super(ctx context.Context) error {
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return cloud.Super.Refusal()
	}
	return nil
}

// change is the gate on a write: platform authority, then the anti-forgery
// control, which is a no-op for a caller presenting a credential.
func change(ctx context.Context) error {
	if err := super(ctx); err != nil {
		return err
	}
	return cloud.CSRF(ctx)
}

// Lists every engine web search can run and how the platform runs it.
//
// For each engine: whether it is enabled and its place in the run order, whether
// it takes an API key and whether it needs one, whether the platform holds that
// key (`keyed` — the key itself is never returned), whether it answers from this
// cluster as things stand (`available`), the option values it is asked with and
// the options it supports with their bounds. Enabled engines come first, in the
// order they run.
//
// `saved` is false when no setting is stored, and the engines shown enabled are
// then the default: Mojeek alone when its key is held, otherwise bing, ddg and
// Mojeek — Mojeek's keyless page does not answer from this cluster on its own.
//
// SuperAdmin only. 503 when the settings store or KMS cannot answer: a board
// that guessed would show an operator a configuration nobody saved.
func listSearchEngines(ctx context.Context, _ *cloud.Unit) (*searchEngines, error) {
	if err := super(ctx); err != nil {
		return nil, err
	}
	d, saved, err := fetch(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "websearch: the engine setting could not be read: %v", err)
	}
	held, err := keysHeld(ctx)
	if err != nil {
		return nil, err
	}
	return board(d, saved, held), nil
}

// Replaces the platform's web search engine setting and answers the result.
//
// `enabled` lists the engines searches run, in order — at least one, each a
// registered engine named once, and at least one that is available (needs no key
// here, or its key is held). `options` gives each engine's values by name; an
// engine or a field left out takes its default, because the setting replaces the
// stored one whole. A count must lie within the engine's bounds, a language is a
// tag like `en` or `en-US`, a region a two-letter country code, and an option the
// engine does not support is refused.
//
// SuperAdmin only. 400 names every problem with the setting at once; 503 when
// the settings store or KMS cannot answer. The answer is the engine list as it
// now stands, so the caller sees the effect without a second read.
//
// Example: {"enabled":["mojeek"],"options":{"mojeek":{"count":30,"language":"en","region":"GB"}}}
func setSearchEngines(ctx context.Context, in *searchEnginesSet) (*searchEngines, error) {
	if err := change(ctx); err != nil {
		return nil, err
	}
	d, problems := check(*in)
	if len(problems) > 0 {
		return nil, zip.ErrBadRequest("websearch: " + strings.Join(problems, "; "))
	}
	held, err := keysHeld(ctx)
	if err != nil {
		return nil, err
	}
	if !lineupAnswers(d.Enabled, held) {
		return nil, zip.ErrBadRequest("websearch: no enabled engine can answer — " + unanswered(d.Enabled) +
			"; enable bing or ddg, or set the key of an engine that takes one")
	}
	doc, err := jsonText(d)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()
	if _, err := settings.SettingsFleetSet(pctx, &client.FleetConfig{Product: product, Config: doc}); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "websearch: the settings store did not take the change: %v", err)
	}
	good.Store(&stored{doc: d, saved: true})
	return board(d, true, held), nil
}

// Seals an engine's API key in KMS, replacing any key held.
//
// The key is the platform's: it lives in KMS under the platform org admin at
// /websearch/<ENGINE>_KEY (env prod), every search reads it from there at the
// moment it runs, and no
// operation returns, logs or echoes it — the answer says only that a key is now
// held. Only an engine that takes a key accepts one (mojeek, brave).
//
// SuperAdmin only. 404 for an engine that does not exist, 400 for one that takes
// no key or a key that is empty, longer than 4096 bytes or holds whitespace or
// control characters, 503 when KMS cannot take it.
func setSearchEngineKey(ctx context.Context, in *searchEngineKey) (*searchEngineKeyed, error) {
	if err := change(ctx); err != nil {
		return nil, err
	}
	e, err := keyedEngine(in.Name)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.Key)
	switch {
	case key == "":
		return nil, zip.ErrBadRequest("websearch: key is required")
	case len(key) > maxKey:
		return nil, zip.ErrBadRequest("websearch: key is longer than 4096 bytes")
	case strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		return nil, zip.ErrBadRequest("websearch: key holds whitespace or control characters")
	}
	k, done := keyCtx(ctx)
	defer done()
	if err := store.PutSecret(k, keyRef(e.name), []byte(key)); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "websearch: KMS did not take the key")
	}
	return &searchEngineKeyed{Name: e.name, Keyed: true}, nil
}

// Removes an engine's API key from KMS.
//
// Searches stop using the key at once. Removing a key that is not held answers
// the same as removing one that is. 409 when the saved setting would be left with
// no engine that can answer — enable bing or ddg first.
//
// SuperAdmin only. 404 for an engine that does not exist, 400 for one that takes
// no key, 503 when the settings store or KMS cannot answer.
func deleteSearchEngineKey(ctx context.Context, in *searchEngineRef) (*searchEngineKeyed, error) {
	if err := change(ctx); err != nil {
		return nil, err
	}
	e, err := keyedEngine(in.Name)
	if err != nil {
		return nil, err
	}
	d, saved, err := fetch(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "websearch: the engine setting could not be read: %v", err)
	}
	if saved && slices.Contains(d.Enabled, e.name) {
		held, err := keysHeld(ctx)
		if err != nil {
			return nil, err
		}
		held[e.name] = false
		if !lineupAnswers(d.Enabled, held) {
			return nil, zip.ErrConflict("websearch: removing the " + e.name + " key would leave no enabled engine that can answer; enable bing or ddg first")
		}
	}
	k, done := keyCtx(ctx)
	defer done()
	if err := store.DeleteSecret(k, keyRef(e.name)); err != nil && !errors.Is(err, secret.ErrNone) {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "websearch: KMS did not remove the key")
	}
	return &searchEngineKeyed{Name: e.name, Keyed: false}, nil
}

// keyedEngine resolves the engine a key operation names: 404 when there is none,
// 400 when it takes no key.
func keyedEngine(name string) (engine, error) {
	e, ok := engineByName[strings.TrimSpace(name)]
	if !ok {
		return engine{}, zip.ErrNotFound("websearch: no engine named " + name)
	}
	if e.key == keyNone {
		return engine{}, zip.ErrBadRequest("websearch: " + e.name + " takes no key")
	}
	return e, nil
}

// keysHeld reports, for every engine that takes a key, whether the platform holds
// one. 503 when KMS cannot answer.
func keysHeld(ctx context.Context) (map[string]bool, error) {
	held := map[string]bool{}
	for name, e := range engineByName {
		if e.key == keyNone {
			continue
		}
		ok, err := keyHeld(ctx, name)
		if err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "websearch: KMS could not say whether the %s key is held", name)
		}
		held[name] = ok
	}
	return held, nil
}

// lineupAnswers reports whether any of the named engines can answer given which
// keys are held.
func lineupAnswers(names []string, held map[string]bool) bool {
	return slices.ContainsFunc(names, func(n string) bool { return engineByName[n].answers(held[n]) })
}

// unanswered names why each engine in names cannot answer, for a refusal.
func unanswered(names []string) string {
	why := make([]string, 0, len(names))
	for _, n := range names {
		e := engineByName[n]
		switch e.key {
		case keyRequired:
			why = append(why, n+" needs its key")
		case keyOptional:
			why = append(why, n+" answers from this cluster only with its key")
		}
	}
	return strings.Join(why, ", ")
}

// board renders the engine list for a setting and the keys held.
func board(d searchEnginesSet, saved bool, held map[string]bool) *searchEngines {
	enabled := d.Enabled
	if !saved {
		enabled = defaults(held[mojeekName])
	}
	out := &searchEngines{Saved: saved, Engines: make([]searchEngine, 0, len(engineByName))}
	for i, n := range enabled {
		out.Engines = append(out.Engines, view(engineByName[n], d.Options[n], i+1, held[n]))
	}
	var rest []string
	for n := range engineByName {
		if !slices.Contains(enabled, n) {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	for _, n := range rest {
		out.Engines = append(out.Engines, view(engineByName[n], d.Options[n], 0, held[n]))
	}
	return out
}

// view is one engine as the board shows it.
func view(e engine, o searchEngineOptions, order int, keyed bool) searchEngine {
	o.Count = e.count(o)
	v := searchEngine{
		Name:       e.name,
		Enabled:    order > 0,
		Order:      order,
		AcceptsKey: e.key != keyNone,
		NeedsKey:   e.key == keyRequired,
		Keyed:      keyed && e.key != keyNone,
		Available:  e.answers(keyed),
		Options:    o,
		Supports:   make([]searchEngineOption, 0, len(e.supports)),
	}
	for _, sp := range e.supports {
		v.Supports = append(v.Supports, searchEngineOption{Name: sp.name, Param: sp.param, Min: sp.min, Max: sp.max, Default: sp.def})
	}
	return v
}

// jsonText is d as the document the settings store keeps.
func jsonText(d searchEnginesSet) (string, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return "", zip.ErrInternal("websearch: the setting could not be encoded")
	}
	return string(b), nil
}
