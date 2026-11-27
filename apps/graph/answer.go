package graph

// answer.go answers a question about the whole graph rather than about one
// entity: the question a walk cannot start, because the caller does not know
// where the answer lives.
//
// MAP, THEN REDUCE. The communities of the graph as of the instant are read in
// the order the question's own words rank them. Each map call hands the model a
// batch of in-force facts, every one labelled, and takes back findings that cite
// the labels they rest on. One reduce call hands it the findings and takes back
// an answer that cites the findings. Every citation is checked against what was
// handed out, so the answer's assertion IDs are rows in this store and its
// community IDs are communities in this partition — the model can omit a
// citation, but it cannot invent one.
//
// THE MODEL IS REACHED ONE WAY: deps.AI, the plane every subsystem completes
// through, with the calling org as the data scope and its ledger as the payer.
// That client gates each call on the payer's balance before it runs and debits
// the tokens after, so this file holds no price and no meter of its own.
//
// NOTHING IS KEPT. A finding is derived from the facts and the question, and a
// cached one would be a stored claim nobody asserted, in a store whose rule is
// that every claim is somebody's.
//
// ALL OR NOTHING. A map call that fails fails the answer: an answer reduced from
// the communities that happened to succeed reads as complete and is not.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
	"golang.org/x/sync/errgroup"
)

// The bounds on one answer. Each is the work a caller buys with one request,
// and each is reported when it binds.
const (
	// questionMax is the longest question, in bytes.
	questionMax = 1000
	// callMax is how many map calls one answer makes; the communities that do
	// not fit are counted in `skipped` rather than read.
	callMax = 8
	// parallel is how many of them run at once.
	parallel = 4
	// factBudget is the bytes of facts one map call carries. A community larger
	// than this is cut, and `clipped` says so.
	factBudget = 12 << 10
	// findingBudget is the bytes of findings the reduce call carries.
	findingBudget = 12 << 10
	// findingMax is how many findings one map call may return.
	findingMax = 10
	// perEntity is how many assertions are read about one member, memberMax
	// how many members of one community are read at all, and readMax how many
	// members one answer reads across every community: each is a query on the
	// organization's one connection.
	perEntity = 64
	memberMax = 256
	readMax   = 2048
	// hitMax is how many assertions the question's words rank communities by.
	hitMax = 200
	// replyMax is the completion ceiling on every call, which is also what the
	// prepaid gate reserves against the payer before the call runs.
	replyMax = 1024
)

// given is one in-force fact as a map call sees it.
type given struct {
	Fact
	community int
	contested bool
}

// line renders a fact for the model: an edge as an arrow, a property as a value.
func (h given) line() string {
	if h.Names {
		return fmt.Sprintf("%s %s -> %s", h.Entity, h.Relation, h.Value)
	}
	s := fmt.Sprintf("%s %s = %q", h.Entity, h.Relation, h.Value)
	if h.contested {
		s += " (contested)"
	}
	return s
}

