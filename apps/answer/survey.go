package answer

// survey.go — SURVEY: the evidence a question is answered from, gathered under a
// bound. search and read applied to a plan and ITERATED. `rounds == 0` is one
// pass — byte-for-byte the loop that ran before this file existed — and the
// round's prose is discarded: gathering is not writing.
//
// This is the one value the deep-research port adds. Everything it composes
// (plan, search, rank, read, synthesize) already had a home; iteration did not.
// The round's decision is a compact JSON `move`, not a model tool-call: the
// engine needs no tool plane, and a model that answers in prose simply ends the
// survey instead of derailing it.
//
// BOUNDED FOUR WAYS, and every exit still reaches the terminal frame:
//
//	rounds        — the mode's round budget, hard-capped at maxRounds
//	deadline      — ctx, set from mode.deadline by Serve
//	tokenCeiling  — the running LLM token total, from mode.tokenCeiling
//	saturation    — a round that found no new source and read no new page
//
// SATURATION replaces the design's `len(srcs) >= maxSources` guard, which cannot
// do the job it was written for: rank() already caps the set at maxSources, so
// that test fires on the FIRST productive round and collapses research back into
// the single pass this file exists to iterate. "No new evidence arrived" is the
// bound that was actually meant, it cannot be satisfied vacuously, and it also
// terminates a model that keeps proposing queries it has already run.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	crawlpkg "github.com/hanzoai/cloud/apps/crawl"
	"github.com/hanzoai/cloud/apps/websearch"
)

const (
	// maxRounds is the hard ceiling on survey rounds, over and above whatever a
	// mode asks for. A mode is policy; this is the guard that policy cannot raise.
	maxRounds = 8
	// surveyClip caps the page text kept per source (runes) once a survey
	// ITERATES: many sources × a long page is the one way this loop could blow a
	// context window, so the deep path trades per-page depth for breadth. A single
	// pass keeps the fuller maxPageText.
	surveyClip = 3000
	// moveContext caps how many gathered sources are described back to the model
	// when it decides the next move — enough to see coverage, not enough to make
	// the decision call expensive.
	moveContext = 32
)

// move is one round's decision: what to search next, what to read next, and
// whether the evidence is complete. It is the ONLY thing read back from a round's
// completion — the prose that came with it is discarded.
type move struct {
	Next    string   `json:"next"`
	Queries []string `json:"queries"`
	Read    []string `json:"read"`
	Done    bool     `json:"done"`
}

// survey gathers the evidence for p.q under the plan, iterating while the model
// asks for more and the bounds allow it. It emits the envelope's gathering
// stages — searching, sources, reading, planning — and returns the ranked,
// enriched source set plus the tokens the decision calls cost.
//
// `sources` is emitted as a CUMULATIVE SNAPSHOT after every change, never as a
// per-source frame: all three SDK consumers REPLACE their source list on this
// event, so an incremental frame would erase the set instead of extending it.
func (e Engine) survey(ctx context.Context, p Params, plan []topic, out Sink) ([]Source, tokens) {
	var tok tokens

	scope := crawlpkg.Scope{Org: p.dataOrg, Project: p.project}
	// A survey re-ranks the whole accumulated pool every round, so the page text a
	// round paid to fetch must be remembered here — rank() rebuilds Sources from
	// raw results and would otherwise discard it.
	body := make(map[string]string)
	asked := make(map[string]bool)   // queries already run — never run twice
	fetched := make(map[string]bool) // urls already read — never fetched twice
	pool := make(map[string]bool)    // urls ever ranked in — the saturation signal

	limit := maxPageText
	if p.rounds > 0 {
		limit = surveyClip
	}

	var found []websearch.Result
	var srcs []Source
	m := move{Queries: flatten(plan, p.maxQueries)}

	for round := 0; ; round++ {
		// SEARCH — every query of this round's move, in the order it asked.
		for _, q := range m.Queries {
			if q = strings.TrimSpace(q); q == "" || asked[q] {
				continue
			}
			asked[q] = true
			out.status("searching", q)
			found = append(found, websearch.Search(ctx, q, p.language)...)
		}

		// RANK — over the whole accumulated pool, so a later round's find can
		// outrank an earlier one, then re-apply the pages already read.
		srcs = revive(rank(p.q, found, p.maxSources, p.hostCap), body)
		out.sources(srcs)

		fresh := 0
		for _, s := range srcs {
			if !pool[s.URL] {
				pool[s.URL] = true
				fresh++
			}
		}

		// READ — the opening round reads the mode's top pages; later rounds read
		// exactly what the model asked for. Never the same page twice.
		urls := m.Read
		if round == 0 && len(urls) == 0 {
			urls = topURLs(srcs, p.readTop)
		}
		urls = unread(urls, fetched)
		if len(urls) > 0 {
			srcs = read(ctx, e.Log, scope, srcs, urls, limit, func(host string) {
				out.status("reading", host)
			})
			for _, s := range srcs {
				if fetched[s.URL] {
					body[s.URL] = s.Snippet
				}
			}
			out.sources(srcs)
		}

		// BOUNDS — every one of them lands on the same exit, and the caller always
		// goes on to synthesize whatever was gathered.
		switch {
		case p.rounds == 0, m.Done,
			round+1 >= p.rounds, round+1 >= maxRounds,
			fresh == 0 && len(urls) == 0,
			tok.total >= p.tokenCeiling,
			ctx.Err() != nil:
			return srcs, tok
		}

		// DECIDE — one completion, whose prose is thrown away.
		nm, u := e.next(ctx, p, plan, srcs, round)
		tok.add(u)
		if nm.Done || (len(nm.Queries) == 0 && len(nm.Read) == 0) {
			return srcs, tok
		}
		out.status("planning", nm.Next)
		m = nm
	}
}

