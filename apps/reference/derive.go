package reference

// derive.go computes the two sets nobody publishes for us: the structural card
// table, and the browser identities the fleet has seen under enough separate
// organisations that no single one of them could have produced the observation.
//
// THE BOUNDARY THIS FILE DEFENDS. Everything a tenant reads from the shared
// baseline must be either PUBLIC (someone else published it under a licence) or
// AGGREGATE (a statistic that no single organisation could have produced alone).
// The device set is the only place we compute the second kind, so the whole
// argument lives here and is enforced twice — in the statement's HAVING and again
// on the way out — because a row written before a gate existed is still a row.
//
// The floor is deliberately high. Publishing "this browser was seen by two
// organisations" tells the first of them something about the second; publishing
// "this browser was seen by twenty-five" tells them about a population. The
// second is a statistic and the first is a disclosure, and the line between them
// is the only thing standing between a shared baseline and a data-sharing
// agreement nobody signed.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The k-anonymity floor for anything derived from fleet traffic.
//
// Orgs is the number of DISTINCT organisations that must have contributed
// before a key may be published. Twenty-five is chosen so that an adversary who
// controls several organisations still cannot attribute the row: to learn about
// one contributor you must know every other contributor's value.
//
// Rows bounds the other direction. Twenty-five organisations contributing one
// observation each is twenty-five readable facts, not a population.
const (
	Orgs = 25
	Rows = 1000
)

// Publishable is the k-anonymity gate as a PURE PREDICATE. It is exported
// because it is the ONE definition: the statement that computes a derived set
// binds these numbers, the reader re-checks them, and any other plane that
// publishes a cross-organisation aggregate must call this rather than restate
// the constants — two spellings of a floor is one floor that can drift, and the
// drift always favours the weaker.
func Publishable(orgs uint32, n uint64) bool { return orgs >= Orgs && n >= Rows }

// producer is everything a local source needs: a bounded context, the instant
// the run is dated at, and a reader for the shared event plane. The reader is a
// function rather than a package call so a test can produce a set without a
// warehouse.
type producer struct {
	ctx   context.Context
	now   time.Time
	query func(ctx context.Context, q string, args ...any) ([]map[string]any, error)
}

// deviceWindow is how far back the device aggregate looks. Thirty days is long
// enough for a shared browser to accumulate the twenty-five organisations the
// floor requires, and short enough that a device that stopped being shared
// leaves the set.
const deviceWindow = 30 * 24 * time.Hour

// deviceStatement is the ONE statement that computes the device aggregate, and
// it is a PACKAGE CONSTANT: nothing a caller sends reaches it, as an identifier
// or otherwise. Its five placeholders bind, in order, the window start, the
// window end, the reserved anonymous tenant, the organisation floor and the
// observation floor.
//
// The anonymous lane is excluded at the source. The reserved `$public` tenant is
// where the event door files credential-less writes, so counting it would let an
// unauthenticated stranger push any browser identity over the floor and into
// every tenant's baseline. Excluding it in the FROM rather than in a later
// filter is the difference between a refusal and a place to forget.
const deviceStatement = `
	SELECT id, orgs, n FROM (
	  SELECT anonymous_id AS id, uniqExact(org) AS orgs, count() AS n
	  FROM event.fact
	  WHERE signal = 'act' AND time >= ? AND time < ? AND anonymous_id != '' AND org != ?
	  GROUP BY anonymous_id
	)
	WHERE orgs >= ? AND n >= ?
	ORDER BY orgs DESC, id
	LIMIT ` + deviceCap + deviceBudget

// deviceCap bounds the result so a warehouse that suddenly matches everything
// costs a truncated set rather than the process. It is a string constant spliced
// into the statement because a LIMIT cannot bind, and it is not reachable from
// any caller.
const deviceCap = "50000"

// deviceBudget bounds what the aggregation may SPEND, which the LIMIT does not.
//
// The LIMIT applies to the outer select — the rows that survive the floor — and
// says nothing about the inner GROUP BY, which visits every distinct browser
// identity the whole fleet saw in [deviceWindow] before a single row is filtered.
// On a busy event plane that is a grouping over hundreds of millions of keys, and
// it runs against the one warehouse analytics, insights, sentry, commerce and
// gateway usage all share, roughly daily and unattended. Housekeeping for one
// reference set must not be able to stall the store every other plane reads from.
//
// So the statement states its own budget: it spills to disk rather than growing,
// it stops rather than spilling forever, and it gives up rather than running past
// the window it is allowed. Exceeding a budget fails THIS take, which is already
// a case this plane handles — the previous version stands and ages out visibly.
const deviceBudget = `
	SETTINGS max_execution_time = 300,
	         max_memory_usage = 4000000000,
	         max_bytes_before_external_group_by = 2000000000,
	         max_bytes_before_external_sort = 2000000000`

// The plane holds five signals in one table, so a read that does not name one
// counts errors and spans as product events. This aggregate is the only
// cross-tenant reader, so a phantom identity inflates both k-anonymity floors.
//
// publicTenant is the reserved org the event door files credential-less writes
// under (apps/analytics/event.go). It is not a customer and it never
// contributes to an aggregate.
const publicTenant = "$public"

