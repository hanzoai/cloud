package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The claims plane, made writable.
//
// A published claim is DATA — a number someone else reported, plus the citation
// it was read from — so it changes when the world does: a vendor restates a
// score, a leaderboard reruns, a citation turns out to be wrong. Compiled into a
// Go slice, every one of those needs a release. That is the wrong shape for the
// only plane on this surface whose contents are other people's assertions.
//
// So claims are stored the way attempts already are: append-only JSONL, one file
// per benchmark, latest row wins for a (benchmark, model) key. Append-only
// because a correction is evidence too — knowing a claim USED to say 94.6 is how
// you catch a vendor quietly restating, which is exactly the drift this arena
// exists to surface. Nothing is ever overwritten in place.
//
// The compiled sets stay, as the FLOOR. `published` and `publishedImported` are
// what a fresh deployment knows before anyone touches it, so an empty store is a
// working arena rather than an empty one. A stored row for the same key wins,
// which is what makes this manageable: to fix a claim you write one, and it
// outranks the seed without editing the seed.
//
// This is also why the measured plane is not writable the same way. An attempt
// is something OUR harness did, and a hand-written attempt is a fabricated
// measurement. Claims can be typed in because a claim is a report of someone
// else's number and carries the source that lets a reader check it; a
// measurement cannot, because there is nothing to check it against but itself.

// storedClaim is a publishedClaim plus the two fields that make a row
// manageable: when it was recorded, and who recorded it. `At` breaks the tie
// between two rows for one key, so the file's order never has to be trusted.
type storedClaim struct {
	publishedClaim
	At time.Time `json:"at"`
	By string    `json:"by,omitempty"`
}

// ClaimStore is the published plane's backend. Deliberately the same shape as
// AttemptStore so production can swap a cloud backend behind either one.
type ClaimStore interface {
	// Claims returns stored claims for a benchmark, or all of them when the
	// benchmark is empty. Seed rows are NOT included — layering is the caller's.
	Claims(benchmark string) []storedClaim
	// Put records one claim. Append-only: a correction is a new row, and the
	// newest row for a (benchmark, model) key is the one that counts.
	Put(c storedClaim) error
}

// claimFileStore is the LOCAL-DEV backend: append-only JSONL under
// {DataDir}/benchmark/claims, one file per benchmark.
type claimFileStore struct {
	dir string
	mu  sync.Mutex
}

func newClaimStore(dataDir string) *claimFileStore {
	return &claimFileStore{dir: filepath.Join(dataDir, "benchmark", "claims")}
}

func (f *claimFileStore) Claims(benchmark string) []storedClaim {
	var out []storedClaim
	pattern := "*.jsonl"
	if benchmark != "" {
		pattern = safeName(benchmark) + ".jsonl"
	}
	files, err := filepath.Glob(filepath.Join(f.dir, pattern))
	if err != nil {
		return out
	}
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(body), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var c storedClaim
			if json.Unmarshal([]byte(line), &c) == nil {
				out = append(out, c)
			}
		}
	}
	return out
}

