package websearch

// lineup.go — which engines one search runs, with which options, holding which key.
//
// THE PLATFORM'S SETTING IS THE ONE SOURCE. Which engines are enabled, their
// order and their options are a document in the settings store's row for the
// reserved platform org (product "websearch"), the same row every platform knob
// lives in: read over the plane (settings_fleet), written through this app's
// SuperAdmin surface (engines.go → settings_fleet_set). There is no environment
// path; a deployment variable was a value nobody could see and a rollout to change.
//
// A KEY IS READ FROM KMS AT THE MOMENT OF THE SEARCH, never held in the
// environment. The platform's engine keys are sealed under the reserved platform
// org admin, env prod, path /websearch — MOJEEK_KEY and BRAVE_KEY — beside the
// platform's other SuperAdmin-owned configuration, and only this app reads or
// writes them on the plane (apps/kms custody.go names the subtree). Org admin is
// also where a SuperAdmin's own `hanzo kms` writes land, so an operator seals a
// key there by hand and this surface reads the same record.
//
// THE DEFAULT DEPENDS ON THE KEY. With nothing saved, a search runs Mojeek alone
// when its key is held — the API answers 40 results from an independent index —
// and otherwise the keyless engines that answer from this cluster (bing, ddg)
// with Mojeek's page beside them. Mojeek alone without a key is not an option:
// its page answered `failed` from production egress (measured 2026-10-10).
//
// SEARCH NEVER RUNS A LINEUP THAT CANNOT ANSWER. A seat answers when its engine
// does so keyless or its key is held. The surface refuses to save a lineup with
// no such seat and refuses to delete the key that would leave one; at search
// time, a lineup that still has none — KMS unreachable, a key removed some other
// way — is replaced by the keyless lineup. Fewer engines, never an empty page.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/client"
	settings "github.com/hanzoai/cloud/client/settings"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
)

// product is this app's key in the settings store, spelled once.
const product = "websearch"

// The platform's engine keys in KMS: org admin, env prod, path /websearch. A ref
// is keyPath + <NAME>_KEY + "@" + keyEnv — orgs/admin/websearch/MOJEEK_KEY@prod
// is the record a SuperAdmin's `hanzo kms` addresses as /websearch/MOJEEK_KEY in
// env prod (the store keys it /orgs/admin/websearch, MOJEEK_KEY, prod).
const (
	keyOrg  = "admin"
	keyEnv  = "prod"
	keyPath = "orgs/admin/websearch/"
)

// peerTimeout bounds one plane call this package makes — the settings read and
// write, and each KMS read. A search that waits longer than this on its own
// configuration has already lost more than the configuration was worth.
const peerTimeout = 2 * time.Second

// keyUse is what a credential means to an engine.
type keyUse int

const (
	keyNone     keyUse = iota // takes none (bing, ddg)
	keyOptional               // a key upgrades it; it runs without one (mojeek: API, else page)
	keyRequired               // no key, no answer (brave)
)

// The options an engine can read. A closed set: each engine names the subset it
// supports and the query parameter each one sets.
const (
	optCount    = "count"
	optLanguage = "language"
	optRegion   = "region"
)

// optionSpec is one option an engine reads: which, the engine's own parameter for
// it, and — for count — its bounds and default.
type optionSpec struct {
	name, param   string
	min, max, def int
}

// question is one engine's question on one search: the query and the options
// that shape it, and the key that pays for it. build reads everything but key.
type question struct {
	query, lang, region string
	count               int
	key                 string
}

// seat is one engine in this search's lineup.
type seat struct {
	engine
	opts searchEngineOptions
	key  string
}

// question asks this seat's engine for query. The request's own language wins
// over the platform's; region and count are the platform's alone.
func (s seat) question(query, lang string) question {
	q := question{query: query, lang: s.opts.Language, region: s.opts.Region, count: s.count(s.opts), key: s.key}
	if lang != "" {
		q.lang = lang
	}
	return q
}

// spec is the engine's spec for one option, and whether it reads that option.
func (e engine) spec(name string) (optionSpec, bool) {
	for _, o := range e.supports {
		if o.name == name {
			return o, true
		}
	}
	return optionSpec{}, false
}

// count is the number of hits this engine is asked for under o: the setting, or
// the engine's default; zero for an engine that reads no count.
func (e engine) count(o searchEngineOptions) int {
	sp, ok := e.spec(optCount)
	if !ok {
		return 0
	}
	if o.Count > 0 {
		return o.Count
	}
	return sp.def
}

