package principals

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hanzoai/cloud/sdk/iam"
)

// Constants matching published sanctions list identifiers and entry shapes.
const (
	ListOFAC = "ofac"
	ListUN   = "un"
	ListEU   = "eu"
	ListOFSI = "ofsi"

	KindEntity     = "entity"
	KindIndividual = "individual"

	NamePrimary = "primary"
	DocAddress  = "address"

	MatchThreshold = 0.85
)

// ScreenName is one name recorded on a designation entry.
type ScreenName struct {
	Full   string
	Type   string
	Strong bool
}

// ScreenDocument is an identifier or blockchain address on a designation entry.
type ScreenDocument struct {
	Kind   string
	Number string
	Chain  string
}

// ScreenEntry is one designation entry from a published sanctions list.
type ScreenEntry struct {
	List      string
	RefID     string
	Group     string
	Kind      string
	Birth     string
	Names     []ScreenName
	Documents []ScreenDocument
}

func (e ScreenEntry) PrimaryName() string {
	for _, n := range e.Names {
		if n.Type == NamePrimary && n.Full != "" {
			return n.Full
		}
	}
	if len(e.Names) > 0 {
		return e.Names[0].Full
	}
	return e.RefID
}

// ScreenBirth bounds a date-of-birth query.
type ScreenBirth struct {
	From string
	To   string
}

// ScreenQuery asks the matcher for designations matching Name/Kind/Birth.
type ScreenQuery struct {
	Name  string
	Kind  string
	Birth ScreenBirth
}

// ScreenMatch is one candidate hit returned by a Matcher.
type ScreenMatch struct {
	Entry    ScreenEntry
	Name     ScreenName
	Score    float64
	Reason   string
	Agree    []string
	Conflict []string
}

// ScreenResult is the outcome of fetching one publisher's list.
type ScreenResult struct {
	Source  string
	Entries []ScreenEntry
	Digest  string
	Err     error
}

// ScreenFitness reports one list's freshness and load state (shape-identical to iam.List).
type ScreenFitness = iam.List

// Matcher indexes loaded sanctions lists and answers name and chain-address queries.
type Matcher interface {
	Load(source string, entries []ScreenEntry) error
	Ready() error
	Search(q ScreenQuery, threshold float64) ([]ScreenMatch, error)
	Chain(address string) ([]ScreenMatch, error)
}

var (
	screenSources = []string{ListOFAC, ListUN, ListEU, ListOFSI}
	matcherHook   func(stale time.Duration) Matcher
)

// RegisterMatcher installs a custom sanctions list Matcher factory (e.g. luxfi/aml).
func RegisterMatcher(fn func(stale time.Duration) Matcher) {
	matcherHook = fn
}

// RegisterPublisher installs the sanctions list fetch function used by new mounts.
func RegisterPublisher(fn func(context.Context) []ScreenResult) {
	if fn != nil {
		published = fn
	}
}

func defaultPublished(context.Context) []ScreenResult {
	err := errors.New("no sanctions list publisher configured")
	out := make([]ScreenResult, len(screenSources))
	for i, s := range screenSources {
		out[i] = ScreenResult{Source: s, Err: err}
	}
	return out
}

type screenReadiness struct {
	mu      sync.RWMutex
	sources []string
	byName  map[string]ScreenFitness
}

func newScreenReadiness(sources []string) *screenReadiness {
	m := make(map[string]ScreenFitness, len(sources))
	for _, s := range sources {
		m[s] = ScreenFitness{Source: s}
	}
	return &screenReadiness{sources: slices.Clone(sources), byName: m}
}

func (r *screenReadiness) Record(results []ScreenResult, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, res := range results {
		f := r.byName[res.Source]
		f.Source = res.Source
		f.AttemptedAt = at
		if res.Err != nil {
			f.Err = res.Err.Error()
			r.byName[res.Source] = f
			continue
		}
		f.LoadedAt = at
		f.Designations = len(res.Entries)
		f.Digest = res.Digest
		f.Err = ""
		r.byName[res.Source] = f
	}
}