// next asks for one round's move over the plan and what has been gathered. The
// completion's prose is discarded; only the JSON is read. A model that answers in
// prose, times out, or is down yields a zero move, which ends the survey — the
// loop degrades to the evidence it already has, exactly like every other stage.
//
// TEMPERATURE: this call wants 0 (a decision, not a composition). cloud.ChatRequest
// carries no temperature field and inventing one here would fork the AI contract
// for one caller, so the determinism is bought with the prompt instead. Flagged,
// not faked.
func (e Engine) next(ctx context.Context, p Params, plan []topic, srcs []Source, round int) (move, *cloud.ChatResponse) {
	spec, err := json.Marshal(plan)
	if err != nil {
		return move{}, nil
	}
	prompt := fmt.Sprintf(
		"You are gathering evidence to answer a question. Decide the NEXT action only — do not answer.\n\n"+
			"Question: %s\n\nResearch plan:\n%s\n\nGathered so far (round %d of %d):\n%s\n\n"+
			"Reply ONLY as compact JSON {\"next\":\"...\",\"queries\":[...],\"read\":[...],\"done\":false}. "+
			"`next` is one plain sentence under 60 characters describing the action a person would take — "+
			"never a tool name, no markdown. `queries` are web searches not already run above. "+
			"`read` are URLs from the gathered list worth reading in full. "+
			"Set `done` true only when every plan item is covered and the key claims are corroborated "+
			"by two independent sources.",
		p.q, spec, round+1, p.rounds, gathered(srcs))

	resp := e.chat(ctx, p, p.model, prompt, nil)
	if resp == nil {
		return move{}, nil
	}
	return parseMove(resp.Content), resp
}

// parseMove reads a move out of a reply that may be fenced or chatty, using the
// same tolerant extractor plan() uses. Anything unparseable is the zero move,
// which ends the survey rather than looping on nonsense.
func parseMove(content string) move {
	obj := sliceBetween(content, '{', '}')
	if obj == "" {
		return move{}
	}
	var m move
	if json.Unmarshal([]byte(obj), &m) != nil {
		return move{}
	}
	m.Next = strings.TrimSpace(clip(oneLine(m.Next), maxNextStep))
	m.Queries = trimAll(m.Queries)
	m.Read = trimAll(m.Read)
	return m
}

// maxNextStep bounds the visible reasoning line: one plain sentence, not a
// paragraph and not a model monologue leaking into the UI.
const maxNextStep = 60

// flatten turns the plan into the opening round's queries: one todo per topic in
// turn, so the first search covers every topic's breadth before any topic's
// depth. Bounded by n; duplicates and blanks dropped.
func flatten(plan []topic, n int) []string {
	if n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	seen := make(map[string]bool, n)
	for depth := 0; len(out) < n; depth++ {
		grew := false
		for _, t := range plan {
			if depth >= len(t.Todos) {
				continue
			}
			grew = true
			q := strings.TrimSpace(t.Todos[depth])
			if q == "" || seen[q] {
				continue
			}
			seen[q] = true
			if out = append(out, q); len(out) >= n {
				return out
			}
		}
		if !grew {
			return out
		}
	}
	return out
}

// topURLs is the opening round's read list: the n best-ranked sources.
func topURLs(srcs []Source, n int) []string {
	if n <= 0 {
		return nil
	}
	if n > len(srcs) {
		n = len(srcs)
	}
	out := make([]string, 0, n)
	for _, s := range srcs[:n] {
		out = append(out, s.URL)
	}
	return out
}

// unread drops the URLs already fetched and marks the rest as fetched, so a
// model that asks twice for the same page pays for it once.
func unread(urls []string, fetched map[string]bool) []string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if u = strings.TrimSpace(u); u == "" || fetched[u] {
			continue
		}
		fetched[u] = true
		out = append(out, u)
	}
	return out
}

// revive re-applies the page text already read to a freshly ranked source set.
func revive(srcs []Source, body map[string]string) []Source {
	for i := range srcs {
		if md, ok := body[srcs[i].URL]; ok {
			srcs[i].Snippet = md
		}
	}
	return srcs
}

// gathered renders what the survey holds for the decision prompt: title + url,
// enough to judge coverage without shipping the corpus back to the model.
func gathered(srcs []Source) string {
	if len(srcs) == 0 {
		return "(nothing yet)"
	}
	if len(srcs) > moveContext {
		srcs = srcs[:moveContext]
	}
	var b strings.Builder
	for _, s := range srcs {
		fmt.Fprintf(&b, "- %s — %s\n", clip(s.Title, 120), s.URL)
	}
	return strings.TrimRight(b.String(), "\n")
}

func trimAll(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if t := strings.TrimSpace(x); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// oneLine collapses whitespace so a model's stray newline cannot break the SSE
// frame's single-line detail.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
