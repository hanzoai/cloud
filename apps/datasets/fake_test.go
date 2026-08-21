package datasets

// fake_test.go is a store that behaves like the warehouse for exactly the
// statements this plane emits, and REFUSES every statement it does not
// recognise.
//
// That refusal is the whole design. A fake that ignored the WHERE clause would
// pass a tenant-isolation test even if the plane had stopped binding the tenant —
// the test would prove nothing while looking green. So this one PARSES the
// predicate out of the statement text and evaluates it against the bound
// arguments, in order. Drop `org = ?` from any statement in plane.go and the
// isolation tests below fail, because this store will hand back every tenant's
// rows exactly as a real one would.
//
// It also emulates the two engine behaviours the plane's correctness rests on:
// ReplacingMergeTree(seq) FINAL (the row with the greatest seq survives, which is
// what makes a published version undisplaceable) and DROP PARTITION (whole-key
// removal, which is what makes disposal per tenant).
//
// The one thing it does NOT reproduce is cityHash64; it stands a Go hash in for
// it. What is under test is that the plane binds the seed and the share and
// samples by SUBJECT, not the store's choice of hash function.

import (
	"context"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// featRow is one row of the SOURCE surface this plane reads but does not own.
type featRow struct {
	Org     string
	Kind    string
	Subject string
	Bucket  time.Time
	Value   map[string]float64
}

// call is one statement the plane ran, kept so a test can assert the shape of
// what was sent and not only the answer that came back.
type call struct {
	Stmt string
	Args []any
}

type fake struct {
	mu sync.Mutex

	down bool
	ttl  string
	// fail, when set, is returned from every read as the DRIVER would return it —
	// a message carrying the statement, the table and the host. It is how a test
	// reaches the one thing a fake store cannot otherwise produce: an error this
	// package did not write.
	fail error
	// leak, when set, makes the readers ignore the predicate — a store that hands
	// back another tenant's rows. It exists so the plane's own outward check on the
	// way OUT of a read is exercised, rather than being a line nothing reaches.
	leak bool

	feature  []featRow
	manifest []map[string]any
	rows     []map[string]any

	calls []call
	// unknown records any statement this fake could not parse. A test asserts it
	// is empty, so a new statement shape cannot be silently half-tested.
	unknown []string
}

func (f *fake) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.down
}

// spent refuses a statement whose context is already over, exactly as the driver
// does: a real client checks the deadline before it writes a byte and returns
// context.DeadlineExceeded having sent nothing.
//
// The fake honours the context because one of this plane's properties is about
// nothing but contexts — a job whose work ran out of time must still be able to
// WRITE that it ran out of time. A store that ignored the deadline would make
// that test pass with the two contexts fused back into one.
func spent(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: the statement was not sent: %v", errStore, err)
	}
	return nil
}

func (f *fake) record(stmt string, args []any) {
	f.calls = append(f.calls, call{Stmt: squash(stmt), Args: append([]any(nil), args...)})
}

// squash collapses the whitespace a multi-line constant carries, so an assertion
// about a statement reads as one line.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func (f *fake) Exec(ctx context.Context, stmt string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := spent(ctx); err != nil {
		return err
	}
	if f.down {
		return errStore
	}
	f.record(stmt, args)
	s := squash(stmt)
	switch {
	case strings.HasPrefix(s, "CREATE DATABASE"), strings.HasPrefix(s, "CREATE TABLE"):
		return nil
	case strings.HasPrefix(s, "INSERT INTO "+manifestTable):
		return f.insert(&f.manifest, manifestColumns, args)
	case strings.HasPrefix(s, "INSERT INTO "+rowTable):
		return f.insert(&f.rows, rowColumns, args)
	case strings.HasPrefix(s, "ALTER TABLE "+rowTable+" DROP PARTITION"):
		f.rows = drop(f.rows, "org", "dataset", args)
		return nil
	case strings.HasPrefix(s, "ALTER TABLE "+manifestTable+" DROP PARTITION"):
		f.manifest = drop(f.manifest, "org", "name", args)
		return nil
	}
	f.unknown = append(f.unknown, s)
	return fmt.Errorf("fake store: unrecognised statement %q", s)
}