// inForce is what is in force about one entity at a point, by the one rule: for
// a property, the value resolution settles on, marked when it is contested; for
// an edge relation, every edge the pair holds — a walk would follow each.
func inForce(facts []Fact, w when, card cardinality) []given {
	byRelation := map[string][]Fact{}
	for _, f := range facts {
		byRelation[f.Relation] = append(byRelation[f.Relation], f)
	}
	var out []given
	for _, fs := range byRelation {
		held, conflicts := Resolve(fs, w, card.one(fs))
		switch {
		case len(held) == 0:
		case !held[0].Names:
			out = append(out, given{Fact: held[0], contested: len(conflicts) > 0})
		default:
			// Two terms of one edge are two statements; the answer lists
			// the edge once, as the most recently begun of them.
			said := map[string]bool{}
			for _, f := range held {
				if f.Names && !said[f.Value] {
					said[f.Value] = true
					out = append(out, given{Fact: f})
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b given) int {
		return cmp.Or(cmp.Compare(a.Relation, b.Relation), cmp.Compare(a.Value, b.Value))
	})
	return out
}

// rank is the entities the question's words find, best match first: the
// entity of each matching assertion and, for an edge, the entity it names. Any
// word may match — a question is not a filter — and on SQLite bm25 weighs a word by
// how rare it is, so the words every question carries count for little (ts_rank,
// on PostgreSQL, counts a word and does not weigh it, so the rows a limit keeps
// can differ there). It is one full-text read, the same one a search makes
// (filter.Match).
func (s *store) rank(ctx context.Context, question string, w when) ([]string, error) {
	hits, err := s.read(ctx, filter{
		Match: terms(question), Any: true, Valid: w.valid, Known: w.known, Limit: hitMax,
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range hits {
		out = append(out, f.Entity)
		if f.Names {
			out = append(out, f.Value)
		}
	}
	return out, nil
}

// order is every community at level 0 — the ones the question's words found
// first, the rest largest first — and the entities those words found, each by
// how well.
func order(p partition, hits []string) ([]int, map[int32]int) {
	comm := p.levels[0]
	var n int
	for _, c := range comm {
		n = max(n, int(c)+1)
	}
	out := make([]int, 0, n)
	seen := make([]bool, n)
	named := map[int32]int{}
	for _, key := range hits {
		i, ok := slices.BinarySearch(p.keys, key)
		if !ok {
			continue
		}
		if _, ok := named[int32(i)]; !ok {
			named[int32(i)] = len(named)
		}
		if !seen[comm[i]] {
			seen[comm[i]] = true
			out = append(out, int(comm[i]))
		}
	}
	for c := range n {
		if !seen[c] {
			out = append(out, c)
		}
	}
	return out, named
}

// batch is one map call's facts. facts[i] is labelled i+1.
type batch struct {
	facts []given
	size  int
}

// gather reads communities, in ranked order, into batches until the fan-out or
// the reads are spent: asked is how many it read, and the rest are skipped.
func gather(ctx context.Context, st *store, p partition, ranked []int, named map[int32]int, w when) (out []batch, asked int, clipped bool, err error) {
	members := p.members(0)
	var cur batch
	left := readMax
	for n, c := range ranked {
		if left == 0 {
			asked = n
			break
		}
		got, size, reads, cut, err := read(ctx, st, p, members[c], named, left, w)
		if err != nil {
			return nil, 0, false, err
		}
		if cur.size+size > factBudget {
			out = append(out, cur)
			cur = batch{}
			if len(out) == callMax {
				return out, n, clipped, nil
			}
		}
		for i := range got {
			got[i].community = c
		}
		cur.facts = append(cur.facts, got...)
		cur.size += size
		clipped = clipped || cut
		left -= reads
		asked = n + 1
	}
	if len(cur.facts) > 0 {
		out = append(out, cur)
	}
	return out, asked, clipped, nil
}

// read is one community's in-force facts, at most one call's worth from at most
// `left` members, how many members it read, and whether either bound cut it
// short. The members the question's words found are read first and then the
// most connected, so a community cut at a bound keeps what the question named
// and what the most edges run through.
func read(ctx context.Context, st *store, p partition, members []int32, named map[int32]int, left int, w when) ([]given, int, int, bool, error) {
	ms := slices.Clone(members)
	slices.SortStableFunc(ms, func(a, b int32) int {
		ra, fa := named[a]
		rb, fb := named[b]
		switch {
		case fa && fb:
			return cmp.Compare(ra, rb)
		case fa != fb:
			if fa {
				return -1
			}
			return 1
		}
		return cmp.Compare(p.degree[b], p.degree[a])
	})
	cut := len(ms) > min(memberMax, left)
	if cut {
		ms = ms[:min(memberMax, left)]
	}
	var got []given
	size := 0
	for n, m := range ms {
		facts, err := st.read(ctx, filter{Entity: p.keys[m], Valid: w.valid, Known: w.known, Newest: true, Limit: perEntity})
		if err != nil {
			return nil, 0, 0, false, err
		}
		// A member with more assertions than one read returns keeps its newest,
		// and the answer says it was cut.
		cut = cut || len(facts) == perEntity
		card, err := st.arity(ctx, relationsOf(facts), w.known)
		if err != nil {
			return nil, 0, 0, false, err
		}
		for _, h := range inForce(facts, w, card) {
			l := len(h.line()) + 8
			if size+l > factBudget {
				return got, size, n + 1, true, nil
			}
			got = append(got, h)
			size += l
		}
	}
	return got, size, len(ms), cut, nil
}

// finding is one thing a map call found, resolved to what it rests on.
type finding struct {
	text   string
	score  int
	facts  []given
	origin int // the batch that found it, which is what orders a tie
}

// asker is the one way this file reaches a model: the calling org's data scope,
// the account that pays, and the project the spend is attributed to.
type asker struct {
	ai      infer.Client
	org     string
	payer   account.Account
	project string
}

// ask runs one completion and decodes its reply into v. The model is left
// unnamed, so the platform's configured route answers — the same one every
// unnamed completion in the fleet takes.
func (a asker) ask(ctx context.Context, prompt string, v any) error {
	resp, err := a.ai.ChatCompletion(ctx, &infer.ChatRequest{
		Prompt:    prompt,
		Org:       a.org,
		Payer:     a.payer,
		Project:   a.project,
		MaxTokens: replyMax,
	})
	if err != nil {
		return err
	}
	if resp == nil {
		return errors.New("the model returned no reply")
	}
	return envelope(resp.Content, v)
}

// envelope reads the one JSON object a reply was asked to be, ignoring prose or
// a code fence around it. A reply that is not that object is an error: a guess
// at what the model meant would put words in a citation.
func envelope(raw string, v any) error {
	i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if i < 0 || j < i {
		return errors.New("the reply holds no JSON object")
	}
	if err := json.Unmarshal([]byte(raw[i:j+1]), v); err != nil {
		return fmt.Errorf("the reply is not the requested object: %w", err)
	}
	return nil
}

// mapPrompt asks for findings in one batch of facts.
func mapPrompt(question string, b batch) string {
	var s strings.Builder
	s.WriteString("These are facts from an organization's knowledge graph, one per line, each labelled [n]. " +
		"An arrow is a relation to another entity; `=` is a property's value.\n\n")
	for i, h := range b.facts {
		fmt.Fprintf(&s, "[%d] %s\n", i+1, h.line())
	}
	fmt.Fprintf(&s, "\nQuestion: %s\n\n", question)
	fmt.Fprintf(&s, "List what these facts say that bears on the question, at most %d findings. "+
		"Use only these facts, and cite every fact a finding rests on by its label. "+
		"Score each finding from 1 to 100 by how much it helps answer the question.\n", findingMax)
	s.WriteString(`Reply with one JSON object and nothing else: {"findings":[{"text":"…","score":1,"facts":[1]}]}` + "\n")
	s.WriteString(`If nothing here bears on the question, reply {"findings":[]}.`)
	return s.String()
}

// reducePrompt asks for the answer from the findings.
func reducePrompt(question string, fs []finding) string {
	var s strings.Builder
	s.WriteString("These are findings from an organization's knowledge graph, most relevant first, each labelled [n].\n\n")
	for i, f := range fs {
		fmt.Fprintf(&s, "[%d] %s\n", i+1, f.text)
	}
	fmt.Fprintf(&s, "\nQuestion: %s\n\n", question)
	s.WriteString("Answer the question from these findings alone, and cite every finding the answer rests on by its label.\n")
	s.WriteString(`Reply with one JSON object and nothing else: {"answer":"…","findings":[1]}` + "\n")
	s.WriteString(`If they do not answer it, reply {"answer":"","findings":[]}.`)
	return s.String()
}

// mapped runs the map calls, at most `parallel` at once, and fails as a whole.
func mapped(ctx context.Context, a asker, question string, batches []batch) ([]finding, error) {
	found := make([][]finding, len(batches))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(parallel)
	for n, b := range batches {
		g.Go(func() error {
			var reply struct {
				Findings []struct {
					Text  string `json:"text"`
					Score int    `json:"score"`
					Facts []int  `json:"facts"`
				} `json:"findings"`
			}
			if err := a.ask(ctx, mapPrompt(question, b), &reply); err != nil {
				return fmt.Errorf("map call %d of %d: %w", n+1, len(batches), err)
			}
			for _, r := range reply.Findings {
				if len(found[n]) == findingMax {
					break
				}
				f := finding{text: strings.TrimSpace(r.Text), score: min(r.Score, 100), origin: n}
				for _, l := range r.Facts {
					if l >= 1 && l <= len(b.facts) {
						f.facts = append(f.facts, b.facts[l-1])
					}
				}
				// A finding that cites nothing it was handed is not grounded, and
				// one that scores itself irrelevant says it is not a finding.
				if f.text == "" || f.score <= 0 || len(f.facts) == 0 {
					continue
				}
				found[n] = append(found[n], f)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return slices.Concat(found...), nil
}

// ── the wire ─────────────────────────────────────────────────────────────────

type graphAnswerIn struct {
	// Question is what to answer, in words, 1000 bytes at most. Its words also
	// decide which communities are read first, so naming the things it is about
	// is what aims it.
	Question string `json:"question" validate:"required"`
	// AsOf answers from the graph as it stood at an instant of the world, RFC
	// 3339: what held then. Absent is now.
	AsOf string `json:"as_of,omitempty"`
	// AsKnown is how much this plane had heard, RFC 3339: the answer as it would
	// have been given then, which is what makes a past answer reproducible.
	// Absent is now.
	AsKnown string `json:"as_known,omitempty"`
}

// graphFinding is one finding the answer rests on.
type graphFinding struct {
	// Text is what the model found, in its words.
	Text string `json:"text"`
	// Score is how much the model judged it helps, 1 to 100.
	Score int `json:"score"`
	// Communities are the communities of the facts it cites, at level 0 of the
	// partition at AsOf — the ids POST /v1/graph/communities answers with.
	Communities []int `json:"communities"`
	// Assertions are the IDs of the assertions it cites, each a row a read of
	// this graph returns.
	Assertions []string `json:"assertions"`
}

type graphAnswerOut struct {
	// AsOf is the instant the graph was read at, RFC 3339: the one asked for, or
	// the server's clock when none was.
	AsOf string `json:"as_of"`
	// AsKnown is the knowledge instant it was taken at, RFC 3339, the same way.
	AsKnown string `json:"as_known"`
	// Grounded is false when no community held a fact bearing on the question,
	// or the findings did not answer it. Answer is then empty: an answer resting
	// on nothing is not one this plane gives.
	Grounded bool `json:"grounded"`
	// Answer is the answer, in prose.
	Answer string `json:"answer,omitempty"`
	// Findings are the findings the answer cites, most relevant first.
	Findings []graphFinding `json:"findings,omitempty"`
	// Communities are every community the answer rests on, ascending.
	Communities []int `json:"communities,omitempty"`
	// Assertions are the IDs of every assertion the answer rests on, ascending.
	Assertions []string `json:"assertions,omitempty"`
	// Asked is how many communities were read.
	Asked int `json:"asked"`
	// Skipped is how many were not, because one answer makes at most eight map
	// calls and reads at most 2048 entities. They are the ones the question's
	// words ranked last.
	Skipped int `json:"skipped"`
	// Clipped says the answer did not read all it gathered: a community held
	// more facts than one call carries, or more members than one answer reads,
	// and kept the ones the question named and then the most connected; a member
	// held more than 64 assertions and kept its newest; or the findings held more
	// than the answering call carries and kept the highest scored.
	Clipped bool `json:"clipped"`
	// Calls is how many model calls the answer made, each billed to the calling
	// organization.
	Calls int `json:"calls"`
}

// answer answers a question from the whole graph and cites the assertions it
// rests on.
//
// It asks a model what each community's in-force facts say about the question,
// then asks once more for one answer from those findings. Every cited ID is
// checked to be a row of this graph, a failed model call fails the answer, and
// the calls are billed to the calling organization.
//
// Time: as_of and as_known place the graph that is read; either absent is now.
//
// Example: {"question": "Which teams own the billing services?"}
func (o ops) answer(ctx context.Context, in *graphAnswerIn) (*graphAnswerOut, error) {
	sc, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	question := strings.TrimSpace(in.Question)
	if question == "" {
		return nil, zip.ErrBadRequest("question is required")
	}
	if len(question) > questionMax {
		return nil, zip.ErrBadRequest(fmt.Sprintf("question is %d bytes, over the %d-byte ceiling", len(question), questionMax))
	}
	if o.s.State.ai == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "no model plane is configured on this deployment")
	}
	w, err := moment(in.AsOf, in.AsKnown)
	if err != nil {
		return nil, err
	}
	out := &graphAnswerOut{AsOf: w.valid.Format(time.RFC3339), AsKnown: w.known.Format(time.RFC3339)}

	p, err := partitionAt(ctx, st, "", w)
	if errors.Is(err, errTooMany) {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if err != nil {
		return nil, err
	}
	hits, err := st.rank(ctx, question, w)
	if err != nil {
		return nil, err
	}
	ranked, named := order(p, hits)
	batches, asked, clipped, err := gather(ctx, st, p, ranked, named, w)
	if err != nil {
		return nil, err
	}
	out.Asked, out.Skipped, out.Clipped = asked, len(ranked)-asked, clipped
	if len(batches) == 0 {
		return out, nil
	}

	// The org whose graph this is, the account that pays — which differs only for
	// a SuperAdmin acting in another org, who spends from its own — and the
	// project the spend is attributed to. tenantOf has already required the
	// request these are read from.
	c, _ := cloud.Request(ctx)
	a := asker{ai: o.s.State.ai, org: sc.org, payer: meter.Payer(c), project: principal.Project(c)}

	found, err := mapped(ctx, a, question, batches)
	out.Calls = len(batches)
	if err != nil {
		return nil, o.failed(err)
	}
	if len(found) == 0 {
		return out, nil
	}

	// Most relevant first, and a tie in the order the batches were read, which
	// is the order the question ranked them.
	slices.SortStableFunc(found, func(x, y finding) int {
		return cmp.Or(cmp.Compare(y.score, x.score), cmp.Compare(x.origin, y.origin))
	})
	size := 0
	for i, f := range found {
		if size += len(f.text) + 8; size > findingBudget {
			found, out.Clipped = found[:i], true
			break
		}
	}

	var reply struct {
		Answer   string `json:"answer"`
		Findings []int  `json:"findings"`
	}
	err = a.ask(ctx, reducePrompt(question, found), &reply)
	out.Calls++
	if err != nil {
		return nil, o.failed(fmt.Errorf("reduce call: %w", err))
	}
	var cited []finding
	seen := make([]bool, len(found))
	for _, l := range reply.Findings {
		if l >= 1 && l <= len(found) && !seen[l-1] {
			seen[l-1] = true
			cited = append(cited, found[l-1])
		}
	}
	answer := strings.TrimSpace(reply.Answer)
	// No answer is an answer: the findings did not settle the question, and the
	// plane says so. An answer that cites none of them is not one — it is prose
	// the facts were never shown to support — so it is refused, never returned.
	if answer == "" {
		return out, nil
	}
	if len(cited) == 0 {
		return nil, o.failed(errors.New("the answer cites none of the findings it was handed"))
	}
	slices.SortStableFunc(cited, func(x, y finding) int { return cmp.Compare(y.score, x.score) })

	out.Grounded, out.Answer = true, answer
	for _, f := range cited {
		w := graphFinding{Text: f.text, Score: f.score}
		for _, h := range f.facts {
			w.Communities = append(w.Communities, h.community)
			w.Assertions = append(w.Assertions, h.ID)
		}
		w.Communities = sorted(w.Communities)
		w.Assertions = sorted(w.Assertions)
		out.Findings = append(out.Findings, w)
		out.Communities = append(out.Communities, w.Communities...)
		out.Assertions = append(out.Assertions, w.Assertions...)
	}
	out.Communities = sorted(out.Communities)
	out.Assertions = sorted(out.Assertions)
	return out, nil
}

// failed is a model call's failure as the caller sees it. A payer out of funds
// or over its cap is the money wire's own refusal. Anything else is the model
// plane failing: logged here in full, and answered without the plane's
// internals and without an answer, since what the other calls found is not the
// whole of what was asked.
func (o ops) failed(err error) error {
	if errors.Is(err, meter.ErrInsufficientBalance) || errors.Is(err, meter.ErrSpendCapExceeded) {
		return cloud.Denied(err)
	}
	o.s.Log.Warn("graph answer: model call failed", "err", err)
	return zip.Errorf(http.StatusBadGateway, "a model call failed, so there is no answer: one from part of the graph would read as the whole")
}

func sorted[T cmp.Ordered](xs []T) []T {
	slices.Sort(xs)
	return slices.Compact(xs)
}
