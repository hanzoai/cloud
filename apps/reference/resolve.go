package reference

// resolve.go answers the only question this plane exists to answer: what is
// known about this key, which version said so, and how old is that version.
//
// TWO LOOKUPS, ALWAYS IN THIS ORDER: the caller's OWN override first, then the
// shared baseline. First hit wins. An organisation's say about its own world
// beats a published list, and that is the whole of the precedence rule — there
// is no third tier and no merge.
//
// ONE MATCHER FOR BOTH. The candidate keys an override is looked up by are the
// SAME candidates the baseline is looked up by, produced by one function, most
// specific first. Two matchers would mean a tenant's deny of tempbox.example
// covering mail.tempbox.example in the baseline's sense and not in their own.
//
// SILENCE IS NEVER CLEAN. An answer carries Refusal when the set could not be
// consulted — never loaded, unlicensed, held elsewhere — and a caller that reads
// Hit==false without reading Refusal is reading "we have no idea" as "not
// listed". That distinction is the difference between a control and the
// appearance of one, and it is why every field below exists.
//
// STALENESS IS A SIGNAL, NOT AN ERROR. A set past its freshness bound still
// answers, and says so, because yesterday's list beats no list — but a decision
// that consulted a three-week-old disposable-domain list should be able to know
// that it did.

