package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// WHY THERE IS NO SERVING PLANE HERE, AND WHY OPENROUTER IS NOT IMPORTED.
//
// The obvious next move is a third store beside claims and attempts, holding
// latency, throughput, uptime and price per model — OpenRouter publishes exactly
// that at /api/v1/models/{author}/{slug}/endpoints (latency_last_30m,
// throughput_last_30m, uptime_last_5m/30m/1d, pricing, quantization, status),
// and it covers 413 models. Measured 2026-08-16; their /api/v1/models carries no
// score, eval, elo or quality field at all, so none of it is benchmark data.
//
// It does not belong here, and mostly it does not need importing.
//
// o11y already records this FIRST-HAND. llmobstypes carries LatencyMs beside
// Model, Provider, Cost and TotalTokens on every observation, so for every model
// we actually route to we have per-call latency with the cost attached — which
// is better than a third party's thirty-minute aggregate for deciding where to
// send the next request. Importing theirs would put a second, weaker answer next
// to a number we already measure ourselves, which is the blending mistake this
// file exists to prevent, one domain over.
//
// The division, stated once so it is not re-litigated:
//
//	accuracy  → this arena: measured (our harness) ∥ published (their claims)
//	serving   → o11y:       measured (our spans, per call, with cost)
//	coverage  → models.hanzo.ai/v1/models, 529 models
//
// The symmetry is deliberate and so is the one gap in it: o11y has no published
// plane. For a model we have never called we hold no latency, and a third-party
// figure is the only thing that would fill it. If that is ever wanted it belongs
// in o11y marked as a CLAIM, exactly as accuracy claims are marked here — never
// in the benchmark arena, and never blended into a measured series.

// The claims plane, made writable.
//
// A published claim is DATA — a number someone else reported, plus the citation
// it was read from — so it changes when the world does: a vendor restates a
// score, a leaderboard reruns, a citation turns out to be wrong. Compiled into a
// Go slice, every one of those needs a release. That is the wrong shape for the
// only plane on this surface whose contents are other people's assertions.
//
// So claims are stored the way attempts already are: append-only JSONL, one file
// per benchmark, latest row wins for an (org, benchmark, model, source) key.
// Append-only because a correction is evidence too — knowing a claim USED to say
// 94.6 is how you catch a vendor quietly restating, which is exactly the drift
// this arena exists to surface. Nothing is ever overwritten in place.
//
// WHO WRITES, AND WHO READS. Any authenticated principal writes, and every row is
// its org's: the org and the author are the verified caller's, never the body's,
// and each write is audited before it lands. A claim is private to its org until
// the org publishes it. Anyone reads the public claims, labelled with org and
// author; a member also reads its own org's private ones; nobody reads another
// org's, a SuperAdmin acting in that org included. The leaderboard reads only the
// curator's public claims.
//
// PUBLICATION IS A SUPERADMIN ACT. An org's public claim is readable by anyone and
// still never reaches the board on its own, because the board is the platform's
// reading and an org's claim about its own model is exactly the number the arena
// exists to check. A SuperAdmin publishes in one of two ways: writing a claim
// under org admin, or promoting an org's public claims (POST
// /v1/benchmark/claims/promote), which copies each row as it stands — score,
// protocol, source — and keeps the org it came from in `from`. Promotion is the
// way to publish a number someone else filed, because retyping it is one more
// place for it to change.
//
// The compiled sets stay, as the FLOOR, and they are the curator's: org admin,
// public. `published` and `publishedImported` are what a fresh deployment knows
// before anyone touches it, so an empty store is a working arena rather than an
// empty one. Writing under org admin is curating, so it takes a SuperAdmin; one
// writing the same (benchmark, model, source) outranks a seed row without
// editing the seed. There is no other write path.
//
// This is also why the measured plane is not writable the same way. An attempt
// is something OUR harness did, and a hand-written attempt is a fabricated
// measurement. Claims can be typed in because a claim is a report of someone
// else's number and carries the source that lets a reader check it; a
// measurement cannot, because there is nothing to check it against but itself.
// Its one way in (POST /v1/benchmark/attempts, attempts.go) gives it something:
// the items must itemize a number the platform already publishes, from the
// research run that claim cites.

// Visibility of a claim. Private is the default: a claim is its org's until the
// org publishes it.
const (
	Private = "private"
	Public  = "public"
)