func (r *screenReadiness) Unfit(now time.Time, stale time.Duration) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for _, s := range r.sources {
		f := r.byName[s]
		if f.LoadedAt.IsZero() || now.Sub(f.LoadedAt) > stale {
			out = append(out, s)
		}
	}
	return out
}

func (r *screenReadiness) Lists() []ScreenFitness {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ScreenFitness, 0, len(r.sources))
	for _, s := range r.sources {
		out = append(out, r.byName[s])
	}
	return out
}

// builtinMatcher provides exact blockchain-address and normalized token-similarity
// name screening when no external Matcher is registered.
type builtinMatcher struct {
	mu     sync.RWMutex
	byList map[string][]ScreenEntry
}

func newScreenMatcher(stale time.Duration) Matcher {
	if matcherHook != nil {
		return matcherHook(stale)
	}
	return &builtinMatcher{byList: map[string][]ScreenEntry{}}
}

func (m *builtinMatcher) Load(source string, entries []ScreenEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byList[source] = slices.Clone(entries)
	return nil
}

func (m *builtinMatcher) Ready() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range screenSources {
		if len(m.byList[s]) == 0 {
			return errors.New("sanctions list empty: " + s)
		}
	}
	return nil
}

var legalSuffixes = map[string]bool{
	"co": true, "company": true, "inc": true, "incorporated": true,
	"llc": true, "ltd": true, "limited": true, "corp": true,
	"corporation": true, "plc": true, "gmbh": true,
	"holdings": true, "holding": true, "group": true,
}

func normTokens(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	raw := strings.Fields(b.String())
	toks := make([]string, 0, len(raw))
	for _, tok := range raw {
		if !legalSuffixes[tok] {
			toks = append(toks, tok)
		}
	}
	if len(toks) == 0 {
		toks = raw
	}
	slices.Sort(toks)
	return slices.Compact(toks)
}

func tokenScore(a, b string) float64 {
	ta, tb := normTokens(a), normTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	if slices.Equal(ta, tb) {
		return 1.0
	}
	inter := 0
	for _, t := range ta {
		if slices.Contains(tb, t) {
			inter++
		}
	}
	dice := float64(2*inter) / float64(len(ta)+len(tb))
	if len(tb) >= 2 && inter == len(tb) && dice < 0.90 {
		return 0.90
	}
	return dice
}

func (m *builtinMatcher) Search(q ScreenQuery, threshold float64) ([]ScreenMatch, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ScreenMatch
	for _, s := range screenSources {
		for _, e := range m.byList[s] {
			if q.Kind != "" && e.Kind != "" && !strings.EqualFold(q.Kind, e.Kind) {
				continue
			}
			var best ScreenName
			bestScore := 0.0
			for _, n := range e.Names {
				if sc := tokenScore(q.Name, n.Full); sc > bestScore {
					bestScore, best = sc, n
				}
			}
			if bestScore >= threshold {
				hit := ScreenMatch{
					Entry:  e,
					Name:   best,
					Score:  bestScore,
					Reason: "name similarity",
				}
				if q.Birth.From != "" && e.Birth != "" {
					if q.Birth.From == e.Birth {
						hit.Agree = []string{"date of birth " + e.Birth}
					} else {
						hit.Conflict = []string{"date of birth " + e.Birth}
					}
				}
				out = append(out, hit)
			}
		}
	}
	return out, nil
}

func (m *builtinMatcher) Chain(address string) ([]ScreenMatch, error) {
	want := strings.ToLower(strings.TrimSpace(address))
	if want == "" {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ScreenMatch
	for _, s := range screenSources {
		for _, e := range m.byList[s] {
			for _, d := range e.Documents {
				if d.Kind == DocAddress && strings.ToLower(strings.TrimSpace(d.Number)) == want {
					primary := ScreenName{Full: e.PrimaryName(), Type: NamePrimary}
					out = append(out, ScreenMatch{
						Entry:  e,
						Name:   primary,
						Score:  1.0,
						Reason: "digital currency address " + d.Number,
					})
				}
			}
		}
	}
	return out, nil
}