// answers reports whether the engine answers from this cluster given whether its
// key is held.
func (e engine) answers(keyed bool) bool { return e.keyless || (e.key != keyNone && keyed) }

// answering reports whether any seat can answer.
func answering(seats []seat) bool {
	return slices.ContainsFunc(seats, func(s seat) bool { return s.answers(s.key != "") })
}

// keylessNames is the lineup that answers with no key at all, in run order.
var keylessNames = []string{bingName, ddgName, mojeekName}

// keylessLineup is the keyless lineup with every engine's default options: what a
// search runs when nothing it was given can answer.
func keylessLineup() []seat {
	out := make([]seat, 0, len(keylessNames))
	for _, n := range keylessNames {
		out = append(out, seat{engine: engineByName[n]})
	}
	return out
}

// defaults is the enabled set when nothing is saved: Mojeek alone once its key is
// held, the keyless lineup until then.
func defaults(mojeekKeyed bool) []string {
	if mojeekKeyed {
		return []string{mojeekName}
	}
	return slices.Clone(keylessNames)
}

// lineup is the seats this search runs, resolved once per search: the saved
// setting (or the default), each keyed engine's key read from KMS, and the
// keyless lineup in place of one that cannot answer.
func lineup(ctx context.Context) []seat {
	d, saved := held(ctx)
	keys := map[string]string{}
	key := func(name string) string {
		if v, ok := keys[name]; ok {
			return v
		}
		v := readKey(ctx, name)
		keys[name] = v
		return v
	}
	names := d.Enabled
	if !saved {
		names = defaults(key(mojeekName) != "")
	}
	seats := make([]seat, 0, len(names))
	for _, n := range names {
		e := engineByName[n]
		s := seat{engine: e, opts: d.Options[n]}
		if e.key != keyNone {
			s.key = key(n)
		}
		seats = append(seats, s)
	}
	if !answering(seats) {
		return keylessLineup()
	}
	return seats
}

// good is the last setting read from the store, so a search during a settings
// outage runs what the operator chose rather than the default.
var good atomic.Pointer[stored]

type stored struct {
	doc   searchEnginesSet
	saved bool
}

// held is the platform's setting for this search: the store's answer, else the
// last one this process read, else nothing saved. A search never waits on, or
// fails for, its own configuration.
func held(ctx context.Context) (searchEnginesSet, bool) {
	d, saved, err := fetch(ctx)
	if err == nil {
		good.Store(&stored{doc: d, saved: saved})
		return d, saved
	}
	warn("websearch: the engine setting could not be read; searching with the last one read", "err", err)
	if p := good.Load(); p != nil {
		return p.doc, p.saved
	}
	return searchEnginesSet{}, false
}

// fetch reads the saved setting. saved is false when nothing is stored. A document
// that names an unknown engine or an invalid option — only a write that bypassed
// this app's surface can store one — is read leniently: what is valid stands,
// the rest is dropped and said once, and a document left with no engine reads as
// nothing saved.
func fetch(ctx context.Context) (searchEnginesSet, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()
	out, err := settings.SettingsFleet(ctx, &client.Product{Product: product})
	if err != nil {
		return searchEnginesSet{}, false, err
	}
	raw := ""
	if out != nil {
		raw = strings.TrimSpace(out.Config)
	}
	if raw == "" {
		return searchEnginesSet{}, false, nil
	}
	var d searchEnginesSet
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		said(raw, "the stored engine setting does not parse; searching with the default", err.Error())
		return searchEnginesSet{}, false, nil
	}
	clean, problems := check(d)
	if len(problems) > 0 {
		said(raw, "the stored engine setting has entries this deployment cannot run; they are ignored", strings.Join(problems, "; "))
	}
	if len(clean.Enabled) == 0 {
		return searchEnginesSet{}, false, nil
	}
	return clean, true, nil
}

// lastSaid is the stored document a warning was last written for, so a bad
// document is reported once and not once per search.
var lastSaid atomic.Pointer[string]

func said(raw, msg, detail string) {
	if p := lastSaid.Load(); p != nil && *p == raw {
		return
	}
	lastSaid.Store(&raw)
	warn("websearch: "+msg, "problems", detail)
}

