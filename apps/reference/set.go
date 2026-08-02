package reference

// set.go is the CATALOG: every reference set this plane can answer from, where
// each one comes from, and under what terms we are allowed to hold it.
//
// The catalog is code, not configuration. A set that exists is a set someone
// wrote a parser and a licence line for, and a set whose source we may not
// redistribute is DECLARED here as a seam rather than quietly omitted — an
// absent set and an unlicensed one look identical from the outside, and only one
// of them is a decision.
//
// THE UNIT OF VERSION AND FRESHNESS IS THE SOURCE, NOT THE SET. A set is the
// union of its sources, and each source carries its own version, its own as-of
// and its own failure. That is not an implementation detail: `net` draws on
// eight publishers, and a set-wide version would make one publisher's outage
// either block every other publisher's update or silently shrink the set. Per
// source, a publisher that stops answering ages out visibly on its own row while
// the rest stay current — which is the same shape luxfi/aml pkg/screen arrived at
// for the four sanctions publishers, for the same reason.

import (
	"time"
)

// Kind is how a set's baseline comes to exist. Four kinds and no more, because
// each one implies a different answer to "what does silence mean here".
type Kind string

const (
	// KindFetch is downloaded from a published source. An EMPTY fetch set is a
	// failure, never a fact: no publisher's list of disposable domains or hosting
	// ranges is empty, so zero entries means the fetch or the parse is wrong.
	KindFetch Kind = "fetch"
	// KindLocal is computed here — a structural table that follows from a
	// published standard, or an aggregate over fleet traffic. An empty local set
	// IS a fact: "no device is shared across enough organisations to publish" is a
	// true statement about the world.
	KindLocal Kind = "local"
	// KindAttest is held by the component that screens against it. This plane
	// records that component's load receipt and answers ONLY freshness — never
	// membership, because a second copy of a sanctions list is a second thing to
	// keep current and the two would disagree on the day it mattered.
	KindAttest Kind = "attest"
	// KindSeam is declared and NOT held: the source needs a licence we do not
	// have. Every lookup against it refuses. A seam is louder than an omission,
	// which is the whole reason it is in the catalog.
	KindSeam Kind = "seam"
)

// Match is how a key is tested against a set's entries. Five matchers, each a
// pure function over a built snapshot (see resolve.go).
type Match string

const (
	// MatchExact is equality on the normalised key: an ASN, a device digest, a
	// publisher name.
	MatchExact Match = "exact"
	// MatchDomain walks a hostname up its labels — mail.tempbox.example matches an
	// entry for tempbox.example — because a disposable provider's subdomains are
	// disposable too.
	MatchDomain Match = "domain"
	// MatchNet is longest-prefix on an IP address against CIDR entries.
	MatchNet Match = "net"
	// MatchDigits is longest numeric prefix, which is how an issuer identification
	// number addresses a card scheme.
	MatchDigits Match = "digits"
	// MatchPattern tests the key against each entry as a regular expression,
	// which is how a crawler declares itself in a user-agent string.
	MatchPattern Match = "pattern"
	// MatchRange is containment in a closed numeric interval, which is how a
	// number registry delegates autonomous system numbers in blocks.
	MatchRange Match = "range"
)

// Set is one published reference set: what it holds, how fresh it has to be, and
// where its entries lawfully come from.
type Set struct {
	// Name is the address: /v1/ml/reference/<name>. One word, lower case.
	Name string
	// Kind decides what an empty set means and whether membership is held here.
	Kind Kind
	// What is one sentence an operator can read.
	What string
	// Match is how a key is tested against this set's entries.
	Match Match
	// MaxAge is how old a source's newest successful load may be before this set
	// is STALE. Past it the set still answers, and every answer says so — a stale
	// list answers "not listed" for everything and reads exactly like a clean
	// world, which is the failure this whole plane exists to make visible.
	MaxAge time.Duration
	// Sources are the publishers this set draws on. Empty for local and seam sets.
	Sources []Source
	// Refusal is why a seam set cannot be consulted. Non-empty ONLY for KindSeam,
	// and it names the licence we do not hold rather than saying "unavailable".
	Refusal string
}

// Source is one publisher of one set: where it is, what it costs us in terms,
// and how its bytes become entries.
type Source struct {
	// Name is the publisher, stable across versions — it is the key freshness is
	// tracked per.
	Name string
	// Origin is the exact URL fetched, so an auditor can take the same bytes.
	Origin string
	// Terms is the licence or permission we redistribute this data under. It is a
	// required field: a source with no stated terms does not go in the catalog.
	Terms string
	// parse turns the fetched bytes into entries. Nil for a local source, whose
	// entries come from produce.
	parse func([]byte) ([]Entry, error)
	// produce computes a local source's entries. Nil for a fetched source.
	produce func(ctx producer) ([]Entry, error)
}