// curator is the org whose claims are the platform's own: the seed, and whatever
// a SuperAdmin writes while acting in the reserved admin org.
const curator = authz.AdminOrg

// storedClaim is a publishedClaim plus what makes a row manageable and
// attributable: when it was recorded, by whom, for which org, and who may read
// it. `At` breaks the tie between two rows for one key, so the file's order
// never has to be trusted.
type storedClaim struct {
	publishedClaim
	At         time.Time `json:"at"`
	By         string    `json:"by,omitempty"`
	Org        string    `json:"org"`
	Visibility string    `json:"visibility"`
	// From is the org a curator row was promoted from; empty on every other row.
	From string `json:"from,omitempty"`
	// Runs and Evidence are the research records behind the claim.
	Runs     []string `json:"runs,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

// The write bounds. A request carries at most maxClaimsPerRequest rows, which
// holds a whole leaderboard import (the seed is 281), in a body of at most
// maxClaimsBody. An org writes at most maxClaimsPerDay rows per UTC day, public
// and private alike, because every row lands in the one file per benchmark that
// every public read scans. A row names its benchmark, model, provider and
// protocol in at most maxClaimName characters, cites a source of at most
// maxClaimSource bytes, scores a percentage, and names at most maxClaimRefs
// research runs and evidence uris of at most maxClaimSource bytes each.
const (
	maxClaimsPerRequest = 500
	maxClaimsBody       = 1 << 20
	maxClaimsPerDay     = 2000
	maxClaimName        = 128
	maxClaimSource      = 2048
	maxClaimRefs        = 64
)

// claimKey is one org's reading of one model on one benchmark from one source.
// A newer row for the same key is a restatement and replaces it.
type claimKey struct{ org, bench, model, source string }

// effective layers the seed under the store for one benchmark (every benchmark
// when bench is empty) and returns the row that wins per claimKey, whoever may
// read it. Callers filter by visibility AFTER layering, so a claim its org
// restated as private stops reading as public rather than falling back to the
// public row it replaced.
//
// The key carries the SOURCE, which is the correction that matters most here.
// Keyed by (benchmark, model) alone, OpenAI's card, Artificial Analysis and Vals
// AI all collapse into whichever was written last — three independent readings
// of one model reduced to one number, with no way to tell that the others
// existed or disagreed. The spread BETWEEN claim sources is signal in exactly the
// way the measured-versus-claimed gap is.
//
// It carries the ORG, so one org's row never replaces another's: two orgs
// citing one source are two claims, each labelled with who made it.
//
// A STORED row with no org is unattributed: nothing verified who wrote it. It
// stays readable, public, with no org and no author, and it is never the
// curator's — only the compiled seed and a SuperAdmin's rows under org admin are.
// That is decided here at read time; the append-only files are never rewritten.
func effective(ctx context.Context, store claimReader, bench string) (map[claimKey]storedClaim, error) {
	eff := map[claimKey]storedClaim{}
	for _, set := range [][]publishedClaim{published, publishedImported} {
		for _, p := range set {
			if bench == "" || p.Benchmark == bench {
				eff[claimKey{curator, p.Benchmark, p.Model, p.Source}] = storedClaim{publishedClaim: p, Org: curator, Visibility: Public}
			}
		}
	}
	if store == nil {
		return eff, nil
	}
	stored, err := store.Claims(ctx, bench)
	if err != nil {
		return nil, err
	}
	newest := map[claimKey]storedClaim{}
	for _, c := range stored {
		if bench != "" && c.Benchmark != bench {
			continue
		}
		if c.Org == "" {
			c.Visibility, c.By = Public, ""
		}
		k := claimKey{c.Org, c.Benchmark, c.Model, c.Source}
		if prev, ok := newest[k]; !ok || c.At.After(prev.At) {
			newest[k] = c
		}
	}
	for k, c := range newest {
		eff[k] = c
	}
	return eff, nil
}

// claimsFor returns the curator's public claims per model on one benchmark —
// everything the leaderboard reads, for its headline and every aggregate beside
// it. Another org's claim never reaches the board, public or not; it is read,
// labelled, at /v1/benchmark/claims.
func claimsFor(ctx context.Context, store claimReader, bench string) (map[string][]storedClaim, error) {
	out := map[string][]storedClaim{}
	eff, err := effective(ctx, store, bench)
	if err != nil {
		return nil, err
	}
	for _, c := range eff {
		if c.Org == curator && c.Visibility == Public {
			out[c.Model] = append(out[c.Model], c)
		}
	}
	for model := range out {
		cs := out[model]
		sort.Slice(cs, func(i, j int) bool { return cs[i].Source < cs[j].Source })
	}
	return out, nil
}

// selectClaim picks the one claim a single-number column shows, and the rule is
// stated rather than incidental: a PROVIDER-REPORTED claim wins, because the
// arena exists to reconcile what a vendor says about its own model against what
// our harness measures. Among equals the better score in dir wins — the higher,
// or on a lower-is-better benchmark the lower — so the gap is never flattered by
// a modest pick.
//
// Everything not selected is still counted in Claims, Spread and Mean, and still
// readable at /v1/benchmark/claims. This chooses a column; it never discards a
// row.
func selectClaim(cs []storedClaim, dir string) (storedClaim, bool) {
	best, ok := storedClaim{}, false
	for _, c := range cs {
		if !ok || outranks(c, best, dir) {
			best, ok = c, true
		}
	}
	return best, ok
}

// outranks is selectClaim's order over two claims.
func outranks(a, b storedClaim, dir string) bool {
	if (a.Protocol == "provider-reported") != (b.Protocol == "provider-reported") {
		return a.Protocol == "provider-reported"
	}
	return better(a.Score, b.Score, dir)
}

// claimMean is the average of every claim for a model, and it is a DIFFERENT
// number from the selected one on purpose. Published answers "what does the
// vendor say"; Mean answers "what does the field say on average", which is the
// more honest summary when several independent sources have measured the same
// model and disagree.
//
// Unweighted. Weighting by source would require ranking the sources, and this
// arena's position is that a claim is checked against a MEASUREMENT rather than
// against an opinion of who reports carefully — so every reading counts once and
// the spread beside it says how much they diverge.
func claimMean(cs []storedClaim) *float64 {
	if len(cs) == 0 {
		return nil
	}
	var sum float64
	for _, c := range cs {
		sum += c.Score
	}
	m := sum / float64(len(cs))
	return &m
}

// claimSpread is the distance between the highest and lowest claim for a model,
// and it is nil when there is only one. It is the disagreement among sources,
// which a reader cannot infer from a single selected number.
func claimSpread(cs []storedClaim) *float64 {
	if len(cs) < 2 {
		return nil
	}
	lo, hi := cs[0].Score, cs[0].Score
	for _, c := range cs[1:] {
		if c.Score < lo {
			lo = c.Score
		}
		if c.Score > hi {
			hi = c.Score
		}
	}
	d := hi - lo
	return &d
}

/* ── the managed surface ──────────────────────────────────────────────────── */

// ClaimRow is one claim as the API returns it: the claim itself, who made it,
// who may read it, and where it came from, so a reader always sees whose number
// this is.
type ClaimRow struct {
	// Benchmark is the canonical test id the claim is about, from /catalog.
	Benchmark string `json:"benchmark"`
	// Provider is who the claim belongs to — the lab or leaderboard whose number
	// this is.
	Provider string `json:"provider"`
	// Model is the system the score is claimed for.
	Model string `json:"model"`
	// Score is the reported aggregate, as a percentage.
	Score float64 `json:"score"`
	// Protocol records HOW it was scored — provider-reported, agentic,
	// third-party-leaderboard — so a provider card is never read as a measurement.
	Protocol string `json:"protocol"`
	// Source is the citation the row was read from.
	Source string `json:"source"`
	// Org is the org that made the claim. "admin" is the platform's own curated
	// reading; any other org is that org's claim, not the platform's. Empty on an
	// unattributed row.
	Org string `json:"org"`
	// Visibility is "public", readable by anyone, or "private", readable by Org's
	// own members only. Only the curator's public claims reach the leaderboard.
	Visibility string `json:"visibility"`
	// Origin is "seed" for a compiled row, "stored" for one a verified caller
	// wrote through this surface, and "unattributed" for a stored row no verified
	// caller vouches for, which never reaches the leaderboard.
	Origin string `json:"origin"`
	// From is the org a curator row was promoted from: the org that filed the
	// claim before a SuperAdmin published it. Empty on every other row.
	From string `json:"from,omitempty"`
	// At is when a stored row was recorded. Zero for a seed row.
	At time.Time `json:"at,omitempty"`
	// By is the verified user who recorded a stored row. Empty for a seed row.
	By string `json:"by,omitempty"`
	// Runs are the research run ids the claim rests on
	// (GET /v1/research/runs?id=).
	Runs []string `json:"runs,omitempty"`
	// Evidence are the evidence uris the claim rests on
	// (GET /v1/research/evidence?uri=).
	Evidence []string `json:"evidence,omitempty"`
}

// Claim is one claim as a caller files it: the number, the citation it was read
// from, and the research records behind it.
type Claim struct {
	// Benchmark is the canonical test id the claim is about, from /catalog.
	Benchmark string `json:"benchmark"`
	// Provider is who the claim belongs to — the lab or leaderboard whose number
	// this is.
	Provider string `json:"provider"`
	// Model is the system the score is claimed for.
	Model string `json:"model"`
	// Score is the reported aggregate, as a percentage.
	Score float64 `json:"score"`
	// Protocol records HOW it was scored — provider-reported, agentic,
	// third-party-leaderboard — so a provider card is never read as a measurement.
	Protocol string `json:"protocol"`
	// Source is the citation the row was read from. Required.
	Source string `json:"source"`
	// Runs are the research run ids the claim rests on, at most 64.
	Runs []string `json:"runs,omitempty"`
	// Evidence are the evidence uris the claim rests on, at most 64.
	Evidence []string `json:"evidence,omitempty"`
}

type claimsIn struct {
	// Benchmark filters to one benchmark id. Empty returns every benchmark.
	Benchmark string `query:"benchmark"`
	// Model filters to one model. Empty returns every model.
	Model string `query:"model"`
	// Provider filters to one lab or leaderboard — the way to read what a single
	// source claims across every model it covers.
	Provider string `query:"provider"`
	// Source filters to one citation, which is the finest grain there is: a
	// source is what makes two claims about one model independent rather than a
	// restatement of each other.
	Source string `query:"source"`
	// Protocol filters by HOW a claim was scored, so provider cards can be read
	// apart from third parties running their own harness.
	Protocol string `query:"protocol"`
	// Org filters to the claims one org made; "admin" reads the platform's own.
	// It narrows what the caller may already read and never widens it.
	Org string `query:"org"`
}

type claimsOut struct {
	// Data is one row per (org, benchmark, model, SOURCE) — every independent
	// claim, not one per model. Effective values only: the row that wins after
	// layering for each key, never the superseded readings behind it.
	Data []ClaimRow `json:"data"`
	// Total is how many rows Data holds.
	Total int `json:"total"`
}

// claims lists the effective claims the caller may read, each labelled with the
// org that made it and the user who recorded it.
//
// Anyone reads the public claims. A signed-in caller also reads its own org's
// private ones. No caller reads another org's private claims — a SuperAdmin
// acting in that org included, since acting in an org is not membership of it.
// Rows under org "admin" are the platform's own curated readings: the compiled
// seed and any correction a SuperAdmin wrote.
//
// Effective values only. The history of a key lives in the append-only file and
// is not what this op is for; a list that returned every superseded row would
// make the common question the hard one.
func (o ops) claims(ctx context.Context, in *claimsIn) (*claimsOut, error) {
	org, member := memberOf(ctx)
	out := []ClaimRow{}
	eff, err := effective(ctx, o.s.State.store, in.Benchmark)
	if err != nil {
		return nil, o.gap(err)
	}
	for _, c := range eff {
		switch {
		case c.Visibility != Public && (!member || c.Org != org):
		case in.Org != "" && c.Org != in.Org:
		case in.Model != "" && c.Model != in.Model:
		case in.Provider != "" && c.Provider != in.Provider:
		case in.Source != "" && c.Source != in.Source:
		case in.Protocol != "" && c.Protocol != in.Protocol:
		default:
			out = append(out, claimRow(c))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Benchmark != b.Benchmark:
			return a.Benchmark < b.Benchmark
		case a.Model != b.Model:
			return a.Model < b.Model
		case a.Org != b.Org:
			return a.Org < b.Org
		}
		return a.Source < b.Source
	})
	return &claimsOut{Data: out, Total: len(out)}, nil
}

type putClaimsIn struct {
	// Data is the claims to record. One row is a correction; many is an import.
	// There is no separate bulk endpoint because there is no separate operation:
	// importing a leaderboard and fixing one number are the same write.
	Data []Claim `json:"data"`
	// Visibility is who may read every row in Data: "private", the default, keeps
	// them to the caller's org; "public" shows them to anyone. Restating a claim
	// with the other value moves it.
	Visibility string `json:"visibility,omitempty"`
}

type putClaimsOut struct {
	// Recorded is how many rows were written.
	Recorded int `json:"recorded"`
	// Rejected names the rows that were not, and why.
	Rejected []string `json:"rejected,omitempty"`
	// Org is the org the rows were recorded under: the caller's own.
	Org string `json:"org"`
	// Visibility is who may read them.
	Visibility string `json:"visibility"`
}

// putClaims records claims for the caller's org: one to correct a number, many to
// import a leaderboard. Any signed-in caller may write; a caller with no verified
// principal is refused 401.
//
// The org and the author are the verified caller's. Nothing in the body names
// either, and a body that tries is not read. Claims are private to that org unless
// visibility is "public". Writing as org "admin" curates the leaderboard, so it
// takes a SuperAdmin; anyone else acting there is refused 403, and the refusal is
// audited.
//
// Every row must carry a Source, because a claim without its citation is a
// number nobody can check, and a benchmark id from /catalog, because an unknown
// id would sit in the store invisible to every read. A row names its benchmark,
// model, provider and protocol in at most 128 characters each, cites a source of
// at most 2048 bytes, and scores a percentage from 0 to 100; a row outside that is
// rejected by number.
//
// A request carries at most 500 rows in at most 1 MiB (413 past either), and an
// org writes at most 2000 rows per UTC day (429 past that, and nothing from the
// request is written).
//
// The trail takes the call's intent, naming every row, BEFORE the first row
// lands; a trail that cannot take it answers 503 and nothing is written. A second
// record then names which rows were stored and which failed.
//
// Writes are append-only, so this never destroys the value it replaces. A
// vendor restating a score leaves both rows on disk, which is how the restating
// itself becomes visible.
func (o ops) putClaims(ctx context.Context, in *putClaimsIn) (*putClaimsOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	by := strings.TrimSpace(cloud.Who(ctx).User)
	super := principal.SuperFrom(ctx)
	if org == curator && !super {
		_ = o.audit(ctx, putAction, org, by, super, audit.Outcome{Result: "deny", Status: http.StatusForbidden, Reason: "SuperAdmin required"}, map[string]any{"claims": in.Data})
		return nil, cloud.Super.Refusal()
	}
	vis := strings.TrimSpace(in.Visibility)
	switch vis {
	case "":
		vis = Private
	case Private, Public:
	default:
		return nil, zip.ErrBadRequest(`visibility is "private" or "public"`)
	}
	if len(in.Data) > maxClaimsPerRequest {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "benchmark: at most %d claims per request", maxClaimsPerRequest)
	}
	if c, ok := cloud.Request(ctx); ok && len(c.Body()) > maxClaimsBody {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "benchmark: a claims request is at most %d bytes", maxClaimsBody)
	}
	st := o.s.State
	if st.store == nil || st.audit == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "benchmark: claims store or audit trail unavailable")
	}
	out := &putClaimsOut{Org: org, Visibility: vis}
	now := time.Now().UTC()
	var rows []storedClaim
	var at []int
	for i, c := range in.Data {
		switch {
		case c.Benchmark == "" || c.Model == "":
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: needs a benchmark and a model", i))
		case !known(c.Benchmark):
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: unknown benchmark", i))
		case overlong(c.Model, c.Provider, c.Protocol):
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: model, provider and protocol are at most %d characters", i, maxClaimName))
		case strings.TrimSpace(c.Source) == "":
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: a claim needs its source", i))
		case len(c.Source) > maxClaimSource:
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: a source is at most %d bytes", i, maxClaimSource))
		case !(c.Score >= 0 && c.Score <= 100):
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: a score is a percentage from 0 to 100", i))
		case len(c.Runs) > maxClaimRefs || len(c.Evidence) > maxClaimRefs || overref(c.Runs) || overref(c.Evidence):
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: runs and evidence name at most %d records of at most %d bytes each", i, maxClaimRefs, maxClaimSource))
		default:
			rows = append(rows, storedClaim{
				publishedClaim: publishedClaim{Benchmark: c.Benchmark, Provider: c.Provider, Model: c.Model, Score: c.Score, Protocol: c.Protocol, Source: c.Source},
				At:             now, By: by, Org: org, Visibility: vis, Runs: c.Runs, Evidence: c.Evidence,
			})
			at = append(at, i)
		}
	}
	if len(rows) == 0 {
		return out, nil
	}
	// ONE TRANSACTION UNDER THE STORE'S WRITE LOCK: the day's count, the intent on the trail
	// and the rows are a single step, so two replicas cannot both fit under one remaining
	// allowance, and the rows land together or not at all.
	var intended bool
	err = st.store.write(ctx, func(b book) error {
		n, err := b.writtenToday(ctx, org, now)
		if err != nil {
			return err
		}
		if n+len(rows) > maxClaimsPerDay {
			return zip.Errorf(http.StatusTooManyRequests, "benchmark: org %s has written %d of its %d claims today (UTC); nothing was written", org, n, maxClaimsPerDay)
		}
		if err := o.audit(ctx, putAction, org, by, super, audit.Outcome{Result: "intent"}, map[string]any{"rows": at, "claims": rows}); err != nil {
			return err
		}
		intended = true
		for _, c := range rows {
			if err := b.PutClaim(ctx, c); err != nil {
				return err
			}
		}
		return nil
	})
	var failed, stored []int
	switch {
	case err == nil:
		stored = at
		out.Recorded = len(rows)
	case !intended || isRefusal(err):
		return nil, o.refused(err)
	default:
		// The intent is on the trail and the rows are not in the store: none of them
		// landed, and each says so.
		o.s.Log.Error("benchmark: claims not stored", "org", org, "by", by, "rows", len(rows), "err", err)
		for _, i := range at {
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: not stored", i))
		}
		failed = at
	}
	outcome := audit.Outcome{Result: "success", Status: http.StatusOK}
	if len(failed) > 0 {
		outcome = audit.Outcome{Result: "error", Status: http.StatusOK, Reason: fmt.Sprintf("%d of %d rows not stored", len(failed), len(rows))}
	}
	// The rows are in the store and the intent covers them, so a trail that cannot
	// take the outcome is logged by audit and the caller still gets the answer.
	_ = o.audit(ctx, putAction, org, by, super, outcome, map[string]any{"stored": stored, "failed": failed})
	return out, nil
}

// memberOf is the org the caller is a member of. A SuperAdmin acting in an org it
// does not belong to is not a member there: acting is not membership, so it
// reads what a stranger reads.
func memberOf(ctx context.Context) (string, bool) {
	org, ok := principal.OrgFrom(ctx)
	if ok && principal.SuperFrom(ctx) && strings.TrimSpace(cloud.Who(ctx).Owner) != org {
		return org, false
	}
	return org, ok
}

type promoteIn struct {
	// Org is the org whose public claims to publish. It is required, and it is
	// never "admin": the platform's own claims are on the board already.
	Org string `json:"org" validate:"required"`
	// Benchmark narrows the promotion to one catalog id. Empty takes every one.
	Benchmark string `json:"benchmark,omitempty"`
	// Model narrows it to one model. Empty takes every one.
	Model string `json:"model,omitempty"`
	// Source narrows it to one citation. Empty takes every one.
	Source string `json:"source,omitempty"`
	// Protocol narrows it to one way of scoring — hanzo-measured,
	// oracle-ceiling. Empty takes every one.
	Protocol string `json:"protocol,omitempty"`
}

type promoteOut struct {
	// Promoted is how many claims this call published.
	Promoted int `json:"promoted"`
	// Unchanged is how many matched and were published already, exactly as
	// they stand, so nothing was written for them.
	Unchanged int `json:"unchanged"`
	// From is the org the claims were promoted from.
	From string `json:"from"`
	// Rejected names the claims that were not stored, and why.
	Rejected []string `json:"rejected,omitempty"`
	// Data is the platform rows this call wrote, in benchmark, model and source
	// order.
	Data []ClaimRow `json:"data"`
}

// promote publishes an org's public claims onto the leaderboard: each one that
// matches is copied, as it stands, into the platform's own claims, with the org it
// came from kept in from.
//
// Publication is a SuperAdmin act. The board is the platform's reading, so an
// org's claim — public or not — never reaches it on its own; this is how one
// does, on purpose and on the record. Only a SuperAdmin acting in org admin may
// call it. Anyone else is refused 403, a SuperAdmin acting in another org
// included, and the refusal is audited.
//
// It reads only the org's PUBLIC claims, the same rows anyone reads at
// /v1/benchmark/claims?org=; a private claim is not the platform's to publish. The
// copy keeps the benchmark, provider, model, score, protocol and source, so the
// published number is the filed number and the citation still names the run it
// came from. A claim already published exactly as it stands is counted unchanged
// and written again never, so promoting twice is free; one the org has restated
// since is published at its new value.
//
// The org's daily write bound applies to the platform's rows, and the trail
// takes the call's intent, naming every row, before the first lands.
//
// Example: {"org": "hanzo", "benchmark": "gpqa_diamond"}
func (o ops) promote(ctx context.Context, in *promoteIn) (*promoteOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	by := strings.TrimSpace(cloud.Who(ctx).User)
	super := principal.SuperFrom(ctx)
	if org != curator || !super {
		_ = o.audit(ctx, promoteAction, org, by, super, audit.Outcome{Result: "deny", Status: http.StatusForbidden, Reason: "SuperAdmin acting in org admin required"}, in)
		return nil, cloud.Super.Refusal()
	}
	from := strings.TrimSpace(in.Org)
	if from == "" || from == curator {
		return nil, zip.ErrBadRequest(`promote names the org whose public claims to publish, and it is not "admin"`)
	}
	st := o.s.State
	if st.store == nil || st.audit == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "benchmark: claims store or audit trail unavailable")
	}

	now := time.Now().UTC()
	out := &promoteOut{From: from, Data: []ClaimRow{}}
	var rows []storedClaim
	// One transaction under the store's write lock, as putClaims: what is promoted is read
	// in it, so two replicas promoting one org's claims do not both publish them.
	var intended bool
	err = st.store.write(ctx, func(b book) error {
		out.Promoted, out.Unchanged, rows = 0, 0, nil
		eff, err := effective(ctx, b, in.Benchmark)
		if err != nil {
			return err
		}
		for _, c := range eff {
			switch {
			case c.Org != from || c.Visibility != Public:
			case in.Model != "" && c.Model != in.Model:
			case in.Source != "" && c.Source != in.Source:
			case in.Protocol != "" && c.Protocol != in.Protocol:
			default:
				if cur, ok := eff[claimKey{curator, c.Benchmark, c.Model, c.Source}]; ok &&
					cur.Visibility == Public && cur.From == from && cur.publishedClaim == c.publishedClaim &&
					slices.Equal(cur.Runs, c.Runs) && slices.Equal(cur.Evidence, c.Evidence) {
					out.Unchanged++
					continue
				}
				rows = append(rows, storedClaim{publishedClaim: c.publishedClaim, At: now, By: by, Org: curator, Visibility: Public, From: from, Runs: c.Runs, Evidence: c.Evidence})
			}
		}
		if len(rows) == 0 {
			return nil
		}
		sort.Slice(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			switch {
			case a.Benchmark != b.Benchmark:
				return a.Benchmark < b.Benchmark
			case a.Model != b.Model:
				return a.Model < b.Model
			}
			return a.Source < b.Source
		})
		n, err := b.writtenToday(ctx, curator, now)
		if err != nil {
			return err
		}
		if n+len(rows) > maxClaimsPerDay {
			return zip.Errorf(http.StatusTooManyRequests, "benchmark: org %s has written %d of its %d claims today (UTC); nothing was promoted", curator, n, maxClaimsPerDay)
		}
		if err := o.audit(ctx, promoteAction, org, by, super, audit.Outcome{Result: "intent"}, map[string]any{"from": from, "claims": rows}); err != nil {
			return err
		}
		intended = true
		for _, c := range rows {
			if err := b.PutClaim(ctx, c); err != nil {
				return err
			}
		}
		return nil
	})
	if len(rows) == 0 && err == nil {
		return out, nil
	}
	var stored, failed []int
	switch {
	case err == nil:
		for i, c := range rows {
			stored = append(stored, i)
			out.Promoted++
			out.Data = append(out.Data, claimRow(c))
		}
	case !intended || isRefusal(err):
		return nil, o.refused(err)
	default:
		o.s.Log.Error("benchmark: promoted claims not stored", "from", from, "by", by, "rows", len(rows), "err", err)
		for i, c := range rows {
			out.Rejected = append(out.Rejected, fmt.Sprintf("%s %s %s: not stored", c.Benchmark, c.Model, c.Source))
			failed = append(failed, i)
		}
	}
	outcome := audit.Outcome{Result: "success", Status: http.StatusOK}
	if len(failed) > 0 {
		outcome = audit.Outcome{Result: "error", Status: http.StatusOK, Reason: fmt.Sprintf("%d of %d rows not stored", len(failed), len(rows))}
	}
	_ = o.audit(ctx, promoteAction, org, by, super, outcome, map[string]any{"stored": stored, "failed": failed})
	return out, nil
}

// overref reports whether any research record a claim names is empty or past
// maxClaimSource bytes.
func overref(refs []string) bool {
	return slices.ContainsFunc(refs, func(r string) bool { return strings.TrimSpace(r) == "" || len(r) > maxClaimSource })
}

// overlong reports whether any name is past maxClaimName characters.
func overlong(names ...string) bool {
	for _, n := range names {
		if utf8.RuneCountInString(n) > maxClaimName {
			return true
		}
	}
	return false
}

// isRefusal reports whether err is an answer this surface wrote for a caller, as opposed
// to a failure of the store.
func isRefusal(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he)
}

// refused is err as the caller is told it: a refusal as written, a store fault as 503.
func (o ops) refused(err error) error {
	if isRefusal(err) {
		return err
	}
	return o.gap(err)
}

// gap is a store that did not answer: logged, and told to the caller as 503 and never as
// an empty arena, which reads exactly like one that holds nothing.
func (o ops) gap(err error) error {
	o.s.Log.Error("benchmark: the store did not answer", "err", err)
	return zip.Errorf(http.StatusServiceUnavailable, "benchmark: the store did not answer")
}

// The audited actions, one per write path.
const (
	putAction     = "benchmark.claims.put"
	promoteAction = "benchmark.claims.promote"
	importAction  = "benchmark.attempts.import"
)

// audit appends one record of a write: its intent before anything lands, its
// outcome after, or its refusal. The actor is the verified caller, and Home is
// set only for a SuperAdmin acting in another org.
func (o ops) audit(ctx context.Context, action, org, by string, super bool, outcome audit.Outcome, after any) error {
	chain := o.s.State.audit
	if chain == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "benchmark: audit trail unavailable, nothing was written")
	}
	who := cloud.Who(ctx)
	body, err := json.Marshal(after)
	if err != nil {
		return err
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: by, Email: who.Email},
		Action:    action,
		Resource:  audit.Resource{Type: action[:strings.LastIndex(action, ".")], ID: org},
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: super},
		Outcome:   outcome,
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		After:     audit.Redact(body),
	}
	if home := strings.TrimSpace(who.Owner); super && home != "" && home != org {
		rec.Actor.Home = home
	}
	if c, ok := cloud.Request(ctx); ok {
		rec.Method, rec.Path, rec.UserAgent = c.Method(), c.Path(), c.Header("User-Agent")
	}
	if _, err := chain.Append(ctx, rec); err != nil {
		o.s.Log.Error("benchmark: audit failed", "action", action, "org", org, "by", by, "result", outcome.Result, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "benchmark: audit trail unavailable, nothing was written")
	}
	return nil
}

// claimRow projects a claim onto the row shape. ClaimRow states its fields
// rather than embedding publishedClaim because an embedded struct's field docs
// do not reach the published registry — the wire gate catches exactly that, and
// an undescribed property on a public surface is the thing it is there to stop.
func claimRow(c storedClaim) ClaimRow {
	origin := "stored"
	switch {
	case c.At.IsZero():
		origin = "seed"
	case c.Org == "":
		origin = "unattributed"
	}
	return ClaimRow{
		Benchmark: c.Benchmark, Provider: c.Provider, Model: c.Model,
		Score: c.Score, Protocol: c.Protocol, Source: c.Source,
		Org: c.Org, Visibility: c.Visibility, Origin: origin, At: c.At, By: c.By, From: c.From,
		Runs: c.Runs, Evidence: c.Evidence,
	}
}