// produceDevice computes the browser identities the fleet sees across many
// organisations.
//
// THE KEY IS A DIGEST, NEVER THE IDENTIFIER. The shared table holds
// sha256(anonymous_id) and the lookup path digests the caller's value the same
// way, so no browser identifier is ever written into a store every tenant reads.
// The digest is not salted and does not need to be: the input is a
// client-minted opaque identifier with the entropy of a UUID, so there is no
// dictionary to run against it — and the k-anonymity floor means the only
// identifiers published at all are ones twenty-five organisations already share.
//
// The count is BUCKETED rather than exact. "Seen by 25 to 99 organisations" is
// the signal a rule wants; the exact number is a fingerprint of the population
// and buys nothing.
func produceDevice(p producer) ([]Entry, error) {
	if p.query == nil {
		return nil, fmt.Errorf("reference: the device aggregate needs the event plane, which is not connected")
	}
	from := p.now.Add(-deviceWindow).UTC()
	rows, err := p.query(p.ctx, deviceStatement, from, p.now.UTC(), publicTenant, Orgs, Rows)
	if err != nil {
		return nil, fmt.Errorf("reference: device aggregate: %w", err)
	}
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		id, _ := r["id"].(string)
		orgs := count32(r["orgs"])
		n := count64(r["n"])
		// The SECOND gate. The statement already refused anything below the floor;
		// this refuses a row that predates the gate, or one a future edit lets
		// through, because the cost of the two disagreeing is borne by the tenant
		// whose data leaks rather than by whoever edited the statement.
		if id == "" || !Publishable(orgs, n) {
			continue
		}
		sum := sha256.Sum256([]byte(id))
		out = append(out, Entry{
			Key:   hex.EncodeToString(sum[:]),
			Value: map[string]string{"class": "shared", "orgs": bucket(orgs)},
			Orgs:  orgs,
			N:     n,
		})
	}
	return out, nil
}

// bucket renders an organisation count as the band a rule should read. Bands
// rather than numbers: the exact count of organisations sharing one browser is
// a more precise description of the population than anyone needs.
func bucket(orgs uint32) string {
	switch {
	case orgs >= 1000:
		return "1000+"
	case orgs >= 100:
		return "100-999"
	default:
		return "25-99"
	}
}

// count32 and count64 read a warehouse count column, which arrives as whichever
// numeric type the driver chose.
func count32(v any) uint32 { return uint32(count64(v)) }

func count64(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case uint32:
		return uint64(n)
	case int64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case int:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case float64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case string:
		u, err := strconv.ParseUint(strings.TrimSpace(n), 10, 64)
		if err != nil {
			return 0
		}
		return u
	default:
		return 0
	}
}

// scheme is one card scheme and the issuer identification number prefixes it
// publishes, with the account lengths it issues at.
type scheme struct {
	name     string
	prefixes []string
	lengths  []int
}

// schemes is the structural card table: which scheme an issuer identification
// number belongs to, from the major industry identifier of ISO/IEC 7812 and the
// prefix ranges each scheme publishes.
//
// This is COMPUTED rather than downloaded on purpose, and it is the one set
// where that is the right answer. These prefixes are structural facts that have
// been stable for decades, they are published by the schemes themselves, and no
// database is licensed to state them. What a licensed database adds — the
// institution behind a prefix, its country, whether the product is debit,
// credit or prepaid — is exactly what the issuer seam declares we do not have.
var schemes = []scheme{
	{"visa", []string{"4"}, []int{13, 16, 19}},
	{"mastercard", []string{"51", "52", "53", "54", "55"}, []int{16}},
	{"mastercard", span2221to2720(), []int{16}},
	{"amex", []string{"34", "37"}, []int{15}},
	{"discover", []string{"6011", "65"}, []int{16, 19}},
	{"discover", spanOf(644, 649), []int{16, 19}},
	{"jcb", spanOf(3528, 3589), []int{16, 19}},
	{"unionpay", []string{"62", "81"}, []int{16, 17, 18, 19}},
	{"diners", []string{"36", "38", "39"}, []int{14, 16, 19}},
	{"diners", spanOf(300, 305), []int{14, 16, 19}},
	{"maestro", []string{"5018", "5020", "5038", "5893", "6304", "6759", "6761", "6762", "6763"}, []int{12, 13, 14, 15, 16, 17, 18, 19}},
	{"mir", spanOf(2200, 2204), []int{16, 17, 18, 19}},
	{"elo", []string{"4011", "4312", "4389", "4514", "4573", "5041", "5066", "5090", "6277", "6362", "6363", "6504", "6505", "6516", "6550"}, []int{16}},
	{"troy", []string{"9792"}, []int{16}},
	{"rupay", []string{"60", "6521", "6522", "8171", "8172"}, []int{16}},
}

// spanOf renders an inclusive numeric prefix range as its literal members.
func spanOf(lo, hi int) []string {
	out := make([]string, 0, hi-lo+1)
	for n := lo; n <= hi; n++ {
		out = append(out, strconv.Itoa(n))
	}
	return out
}

// span2221to2720 is the Mastercard two-series. It is expressed as its 500
// members rather than as a range test so that every entry in the set is a
// literal prefix and one matcher serves the whole set.
func span2221to2720() []string { return spanOf(2221, 2720) }

// produceBIN renders the structural table into entries. It takes no producer
// input: the answer is the same on every run, which is why the version digest
// over it is stable and a refresh that changes nothing is visibly a refresh
// that changed nothing.
func produceBIN(producer) ([]Entry, error) {
	out := make([]Entry, 0, 1024)
	for _, s := range schemes {
		lengths := make([]string, 0, len(s.lengths))
		for _, n := range s.lengths {
			lengths = append(lengths, strconv.Itoa(n))
		}
		joined := strings.Join(lengths, ",")
		for _, p := range s.prefixes {
			out = append(out, Entry{
				Key:   p,
				Value: map[string]string{"scheme": s.name, "lengths": joined},
			})
		}
	}
	return out, nil
}