// Entry is one member of a set: the key, the facts the publisher states about
// it, and — for a set derived from fleet traffic — the two counts that prove the
// row could not have come from one organisation.
type Entry struct {
	// Key is the normalised member: a domain, a CIDR, an IIN prefix, a digest.
	Key string
	// Value is what the publisher says about it — class, operator, region,
	// scheme. Facts, never a verdict: the verdict is the caller's policy, and a
	// baseline that shipped verdicts would be making every tenant's policy for it.
	Value map[string]string
	// Score is a risk weight in [0,1] where the source expresses one, else zero.
	Score float64
	// Orgs and N are the k-anonymity evidence for a derived entry: how many
	// distinct organisations and how many observations produced it. Zero for a
	// published source, where the evidence is the licence instead.
	Orgs uint32
	N    uint64
}

// Freshness bounds. A day for the lists that move daily, a week for the ones
// that move monthly. They are separate constants rather than one because the
// question "is this stale" has a different honest answer per publisher cadence.
const (
	daily   = 36 * time.Hour
	weekly  = 8 * 24 * time.Hour
	monthly = 40 * 24 * time.Hour
)

// Catalog is every set, in a stable order. It is the ONE declaration: the routes
// project it, the refresh walks it, and a lookup for a name not in it is a 404
// rather than an invented empty answer.
//
// LAWFULNESS IS A FIELD, NOT A FOOTNOTE. Every fetched source states the terms
// it is redistributed under, and every source we would want but may not have is
// present as a seam naming the licence we lack. The three seams below are the
// honest state of the art: politically-exposed-person listings, issuer
// identification tables and commercial network reputation are all sold, and the
// free copies in circulation are either non-commercial-only or of unstated
// provenance. Embedding one of those would put a licence breach inside a
// compliance product.
func Catalog() []Set {
	return []Set{
		{
			Name:   "domain",
			Kind:   KindFetch,
			What:   "Email domains that hand out throwaway inboxes.",
			Match:  MatchDomain,
			MaxAge: weekly,
			Sources: []Source{{
				Name:   "disposable",
				Origin: "https://raw.githubusercontent.com/disposable-email-domains/disposable-email-domains/main/disposable_email_blocklist.conf",
				Terms:  "CC0-1.0",
				parse:  parseLines("disposable", map[string]string{"class": "disposable"}),
			}},
		},
		{
			Name:   "net",
			Kind:   KindFetch,
			What:   "IP ranges with a known character: cloud and hosting estates, Tor exits, and the addresses no public host may use.",
			Match:  MatchNet,
			MaxAge: weekly,
			Sources: []Source{
				{Name: "aws", Origin: "https://ip-ranges.amazonaws.com/ip-ranges.json", Terms: "operator-published range list", parse: parseAWS},
				{Name: "gcp", Origin: "https://www.gstatic.com/ipranges/cloud.json", Terms: "operator-published range list", parse: parseGCP},
				{Name: "oracle", Origin: "https://docs.oracle.com/iaas/tools/public_ip_ranges.json", Terms: "operator-published range list", parse: parseOracle},
				{Name: "fastly", Origin: "https://api.fastly.com/public-ip-list", Terms: "operator-published range list", parse: parseFastly},
				{Name: "cloudflare4", Origin: "https://www.cloudflare.com/ips-v4", Terms: "operator-published range list", parse: parseCIDRs("cloudflare", "hosting", "cloudflare")},
				{Name: "cloudflare6", Origin: "https://www.cloudflare.com/ips-v6", Terms: "operator-published range list", parse: parseCIDRs("cloudflare", "hosting", "cloudflare")},
				{Name: "linode", Origin: "https://geoip.linode.com/", Terms: "operator-published range list", parse: parseLinode},
				{Name: "digitalocean", Origin: "https://digitalocean.com/geo/google.csv", Terms: "operator-published range list", parse: parseDigitalOcean},
				{Name: "tor", Origin: "https://check.torproject.org/torbulkexitlist", Terms: "Tor Project bulk exit list, published for operator use (CC BY 3.0 US)", parse: parseTor},
				{Name: "reserved4", Origin: "https://www.iana.org/assignments/iana-ipv4-special-registry/iana-ipv4-special-registry-1.csv", Terms: "IANA public registry", parse: parseSpecial},
				{Name: "reserved6", Origin: "https://www.iana.org/assignments/iana-ipv6-special-registry/iana-ipv6-special-registry-1.csv", Terms: "IANA public registry", parse: parseSpecial},
			},
		},
		{
			Name:   "crawler",
			Kind:   KindFetch,
			What:   "User-agent patterns by which an automated client declares itself.",
			Match:  MatchPattern,
			MaxAge: monthly,
			Sources: []Source{{
				Name:   "patterns",
				Origin: "https://raw.githubusercontent.com/monperrus/crawler-user-agents/master/crawler-user-agents.json",
				Terms:  "MIT",
				parse:  parseCrawlers,
			}},
		},
		{
			Name:   "asn",
			Kind:   KindFetch,
			What:   "Autonomous system numbers and the registry each block was delegated to.",
			Match:  MatchRange,
			MaxAge: monthly,
			Sources: []Source{{
				Name:   "iana",
				Origin: "https://www.iana.org/assignments/as-numbers/as-numbers-1.csv",
				Terms:  "IANA public registry",
				parse:  parseASN,
			}},
		},
		{
			Name:   "bin",
			Kind:   KindLocal,
			What:   "Issuer identification number prefixes and the card scheme each one belongs to.",
			Match:  MatchDigits,
			MaxAge: monthly,
			Sources: []Source{{
				Name:    "structure",
				Origin:  "ISO/IEC 7812 major industry identifier and the schemes' own published prefix ranges",
				Terms:   "structural facts, no database licensed",
				produce: produceBIN,
			}},
		},
		{
			Name:   "device",
			Kind:   KindLocal,
			What:   "Browser identities seen under enough separate organisations that no single one of them could have produced the observation.",
			Match:  MatchExact,
			MaxAge: daily,
			Sources: []Source{{
				Name:    "fleet",
				Origin:  "aggregate over the shared event plane",
				Terms:   "our own aggregate, published only above the k-anonymity floor",
				produce: produceDevice,
			}},
		},
		{
			Name:   "sanction",
			Kind:   KindAttest,
			What:   "Freshness of the designation lists the screening engine holds — which publisher, how many designations, how long ago.",
			Match:  MatchExact,
			MaxAge: daily,
			Sources: []Source{
				{Name: "OFAC", Origin: "luxfi/aml pkg/screen", Terms: "receipt only; the designations stay with the engine that screens"},
				{Name: "UN", Origin: "luxfi/aml pkg/screen", Terms: "receipt only; the designations stay with the engine that screens"},
				{Name: "EU", Origin: "luxfi/aml pkg/screen", Terms: "receipt only; the designations stay with the engine that screens"},
				{Name: "OFSI", Origin: "luxfi/aml pkg/screen", Terms: "receipt only; the designations stay with the engine that screens"},
			},
		},
		{
			Name:   "jurisdiction",
			Kind:   KindAttest,
			What:   "Freshness of the higher-risk country listing the screening engine evaluates against.",
			Match:  MatchExact,
			MaxAge: monthly,
			Sources: []Source{
				{Name: "listing", Origin: "luxfi/aml pkg/reference", Terms: "receipt only; the listing stays with the engine that evaluates it"},
			},
		},
		{
			Name:    "pep",
			Kind:    KindSeam,
			What:    "Politically exposed persons and their close associates.",
			Match:   MatchExact,
			MaxAge:  weekly,
			Refusal: "no politically-exposed-person listing is held: the comprehensive ones are sold under commercial terms and the open one is licensed for non-commercial use only. Screening against this set would be screening against nothing, so it refuses instead.",
		},
		{
			Name:    "issuer",
			Kind:    KindSeam,
			What:    "Card issuer identity behind an issuer identification number — institution, country, product and funding type.",
			Match:   MatchDigits,
			MaxAge:  monthly,
			Refusal: "no issuer identification database is held: the authoritative tables are licensed by the card schemes and the freely circulating copies state no provenance. The scheme a prefix belongs to is structural and is answered by the bin set; the institution behind it is not.",
		},
		{
			Name:    "reputation",
			Kind:    KindSeam,
			What:    "Commercial network reputation — per-address and per-autonomous-system abuse scoring.",
			Match:   MatchNet,
			MaxAge:  daily,
			Refusal: "no commercial network reputation feed is held. Deriving one from our own traffic needs an address-to-autonomous-system map, and every map we can reach carries redistribution terms we have not accepted. The net set answers what an operator publishes about its own estate; it does not score behaviour.",
		},
	}
}

// byName resolves a set by its address. A name the catalog does not carry is not
// an empty set — it is a name this plane never published.
func byName(name string) (Set, bool) {
	for _, s := range Catalog() {
		if s.Name == name {
			return s, true
		}
	}
	return Set{}, false
}

// source resolves one publisher within a set.
func (s Set) source(name string) (Source, bool) {
	for _, src := range s.Sources {
		if src.Name == name {
			return src, true
		}
	}
	return Source{}, false
}

// emptyIsFact reports whether a set with zero entries is telling the truth.
//
// It is derived from Kind rather than declared per set, so the two cannot
// disagree: a downloaded list is never legitimately empty, and a computed one
// frequently is.
func (s Set) emptyIsFact() bool { return s.Kind != KindFetch }