var (
	languageRE = regexp.MustCompile(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{2,8})?$`)
	regionRE   = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

// check validates a setting and answers it cleaned, with every problem found. The
// surface refuses a setting with any problem; the reader runs the cleaned one.
// One rule for both, so what can be saved and what is run cannot drift.
//
// Cleaned means: enabled names only registered engines, once each, in the order
// given; options name only registered engines and only the options each reads,
// within bounds; a region is upper case.
func check(in searchEnginesSet) (searchEnginesSet, []string) {
	var problems []string
	out := searchEnginesSet{Options: map[string]searchEngineOptions{}}
	for _, raw := range in.Enabled {
		n := strings.TrimSpace(raw)
		if _, ok := engineByName[n]; !ok {
			problems = append(problems, fmt.Sprintf("no engine named %q", raw))
			continue
		}
		if slices.Contains(out.Enabled, n) {
			problems = append(problems, fmt.Sprintf("%s is enabled twice", n))
			continue
		}
		out.Enabled = append(out.Enabled, n)
	}
	if len(in.Enabled) == 0 {
		problems = append(problems, "enabled must name at least one engine")
	}
	for n, o := range in.Options {
		e, ok := engineByName[n]
		if !ok {
			problems = append(problems, fmt.Sprintf("options name no engine %q", n))
			continue
		}
		var keep searchEngineOptions
		if o.Count != 0 {
			if sp, ok := e.spec(optCount); !ok {
				problems = append(problems, n+" takes no count")
			} else if o.Count < sp.min || o.Count > sp.max {
				problems = append(problems, fmt.Sprintf("%s count must be %d to %d", n, sp.min, sp.max))
			} else {
				keep.Count = o.Count
			}
		}
		if o.Language != "" {
			if _, ok := e.spec(optLanguage); !ok {
				problems = append(problems, n+" takes no language")
			} else if !languageRE.MatchString(o.Language) {
				problems = append(problems, fmt.Sprintf("%s language %q is not a language tag like en or en-US", n, o.Language))
			} else {
				keep.Language = o.Language
			}
		}
		if o.Region != "" {
			if _, ok := e.spec(optRegion); !ok {
				problems = append(problems, n+" takes no region")
			} else if !regionRE.MatchString(o.Region) {
				problems = append(problems, fmt.Sprintf("%s region %q is not a two-letter country code", n, o.Region))
			} else {
				keep.Region = strings.ToUpper(o.Region)
			}
		}
		if keep != (searchEngineOptions{}) {
			out.Options[n] = keep
		}
	}
	if len(out.Options) == 0 {
		out.Options = nil
	}
	return out, problems
}

// keyRef is the KMS ref of an engine's key.
func keyRef(name string) string {
	return keyPath + strings.ToUpper(name) + "_KEY@" + keyEnv
}

// store is where the platform's engine keys live. Exactly one process holds it
// and this app is never that process, so every call asks it over the plane —
// from the websearch app, and from the ai and answer apps that search in-process.
var store = cloud.KMSPeer{}

// keyCtx is the context a KMS call for a platform key runs on: a fresh one acting
// for the org the keys are sealed under, bounded, and cancelled with the caller.
//
// Fresh, because a request's org wins over a stated one: a search by acme asking
// for the platform's key on acme's request context is refused by the store,
// rightly, and so is one stating nothing, which acts for the brand org. The key
// is the platform's whoever searched — the license signer reads its seed the same
// way (plugin/licensing).
func keyCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	k, cancel := context.WithTimeout(cloud.For(context.Background(), keyOrg), peerTimeout)
	stop := context.AfterFunc(ctx, cancel)
	return k, func() { stop(); cancel() }
}

// keyHeld reports whether the platform holds a key for the engine. The value is
// read and dropped; nothing here keeps or returns it.
func keyHeld(ctx context.Context, name string) (bool, error) {
	k, done := keyCtx(ctx)
	defer done()
	v, err := store.GetSecret(k, keyRef(name))
	switch {
	case errors.Is(err, secret.ErrNone):
		return false, nil
	case err != nil:
		return false, err
	}
	return strings.TrimSpace(string(v)) != "", nil
}

// readKey is the engine's key for one search, "" when none is held. A store that
// cannot answer reads as no key, so the engine falls back to what it does without
// one and the search still answers.
func readKey(ctx context.Context, name string) string {
	k, done := keyCtx(ctx)
	defer done()
	v, err := store.GetSecret(k, keyRef(name))
	switch {
	case errors.Is(err, secret.ErrNone):
		return ""
	case err != nil:
		warn("websearch: an engine key could not be read; searching without it", "engine", name, "err", err)
		return ""
	}
	return strings.TrimSpace(string(v))
}
