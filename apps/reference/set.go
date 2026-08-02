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

// Grant is the BASIS on which a source's data may reach a tenant through this
// plane. It is a closed vocabulary rather than free text, and that is the whole
// point of the type.
//
// Terms used to be the only field, and it carried both kinds of sentence at
// once: "CC0-1.0" (a licence) and "operator-published range list" (a description
// of where a file came from). The gate over it could only ask whether the string
// was non-empty, so an unlicensed source wearing a licence field passed — the
// mirror image of the seam argument this plane is built on, where an unlicensed
// set REFUSES precisely because an absent one and an unlicensed one look
// identical from the outside.
//
// Splitting the kind from the citation makes the position machine-checkable and
// puts it on the wire, so which sources rest on a licence and which rest on an
// operator's own publication is an audit anyone can run rather than a judgement
// buried in a string.
type Grant string

const (
	// GrantLicence — the publisher states an explicit licence that permits
	// redistribution. Terms names it: CC0-1.0, MIT, CC BY 3.0 US.
	GrantLicence Grant = "licence"
	// GrantRegistry — the registry of record publishes the data for anyone to
	// consult, which is what a registry is for. Terms names the registry.
	GrantRegistry Grant = "registry"
	// GrantOperator — an operator's machine-readable statement about its OWN
	// network, published so third parties can filter and route by it. It is NOT a
	// licence and this value does not claim one: it says the data is a list of
	// factual prefixes the operator publishes for exactly this use, and Terms names
	// the publication. Stating that plainly is what lets someone review it.
	GrantOperator Grant = "operator"
	// GrantOwn — computed here, from a published standard or from fleet aggregates
	// that clear the k-anonymity floor. Nothing of anyone else's is redistributed.
	GrantOwn Grant = "own"
	// GrantNone — nothing reaches a tenant through this source at all: the
	// membership is held by the component that screens against it and this plane
	// carries only its freshness. The only honest basis for a set of kind attest.
	GrantNone Grant = "none"
)

// grants is the vocabulary as a set, so the gate has ONE definition to check
// against and a new value cannot be introduced by spelling it.
var grants = map[Grant]bool{GrantLicence: true, GrantRegistry: true, GrantOperator: true, GrantOwn: true, GrantNone: true}

// Redistributes reports whether this basis lets a publisher's bytes reach a
// tenant. It is the predicate a fetched source must satisfy.
func (g Grant) Redistributes() bool {
	return g == GrantLicence || g == GrantRegistry || g == GrantOperator
}