import (
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// scanned is one entry of a set that cannot be addressed by key: a compiled
// crawler pattern, or a delegated number range.
type scanned struct {
	entry  Entry
	re     *regexp.Regexp
	lo, hi uint64
}

// snap is one set, resolved and immutable. It is built once per refresh and
// swapped in whole, so a reader either sees the old version or the new one and
// never a half-built map.
type snap struct {
	set     Set
	version string
	took    []version
	asOf    time.Time
	fetched time.Time
	byKey   map[string]Entry
	scan    []scanned
	any     *regexp.Regexp
	refusal string
}

// stale reports whether this set is past its freshness bound.
func (s *snap) stale(now time.Time) bool {
	if s == nil || s.asOf.IsZero() {
		return true
	}
	if s.set.MaxAge <= 0 {
		return false
	}
	return now.Sub(s.asOf) > s.set.MaxAge
}

// age is how long since the OLDEST contributing publisher was current. The
// oldest and not the newest: a set is exactly as fresh as its weakest source,
// and reporting the newest would let one daily-updating publisher hide three
// that stopped answering months ago.
func (s *snap) age(now time.Time) time.Duration {
	if s == nil || s.asOf.IsZero() {
		return 0
	}
	return now.Sub(s.asOf)
}

// build assembles a set's snapshot from its sources' current versions and their
// entries. A set with no ready source is not an empty set — it is a set that
// refuses, and the refusal names why.
func build(set Set, took []version, entries []Entry) *snap {
	s := &snap{set: set, took: took, byKey: map[string]Entry{}}

	if set.Kind == KindSeam {
		s.refusal = set.Refusal
		return s
	}
	if len(took) == 0 {
		s.refusal = "this set has never loaded, so it cannot tell a clean key from an unknown one"
		return s
	}

	sort.Slice(took, func(i, j int) bool { return took[i].Source < took[j].Source })
	var parts []string
	for i, v := range took {
		parts = append(parts, v.Source+"@"+v.Version)
		if i == 0 || v.AsOf.Before(s.asOf) {
			s.asOf = v.AsOf
		}
		if v.Fetched.After(s.fetched) {
			s.fetched = v.Fetched
		}
	}
	// The set's version is the composition of its sources' versions, so a
	// decision can name ONE string and an auditor can resolve it back to exactly
	// which publisher contributed what.
	s.version = strings.Join(parts, "+")

	if set.Kind == KindAttest {
		// The membership is held by the component that screens against it. This
		// plane knows how fresh that component's lists are and nothing else, and
		// saying so is more useful than a second copy that would drift.
		s.refusal = "membership of this set is held by the component that screens against it; this plane reports its freshness"
		return s
	}

	switch set.Match {
	case MatchPattern:
		var alts []string
		for _, e := range entries {
			re, err := regexp.Compile(e.Key)
			if err != nil {
				// A pattern that will not compile is dropped rather than failing the
				// whole set: one bad row from a publisher must not silence the rest.
				continue
			}
			s.scan = append(s.scan, scanned{entry: e, re: re})
			alts = append(alts, "(?:"+e.Key+")")
		}
		// One alternation over every pattern is the fast reject. Most traffic
		// matches nothing, and a single automaton pass answers that in one walk of
		// the input instead of one walk per pattern.
		if len(alts) > 0 {
			if re, err := regexp.Compile(strings.Join(alts, "|")); err == nil {
				s.any = re
			}
		}
	case MatchRange:
		for _, e := range entries {
			lo, hi, ok := span(e.Key)
			if !ok {
				continue
			}
			s.scan = append(s.scan, scanned{entry: e, lo: lo, hi: hi})
		}
	default:
		for _, e := range entries {
			s.byKey[e.Key] = e
		}
	}

	if len(entries) == 0 && !set.emptyIsFact() {
		s.refusal = "this set loaded no entries, which no published list is — the fetch or the parse is wrong"
	}
	return s
}

// candidates renders the bounded, most-specific-first list of keys a lookup
// should try. It is the ONE place a set's matching shape becomes concrete, and
// both the override store and the baseline snapshot consult it.
//
// Every case is bounded in COUNT by the key's own length — at most one key for an
// exact set, one per label for a hostname, eight for a card number, 129 for an
// address — and every case is bounded in BYTES by that length too, which is the
// property that matters and the one this function used to lack.
//
// A DOMAIN SUFFIX IS A SLICE, NEVER A JOIN. Splitting a host into labels and
// re-joining each tail allocates a fresh copy of every suffix: an L-label host
// costs O(L * len(key)) bytes, so one 8 KB dotted key materialised 16 MB and one
// [maxKey]-bounded resolve call of [maxKeys] such keys was 1.7 GB in a single
// request — on a one-replica deployment, an OOM every product on the host shares.
// A Go string is immutable, so host[o:] is the SAME bytes with a different header:
// walking the dot offsets gives the identical suffixes in O(L) headers over one
// backing array. The bound at the door ([maxKey], reference.go) and this shape are
// the two halves of one property — the door refuses a key no published list could
// carry, and this makes the work linear in whatever the door admits.
func candidates(set Set, key string) []string {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	switch set.Match {
	case MatchDomain:
		host := strings.ToLower(strings.Trim(key, "."))
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		// Every suffix that still has two labels in it, most specific first. A bare
		// public suffix is deliberately not a candidate: a deny on ".example" would
		// be a deny on a registry, not on a member.
		out := make([]string, 0, 8)
		for o := 0; ; {
			rest := host[o:]
			dot := strings.IndexByte(rest, '.')
			if dot < 0 {
				break
			}
			out = append(out, rest)
			o += dot + 1
		}
		if len(out) == 0 {
			out = append(out, host)
		}
		return out
	case MatchDigits:
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, key)
		if digits == "" {
			return nil
		}
		// An issuer identification number is at most eight digits; taking prefixes
		// longer than that from a full card number would be looking up the account.
		n := min(len(digits), 8)
		out := make([]string, 0, n)
		for i := n; i >= 1; i-- {
			out = append(out, digits[:i])
		}
		return out
	case MatchNet:
		addr, err := netip.ParseAddr(strings.Trim(key, "[]"))
		if err != nil {
			if p, ok := prefix(key); ok {
				return []string{p.String()}
			}
			return nil
		}
		addr = addr.Unmap()
		out := make([]string, 0, addr.BitLen()+1)
		for bits := addr.BitLen(); bits >= 0; bits-- {
			out = append(out, netip.PrefixFrom(addr, bits).Masked().String())
		}
		return out
	default:
		return []string{key}
	}
}

// look resolves a key against one built snapshot. It returns the entry and the
// candidate that matched, so the answer can say WHICH published member covered
// the key — a deny on tempbox.example is a different fact from a deny on
// mail.tempbox.example and an operator has to be able to tell them apart.
func (s *snap) look(key string) (Entry, string, bool) {
	if s == nil {
		return Entry{}, "", false
	}
	switch s.set.Match {
	case MatchPattern:
		if s.any != nil && !s.any.MatchString(key) {
			return Entry{}, "", false
		}
		for _, c := range s.scan {
			if c.re != nil && c.re.MatchString(key) {
				return c.entry, c.entry.Key, true
			}
		}
		return Entry{}, "", false
	case MatchRange:
		n, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(key), "AS")), 10, 64)
		if err != nil {
			return Entry{}, "", false
		}
		for _, c := range s.scan {
			if n >= c.lo && n <= c.hi {
				return c.entry, c.entry.Key, true
			}
		}
		return Entry{}, "", false
	default:
		for _, c := range candidates(s.set, key) {
			if e, ok := s.byKey[c]; ok {
				return e, c, true
			}
		}
		return Entry{}, "", false
	}
}