func (f *claimFileStore) Put(c storedClaim) error {
	if c.Benchmark == "" || c.Model == "" {
		return fmt.Errorf("claim needs a benchmark and a model")
	}
	if c.At.IsZero() {
		c.At = time.Now().UTC()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(c)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(
		filepath.Join(f.dir, safeName(c.Benchmark)+".jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644,
	)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = fh.Write(append(line, '\n'))
	return err
}

// claimsFor layers the three sources for one benchmark, weakest first: the
// hand-written set, then the generated import, then whatever the store holds.
// Later wins, so a stored correction beats an import and an import beats
// nothing — and the seed keeps a deployment with an empty store useful.
//
// Among stored rows the NEWEST wins, by `At` rather than by file order, because
// append-only means the same key legitimately appears more than once and the
// file is not a sequence anyone should have to trust.
func claimsFor(store ClaimStore, bench string) map[string]publishedClaim {
	out := map[string]publishedClaim{}
	for _, set := range [][]publishedClaim{published, publishedImported} {
		for _, p := range set {
			if bench == "" || p.Benchmark == bench {
				out[p.Model] = p
			}
		}
	}
	if store == nil {
		return out
	}
	newest := map[string]storedClaim{}
	for _, c := range store.Claims(bench) {
		if bench != "" && c.Benchmark != bench {
			continue
		}
		if prev, ok := newest[c.Model]; !ok || c.At.After(prev.At) {
			newest[c.Model] = c
		}
	}
	for model, c := range newest {
		out[model] = c.publishedClaim
	}
	return out
}

/* ── the managed surface ──────────────────────────────────────────────────── */

// ClaimRow is one claim as the API returns it: the claim itself, plus where it
// came from, so a reader can tell a row somebody fixed from a row that arrived
// with the deployment.
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
	// Origin is "seed" for a compiled row and "stored" for one written through
	// this surface. It is the difference between what we shipped and what an
	// operator has since corrected.
	Origin string `json:"origin"`
	// At is when a stored row was recorded. Zero for a seed row.
	At time.Time `json:"at,omitempty"`
	// By is who recorded it, when the caller said.
	By string `json:"by,omitempty"`
}

type claimsIn struct {
	// Benchmark filters to one benchmark id. Empty returns every benchmark.
	Benchmark string `query:"benchmark"`
	// Model filters to one model. Empty returns every model.
	Model string `query:"model"`
}

type claimsOut struct {
	// Data is one row per (benchmark, model), effective values only — the row
	// that WINS after layering, not every row ever written.
	Data []ClaimRow `json:"data"`
	// Total is how many rows Data holds.
	Total int `json:"total"`
}

// claims lists the effective published claims: what the leaderboard will use for
// each (benchmark, model) after the seed, the import and any stored correction
// are layered. It answers the operator's question — what does this arena
// currently believe someone else reported, and did we ship that or fix it.
//
// Effective values only. The history of a key lives in the append-only file and
// is not what this op is for; a list that returned every superseded row would
// make the common question the hard one.
func (o ops) claims(ctx context.Context, in *claimsIn) (*claimsOut, error) {
	st := o.s.State
	stored := map[string]storedClaim{}
	if st.claims != nil {
		for _, c := range st.claims.Claims(in.Benchmark) {
			key := c.Benchmark + "\x00" + c.Model
			if prev, ok := stored[key]; !ok || c.At.After(prev.At) {
				stored[key] = c
			}
		}
	}

	eff := map[string]ClaimRow{}
	for _, set := range [][]publishedClaim{published, publishedImported} {
		for _, p := range set {
			if in.Benchmark != "" && p.Benchmark != in.Benchmark {
				continue
			}
			eff[p.Benchmark+"\x00"+p.Model] = claimRow(p, "seed", time.Time{}, "")
		}
	}
	for key, c := range stored {
		eff[key] = claimRow(c.publishedClaim, "stored", c.At, c.By)
	}

	out := make([]ClaimRow, 0, len(eff))
	for _, r := range eff {
		if in.Model != "" && r.Model != in.Model {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Benchmark != out[j].Benchmark {
			return out[i].Benchmark < out[j].Benchmark
		}
		return out[i].Model < out[j].Model
	})
	return &claimsOut{Data: out, Total: len(out)}, nil
}

type putClaimsIn struct {
	// Data is the claims to record. One row is a correction; many is an import.
	// There is no separate bulk endpoint because there is no separate operation:
	// importing a leaderboard and fixing one number are the same write.
	Data []publishedClaim `json:"data"`
	// By is who is recording them — a person, or the importer's name.
	By string `json:"by"`
}

type putClaimsOut struct {
	// Recorded is how many rows were written.
	Recorded int `json:"recorded"`
	// Rejected names the rows that were not, and why.
	Rejected []string `json:"rejected,omitempty"`
}

// putClaims records published claims: one to correct a number, many to import a
// leaderboard. Every row must carry a Source, because a claim without its
// citation is a number nobody can check — and an unattributed number in the
// published plane is indistinguishable from a measurement, which is the one
// confusion this whole surface is built to prevent.
//
// Writes are append-only, so this never destroys the value it replaces. A
// vendor restating a score leaves both rows on disk, which is how the restating
// itself becomes visible.
func (o ops) putClaims(ctx context.Context, in *putClaimsIn) (*putClaimsOut, error) {
	st := o.s.State
	if st.claims == nil {
		return nil, fmt.Errorf("claims store unavailable")
	}
	known := map[string]bool{}
	for _, b := range catalog {
		known[b.ID] = true
	}

	out := &putClaimsOut{}
	now := time.Now().UTC()
	for i, c := range in.Data {
		switch {
		case c.Benchmark == "" || c.Model == "":
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: needs a benchmark and a model", i))
		case !known[c.Benchmark]:
			// An unknown benchmark id is a typo that would sit in the store
			// forever, invisible to every read that filters by a real one.
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: unknown benchmark %q", i, c.Benchmark))
		case strings.TrimSpace(c.Source) == "":
			out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: a claim needs its source", i))
		default:
			if err := st.claims.Put(storedClaim{publishedClaim: c, At: now, By: in.By}); err != nil {
				out.Rejected = append(out.Rejected, fmt.Sprintf("row %d: %v", i, err))
				continue
			}
			out.Recorded++
		}
	}
	return out, nil
}

// claimRow projects a claim onto the wire shape. ClaimRow states its fields
// rather than embedding publishedClaim because an embedded struct's field docs
// do not reach the published registry — the wire gate catches exactly that, and
// an undescribed property on a public surface is the thing it is there to stop.
func claimRow(p publishedClaim, origin string, at time.Time, by string) ClaimRow {
	return ClaimRow{
		Benchmark: p.Benchmark, Provider: p.Provider, Model: p.Model,
		Score: p.Score, Protocol: p.Protocol, Source: p.Source,
		Origin: origin, At: at, By: by,
	}
}