// Source is one publisher of one set: where it is, on what basis we may pass it
// on, and how its bytes become entries.
type Source struct {
	// Name is the publisher, stable across versions — it is the key freshness is
	// tracked per.
	Name string
	// Origin is the exact URL fetched, so an auditor can take the same bytes.
	Origin string
	// Basis is the KIND of permission this data reaches a tenant under, from a
	// closed vocabulary. Required: the zero value is not a basis, and the catalog
	// gate refuses it.
	Basis Grant
	// Terms is the CITATION the basis points at — the licence identifier, the
	// registry, or the operator publication. Required: a basis with nothing behind
	// it is an assertion.
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
				Basis:  GrantLicence,
				Terms:  "CC0-1.0",
				parse:  parseDisposable,
			}},
		},
		{
			Name:   "net",
			Kind:   KindFetch,
			What:   "IP ranges with a known character: cloud and hosting estates, Tor exits, and the addresses no public host may use.",
			Match:  MatchNet,
			MaxAge: weekly,
			Sources: []Source{
				{Name: "aws", Origin: "https://ip-ranges.amazonaws.com/ip-ranges.json", Basis: GrantOperator, Terms: "Amazon's own ip-ranges.json, published machine-readable so third parties can filter and route by it; no separate licence is stated and none is claimed here", parse: parseAWS},
				{Name: "gcp", Origin: "https://www.gstatic.com/ipranges/cloud.json", Basis: GrantOperator, Terms: "Google's own cloud.json, published machine-readable so third parties can filter and route by it; no separate licence is stated and none is claimed here", parse: parseGCP},
				{Name: "oracle", Origin: "https://docs.oracle.com/iaas/tools/public_ip_ranges.json", Basis: GrantOperator, Terms: "Oracle's own public_ip_ranges.json, published machine-readable so third parties can filter and route by it; no separate licence is stated and none is claimed here", parse: parseOracle},
				{Name: "fastly", Origin: "https://api.fastly.com/public-ip-list", Basis: GrantOperator, Terms: "Fastly's own public IP list API, published so third parties can filter and route by it; no separate licence is stated and none is claimed here", parse: parseFastly},
				{Name: "cloudflare4", Origin: "https://www.cloudflare.com/ips-v4", Basis: GrantOperator, Terms: "Cloudflare's own published IPv4 range list, served for third parties to allow traffic by; no separate licence is stated and none is claimed here", parse: parseCIDRs("cloudflare", "hosting", "cloudflare")},
				{Name: "cloudflare6", Origin: "https://www.cloudflare.com/ips-v6", Basis: GrantOperator, Terms: "Cloudflare's own published IPv6 range list, served for third parties to allow traffic by; no separate licence is stated and none is claimed here", parse: parseCIDRs("cloudflare", "hosting", "cloudflare")},
				{Name: "linode", Origin: "https://geoip.linode.com/", Basis: GrantOperator, Terms: "RFC 8805 geofeed self-published by the operator, which exists so third parties can read it; no separate licence is stated and none is claimed here", parse: parseLinode},
				{Name: "digitalocean", Origin: "https://digitalocean.com/geo/google.csv", Basis: GrantOperator, Terms: "RFC 8805 geofeed self-published by the operator, which exists so third parties can read it; no separate licence is stated and none is claimed here", parse: parseDigitalOcean},
				{Name: "tor", Origin: "https://check.torproject.org/torbulkexitlist", Basis: GrantLicence, Terms: "CC BY 3.0 US — the Tor Project publishes the bulk exit list for operator use under its site licence", parse: parseTor},
				{Name: "reserved4", Origin: "https://www.iana.org/assignments/iana-ipv4-special-registry/iana-ipv4-special-registry-1.csv", Basis: GrantRegistry, Terms: "IANA IPv4 Special-Purpose Address Registry, the registry of record", parse: parseSpecial},
				{Name: "reserved6", Origin: "https://www.iana.org/assignments/iana-ipv6-special-registry/iana-ipv6-special-registry-1.csv", Basis: GrantRegistry, Terms: "IANA IPv6 Special-Purpose Address Registry, the registry of record", parse: parseSpecial},
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
				Basis:  GrantLicence,
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
				Basis:  GrantRegistry,
				Terms:  "IANA Autonomous System Number Registry, the registry of record",
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
				Basis:   GrantOwn,
				Terms:   "computed here from structural facts; no issuer database is licensed or held",
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
				Basis:   GrantOwn,
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
				{Name: "OFAC", Origin: "luxfi/aml pkg/screen", Basis: GrantNone, Terms: "receipt only; the designations stay with the engine that screens"},
				{Name: "UN", Origin: "luxfi/aml pkg/screen", Basis: GrantNone, Terms: "receipt only; the designations stay with the engine that screens"},
				{Name: "EU", Origin: "luxfi/aml pkg/screen", Basis: GrantNone, Terms: "receipt only; the designations stay with the engine that screens"},
				{Name: "OFSI", Origin: "luxfi/aml pkg/screen", Basis: GrantNone, Terms: "receipt only; the designations stay with the engine that screens"},
			},
		},
		{
			Name:   "jurisdiction",
			Kind:   KindAttest,
			What:   "Freshness of the higher-risk country listing the screening engine evaluates against.",
			Match:  MatchExact,
			MaxAge: monthly,
			Sources: []Source{
				{Name: "listing", Origin: "luxfi/aml pkg/reference", Basis: GrantNone, Terms: "receipt only; the listing stays with the engine that evaluates it"},
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