// plane holds the built snapshots. The map is replaced per set under a write
// lock and every snap is immutable, so a reader takes a pointer and is never
// racing a build.
//
// Bounded by the CATALOG, which is code: there is one snapshot per declared set
// and no caller can mint another. That is the difference between this and a
// per-tenant cache — there is no key an adversary controls.
type plane struct {
	mu   sync.RWMutex
	snap map[string]*snap
}

func newPlane() *plane { return &plane{snap: map[string]*snap{}} }

func (p *plane) get(set string) *snap {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snap[set]
}

func (p *plane) put(name string, s *snap) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snap[name] = s
}

// all returns every built snapshot, in catalog order.
func (p *plane) all() []*snap {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*snap, 0, len(p.snap))
	for _, s := range Catalog() {
		if got, ok := p.snap[s.Name]; ok {
			out = append(out, got)
		}
	}
	return out
}

// ReferenceAnswer is what the plane says about one key in one set — including, always,
// which version said it.
type ReferenceAnswer struct {
	// Set is the set consulted.
	Set string `json:"set"`
	// Key is the key as asked.
	Key string `json:"key"`
	// Hit is whether the key is a member. It is meaningful ONLY when Refusal is
	// empty: false with a refusal means the set could not be consulted, which is
	// not the same as the key being clean.
	Hit bool `json:"hit"`
	// From is override or baseline — which plane answered.
	From string `json:"from,omitempty"`
	// Matched is the member that covered the key, which for a domain or a network
	// is the enclosing entry rather than the key itself.
	Matched string `json:"matched,omitempty"`
	// Verdict is the tenant's own allow or deny, present only for an override.
	// The baseline never carries one: it states facts and leaves the decision to
	// the caller's policy.
	Verdict string `json:"verdict,omitempty"`
	// Value is what the publisher says about the member — class, operator,
	// scheme, region.
	Value map[string]string `json:"value,omitempty"`
	// Score is the published risk weight where the source expresses one.
	Score float64 `json:"score,omitempty"`
	// Version is the exact baseline version consulted, composed of each
	// contributing publisher and its content digest. It is what makes a decision
	// reproducible: an auditor takes this string and knows precisely what was
	// consulted.
	Version string `json:"version,omitempty"`
	// AsOf is when the oldest contributing publisher was current, RFC 3339.
	AsOf string `json:"asOf,omitempty"`
	// Age is how old that is, as a duration.
	Age string `json:"age,omitempty"`
	// Stale is whether the set is past its freshness bound. A stale set still
	// answers — yesterday's list beats none — and this is how a decision knows it
	// leaned on one.
	Stale bool `json:"stale,omitempty"`
	// Refusal is why the set could not be consulted, when it could not: never
	// loaded, held elsewhere, or a source we hold no licence for. Non-empty means
	// Hit must not be read as an answer.
	Refusal string `json:"refusal,omitempty"`
}

// answer resolves one key: the tenant's override first, then the baseline. own
// is the tenant's store, or nil for a caller with none yet — which is not an
// error, it is an organisation that has never written one.
func answer(set Set, s *snap, own *overrides, key string, now time.Time) (ReferenceAnswer, error) {
	a := ReferenceAnswer{Set: set.Name, Key: key}
	if s != nil {
		a.Version, a.Stale = s.version, s.stale(now)
		if !s.asOf.IsZero() {
			a.AsOf = s.asOf.UTC().Format(time.RFC3339)
			a.Age = s.age(now).Truncate(time.Minute).String()
		}
		a.Refusal = s.refusal
	} else {
		a.Refusal = "this set has never loaded, so it cannot tell a clean key from an unknown one"
	}

	// The override is consulted even for a seam or an unloaded set, and
	// deliberately: an organisation's own deny list is the one thing that still
	// works when the published source does not, and refusing to read it because
	// the baseline is unavailable would take away the only control left.
	if own != nil {
		if e, ok, err := own.pick(set.Name, candidates(set, key)); err != nil {
			return ReferenceAnswer{}, err
		} else if ok {
			a.Hit, a.From, a.Matched, a.Verdict = true, "override", e.Key, e.Verdict
			if e.Note != "" {
				a.Value = map[string]string{"note": e.Note}
			}
			a.Refusal = ""
			return a, nil
		}
	}

	if a.Refusal != "" {
		return a, nil
	}
	if e, matched, ok := s.look(key); ok {
		a.Hit, a.From, a.Matched, a.Value, a.Score = true, "baseline", matched, e.Value, e.Score
	} else {
		a.From = "baseline"
	}
	return a, nil
}