// insert appends one or more value groups, decoding them positionally against the
// column list the plane declared.
func (f *fake) insert(into *[]map[string]any, cols []string, args []any) error {
	if len(args)%len(cols) != 0 {
		return fmt.Errorf("fake store: %d args for %d columns", len(args), len(cols))
	}
	for start := 0; start < len(args); start += len(cols) {
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = args[start+i]
		}
		*into = append(*into, row)
	}
	return nil
}

// drop removes every row whose two partition components match the bound values.
// It is whole-key removal, exactly like the engine's, so a disposal that named
// the wrong key would remove the wrong rows here too.
func drop(in []map[string]any, a, b string, args []any) []map[string]any {
	if len(args) != 2 {
		return in
	}
	out := in[:0:0]
	for _, r := range in {
		if fmt.Sprint(r[a]) == fmt.Sprint(args[0]) && fmt.Sprint(r[b]) == fmt.Sprint(args[1]) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (f *fake) Query(ctx context.Context, stmt string, args ...any) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := spent(ctx); err != nil {
		return nil, err
	}
	if f.down {
		return nil, errStore
	}
	f.record(stmt, args)
	if f.fail != nil {
		return nil, f.fail
	}
	s := squash(stmt)
	switch {
	case strings.Contains(s, "FROM system.tables"):
		if f.ttl == "" {
			return nil, nil
		}
		return []map[string]any{{"engine_full": "SummingMergeTree ORDER BY (org) " + f.ttl}}, nil
	case strings.Contains(s, "FROM "+manifestTable):
		return f.readManifest(s, args)
	case strings.Contains(s, "FROM "+rowTable):
		return f.readRows(s, args)
	case strings.Contains(s, "FROM "+sourceTable):
		return f.readSource(s, args)
	}
	f.unknown = append(f.unknown, s)
	return nil, fmt.Errorf("fake store: unrecognised query %q", s)
}

// ── the predicate evaluator ──────────────────────────────────────────────────

// term is one `col <op> ?` conjunct of a WHERE clause.
type term struct {
	col string
	op  string
}

var termRe = regexp.MustCompile(`^([a-z_]+) (=|>=|<) \?$`)

// sampleRe is the seeded membership predicate. Its two arguments are the seed and
// the share.
var sampleRe = regexp.MustCompile(`^cityHash64\(\?, subject\) % (\d+) < \?$`)

// sizeRe is the row-size bound — [representable]. It consumes one argument, and
// the fake EVALUATES it: a store that ignored it would hand back the oversized
// subjects the plane believes it excluded, so every byte-bound test below would
// pass with the bound deleted. That is the exact failure mode a fixture is for.
var sizeRe = regexp.MustCompile(`^length\(subject_kind\) \+ length\(subject\) <= \?$`)

// sizeCol is the synthetic column the size bound reads. The source readers put the
// subject identity's byte length in the row under this name, so the bound is
// evaluated by the same generic [match] as every other conjunct rather than by a
// special case that could drift from it.
const sizeCol = "__subject_bytes__"

// pred is a parsed WHERE clause: its conjuncts, the arguments THEY were parsed
// against, how many of those were consumed, and the seeded sample if one is present.
//
// args is deliberately not the caller's whole argument list. A driver binds `?`
// POSITIONALLY across the entire statement, so a statement carrying placeholders
// in its SELECT list — the census, whose aggregates are conditional — offsets
// every WHERE argument. The fake has to do the same or it would compare `org = ?`
// against the first aggregate's bound and match nothing.
type pred struct {
	terms  []term
	args   []any
	used   int
	seed   string
	shared int
}

// where extracts the conjuncts and the arguments they consume. It FAILS on a
// conjunct it does not recognise rather than skipping it — a skipped predicate is
// a predicate that was never tested.
func where(s string, args []any) (pred, error) {
	before, after, ok := strings.Cut(s, " WHERE ")
	if !ok {
		return pred{args: args, shared: shareDenominator}, nil
	}
	// Every placeholder BEFORE the WHERE belongs to the SELECT list, and consumed
	// its argument there.
	lead := strings.Count(before, "?")
	if lead > len(args) {
		return pred{}, fmt.Errorf("fake store: %d placeholders precede the WHERE but only %d arguments were bound", lead, len(args))
	}
	p := pred{args: args[lead:], shared: shareDenominator}
	rest := after
	for _, stop := range []string{" GROUP BY ", " ORDER BY ", " LIMIT "} {
		if j := strings.Index(rest, stop); j >= 0 {
			rest = rest[:j]
		}
	}
	for part := range strings.SplitSeq(rest, " AND ") {
		part = strings.TrimSpace(part)
		if m := termRe.FindStringSubmatch(part); m != nil {
			p.terms = append(p.terms, term{col: m[1], op: m[2]})
			p.used++
			continue
		}
		if sizeRe.MatchString(part) {
			p.terms = append(p.terms, term{col: sizeCol, op: "<="})
			p.used++
			continue
		}
		if sampleRe.MatchString(part) {
			if p.used+1 >= len(p.args) {
				return pred{}, fmt.Errorf("fake store: the sample predicate has no arguments")
			}
			p.seed = fmt.Sprint(p.args[p.used])
			p.shared = int(asFloat(p.args[p.used+1]))
			p.used += 2
			continue
		}
		return pred{}, fmt.Errorf("fake store: unrecognised predicate %q", part)
	}
	return p, nil
}

// match evaluates the conjuncts against one row.
func match(p pred, row map[string]any) bool {
	for i, t := range p.terms {
		if i >= len(p.args) {
			return false
		}
		got, want := row[t.col], p.args[i]
		switch t.op {
		case "=":
			if fmt.Sprint(got) != fmt.Sprint(want) {
				return false
			}
		case ">=":
			if !geq(got, want) {
				return false
			}
		case "<":
			if geq(got, want) {
				return false
			}
		case "<=":
			if asFloat(got) > asFloat(want) {
				return false
			}
		}
	}
	return true
}

// geq compares a stored value with a bound one. The plane binds instants as the
// store's DateTime literal, so the comparison happens on that rendering — which
// is exactly what the real store compares.
func geq(got, want any) bool {
	g, w := render(got), render(want)
	return g >= w
}

func render(v any) string {
	if t, ok := v.(time.Time); ok {
		return literal(t)
	}
	return fmt.Sprint(v)
}

func asFloat(v any) float64 { return number(v) }

// tail reads the trailing LIMIT / OFFSET arguments a statement declared. They are
// read from the predicate's own argument slice, after what the conjuncts consumed.
func tail(s string, p pred) (limit, offset int) {
	limit, offset = -1, 0
	args, used := p.args, p.used
	if strings.Contains(s, " LIMIT ? OFFSET ?") {
		if used+1 < len(args) {
			limit, offset = int(asFloat(args[used])), int(asFloat(args[used+1]))
		}
		return
	}
	if strings.HasSuffix(s, " LIMIT ?") {
		if used < len(args) {
			limit = int(asFloat(args[used]))
		}
		return
	}
	if m := regexp.MustCompile(` LIMIT (\d+)$`).FindStringSubmatch(s); m != nil {
		fmt.Sscanf(m[1], "%d", &limit)
	}
	return
}

// ── the three readers ────────────────────────────────────────────────────────

// readManifest emulates a FINAL read of a ReplacingMergeTree(seq): among rows
// sharing (org, name, version), the one with the GREATEST seq survives. That is
// the engine behaviour a published version's immutability rests on, so it is
// reproduced here rather than assumed.
func (f *fake) readManifest(s string, args []any) ([]map[string]any, error) {
	p, err := where(s, args)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(s, " FINAL ") {
		return nil, fmt.Errorf("fake store: a register read without FINAL would see superseded rows")
	}
	best := map[string]map[string]any{}
	order := []string{}
	for _, r := range f.manifest {
		key := fmt.Sprint(r["org"], "\x00", r["name"], "\x00", r["version"])
		prev, ok := best[key]
		if !ok {
			order = append(order, key)
			best[key] = r
			continue
		}
		if asFloat(r["seq"]) >= asFloat(prev["seq"]) {
			best[key] = r
		}
	}
	out := []map[string]any{}
	for _, key := range order {
		r := best[key]
		if f.leak || match(p, r) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if strings.Contains(s, "ORDER BY name, version DESC") {
			if a, b := fmt.Sprint(out[i]["name"]), fmt.Sprint(out[j]["name"]); a != b {
				return a < b
			}
		}
		return asFloat(out[i]["version"]) > asFloat(out[j]["version"])
	})
	// `LIMIT 1 BY name` keeps the first row of each name in the ORDER BY's order —
	// reproduced here because the plane relies on the store doing it, and a fake
	// that returned every version instead would make an O(names) read look like one
	// while the plane was in fact asking for the whole register.
	if strings.Contains(s, " LIMIT 1 BY name ") {
		seen := map[string]bool{}
		kept := out[:0:0]
		for _, r := range out {
			name := fmt.Sprint(r["name"])
			if seen[name] {
				continue
			}
			seen[name] = true
			kept = append(kept, r)
		}
		out = kept
	}
	if limit, _ := tail(s, p); limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fake) readRows(s string, args []any) ([]map[string]any, error) {
	p, err := where(s, args)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range f.rows {
		if f.leak || match(p, r) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return fmt.Sprint(out[i]["id"]) < fmt.Sprint(out[j]["id"]) })
	limit, offset := tail(s, p)
	if offset > len(out) {
		return []map[string]any{}, nil
	}
	out = out[offset:]
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// readSource serves both shapes the plane reads from the source: the census
// aggregate and the grouped fact read.
func (f *fake) readSource(s string, args []any) ([]map[string]any, error) {
	p, err := where(s, args)
	if err != nil {
		return nil, err
	}
	// sizeCol is what [representable] reads. It is computed here, from the row, so
	// the fake evaluates the real byte length rather than trusting the plane.
	var kept []featRow
	for _, r := range f.feature {
		row := map[string]any{
			"org": r.Org, "subject_kind": r.Kind, "subject": r.Subject, "bucket": r.Bucket,
			sizeCol: len(r.Kind) + len(r.Subject),
		}
		if !match(p, row) {
			continue
		}
		if p.shared < shareDenominator && bucketOf(p.seed, r.Subject) >= p.shared {
			continue
		}
		kept = append(kept, r)
	}
	if strings.Contains(s, "uniqExact") {
		// THE CENSUS, whose aggregates are CONDITIONAL: the size bound sits inside them
		// rather than in the WHERE, because the count of what the bound EXCLUDES is one
		// of the things being measured. The fake honours that split — it evaluates the
		// same predicate per aggregate — so a census that stopped conditioning, or
		// stopped counting the excluded, changes what these tests see.
		fits := func(r featRow) bool { return len(r.Kind)+len(r.Subject) <= maxSubjectBytes }
		seen := map[string]bool{}
		subjects := map[string]bool{}
		oversize := map[string]bool{}
		var first, last time.Time
		for _, r := range kept {
			if !fits(r) {
				oversize[r.Kind+"\x00"+r.Subject] = true
				continue
			}
			seen[r.Kind+"\x00"+r.Subject+"\x00"+r.Bucket.String()] = true
			subjects[r.Kind+"\x00"+r.Subject] = true
			if first.IsZero() || r.Bucket.Before(first) {
				first = r.Bucket
			}
			if r.Bucket.After(last) {
				last = r.Bucket
			}
		}
		return []map[string]any{{
			"rows": uint64(len(seen)), "subjects": uint64(len(subjects)),
			"oversize": uint64(len(oversize)),
			"first":    first, "last": last,
		}}, nil
	}

	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].Kind != kept[j].Kind {
			return kept[i].Kind < kept[j].Kind
		}
		if kept[i].Subject != kept[j].Subject {
			return kept[i].Subject < kept[j].Subject
		}
		return kept[i].Bucket.Before(kept[j].Bucket)
	})
	out := []map[string]any{}
	for _, r := range kept {
		row := map[string]any{"subject_kind": r.Kind, "subject": r.Subject, "bucket": r.Bucket}
		for c, v := range r.Value {
			row[c] = v
		}
		out = append(out, row)
	}
	if limit, _ := tail(s, p); limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// bucketOf stands in for cityHash64. What matters is that it is a function of the
// seed and the SUBJECT only, so a subject is admitted whole or not at all.
func bucketOf(seed, subject string) int {
	h := fnv.New64a()
	h.Write([]byte(seed))
	h.Write([]byte{0})
	h.Write([]byte(subject))
	return int(h.Sum64() % shareDenominator)
}

// ── assertions a test reuses ─────────────────────────────────────────────────

// bound returns every statement the fake has seen.
func (f *fake) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func (f *fake) forget() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}
