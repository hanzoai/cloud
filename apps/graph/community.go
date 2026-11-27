package graph

// community.go partitions the edge graph into communities: sets of entities more
// densely connected to each other than to the rest of the graph.
//
// THE GRAPH IS THE ONE A WALK READS: the edges in force at the instant, as known
// then, each pair resolved by the one rule a walk follows (edges). An edge that
// was retracted, that ran past its own until or, under a relation declared to
// hold one value, that a later one superseded is not in it; an entity that
// `depends` on two services depends on both, because an edge relation holds many
// values unless the organization declares otherwise. Direction is dropped
// because belonging together is symmetric, and an edge asserted twice, or by two
// sources, is one edge: weight counts the distinct relations joining a pair, not
// how often somebody said so.
//
// THE ALGORITHM IS LOUVAIN, deterministic. Nodes are visited in key order and a
// tie keeps the node where it is, else goes to the lower community, so the same
// graph gives the same partition on every machine and every run — which is what
// lets an answer cite a community by number. Louvain's best-known defect, a
// community whose parts are not connected to each other, is closed after each
// pass by splitting every community into its connected components. That split
// can only raise modularity, and it keeps the levels nested.
//
// NOTHING IS STORED. A partition is derived from the assertions and recomputed on
// demand; a second table would be a second answer able to go stale. At the
// ceiling — 100,000 edges over 20,000 entities (BenchmarkDetect,
// BenchmarkPartition) — detection takes about 0.1s and the whole partition, read
// included, about 0.3s: less than one model call an answer makes, and not worth
// a cache.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/zap-proto/zip"
)

// edgeMax is the ceiling on the edge assertions one partition reads. It bounds
// the READ, because the read is what holds the organization's one connection:
// every row counts, so an edge asserted by three sources is three rows of work
// and one edge of graph. Past it the request is REFUSED rather than partitioned
// from a truncated read: communities found in whichever part of the graph the
// ceiling happened to keep are wrong about the part it dropped, and nothing in
// the answer could say where.
const edgeMax = 100000

// sweepMax bounds the local moves in one Louvain pass. It converges long before
// this on any graph measured; the bound exists so a pathological one cannot hold
// a request open.
const sweepMax = 64

// link is one undirected edge of the partitioned graph.
type link struct {
	a, b string
	w    float64
}

// links reads the edges in force at a point, narrowed to one relation when one is
// named: the edges and the retractions that can end them, begun and known by the
// point, resolved by the rule a walk uses (edges). It answers false, not a
// short read, when the rows are over max.
//
// The weight between two entities is how many relations join them, so an edge
// two sources assert is one edge, and one pair's edge is counted once.
func (s *store) links(ctx context.Context, relation string, w when, max int) ([]link, bool, error) {
	q := `SELECT ` + cols + ` FROM assertion
		WHERE at <= $1 AND knowable <= $2 AND ((names = 1 AND entity <> value) OR (names = 0 AND value = ''))`
	args := []any{w.valid.Unix(), w.known.Unix()}
	if relation != "" {
		args = append(args, relation)
		q += ` AND relation = ` + mark(args)
	}
	lim, args := bound(args, max+1)
	facts, err := s.query(ctx, q+lim, args...)
	if err != nil {
		return nil, false, err
	}
	if len(facts) > max {
		return nil, false, nil
	}
	card, err := s.arity(ctx, relationsOf(facts), w.known)
	if err != nil {
		return nil, false, err
	}
	joined := map[[3]string]bool{}
	for _, e := range edges(facts, w, card) {
		joined[[3]string{e.Entity, e.Relation, e.Value}] = true
	}
	weight := map[[2]string]float64{}
	for k := range joined {
		weight[[2]string{k[0], k[2]}]++
	}
	out := make([]link, 0, len(weight))
	for k, w := range weight {
		out = append(out, link{k[0], k[1], w})
	}
	return out, true, nil
}

// arc is one weighted adjacency.
type arc struct {
	to int32
	w  float64
}

// wgraph is an undirected weighted graph. A self-loop lives in loop rather than
// in adj, because a node's own weight moves with it and is never a neighbour.
type wgraph struct {
	adj  [][]arc
	loop []float64
}

// degree is the weighted degree: every arc once, a self-loop twice.
func (g *wgraph) degree(i int) float64 {
	d := 2 * g.loop[i]
	for _, a := range g.adj[i] {
		d += a.w
	}
	return d
}

// partition is what detection found. levels[0] is the coarsest partition — the
// one Louvain stops at, of highest modularity — and each level after it is the
// pass before, finer. Every level maps a node to a community numbered by size,
// largest first, then by its lowest key, so numbering is as deterministic as
// membership.
type partition struct {
	keys       []string
	degree     []float64
	levels     [][]int32
	modularity []float64
	edges      int
}

// detect partitions the graph the links describe.
func detect(links []link) partition {
	// Keys in order, so a node's index — and therefore the visiting order — is a
	// function of the graph and not of how it was read.
	index := map[string]int32{}
	for _, l := range links {
		index[l.a], index[l.b] = 0, 0
	}
	keys := make([]string, 0, len(index))
	for k := range index {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for i, k := range keys {
		index[k] = int32(i)
	}

	// An edge read both ways is one undirected edge carrying both weights.
	weight := map[[2]int32]float64{}
	for _, l := range links {
		a, b := index[l.a], index[l.b]
		weight[[2]int32{min(a, b), max(a, b)}] += l.w
	}
	g := &wgraph{adj: make([][]arc, len(keys)), loop: make([]float64, len(keys))}
	for e, w := range weight {
		g.adj[e[0]] = append(g.adj[e[0]], arc{e[1], w})
		g.adj[e[1]] = append(g.adj[e[1]], arc{e[0], w})
	}
	for i := range g.adj {
		slices.SortFunc(g.adj[i], func(x, y arc) int { return int(x.to - y.to) })
	}

	p := partition{keys: keys, degree: make([]float64, len(keys)), edges: len(weight)}
	for i := range keys {
		p.degree[i] = g.degree(i)
	}
	passes := louvain(g)
	for i := len(passes) - 1; i >= 0; i-- {
		comm := number(split(g, passes[i]))
		p.levels = append(p.levels, comm)
		p.modularity = append(p.modularity, modularity(g, comm))
	}
	return p
}

// louvain runs passes until one moves nothing, and returns each pass's partition
// of the ORIGINAL nodes, finest first. A graph where no move helps is one pass of
// singletons.
func louvain(g *wgraph) [][]int32 {
	n := len(g.adj)
	member := make([]int32, n)
	for i := range member {
		member[i] = int32(i)
	}
	var passes [][]int32
	cur := g
	for {
		comm, moved := move(cur)
		if !moved {
			break
		}
		comm, k := renumber(comm)
		next := make([]int32, n)
		for v := range member {
			next[v] = comm[member[v]]
		}
		member = next
		passes = append(passes, member)
		if k == len(cur.adj) {
			break
		}
		cur = aggregate(cur, comm, k)
	}
	if len(passes) == 0 {
		passes = append(passes, member)
	}
	return passes
}

// move is one pass of local moves: each node in turn joins the neighbouring
// community that gains the most modularity, until a sweep moves nothing.
func move(g *wgraph) ([]int32, bool) {
	n := len(g.adj)
	var m2 float64
	k := make([]float64, n)
	for i := range n {
		k[i] = g.degree(i)
		m2 += k[i]
	}
	comm := make([]int32, n)
	tot := make([]float64, n)
	for i := range n {
		comm[i] = int32(i)
		tot[i] = k[i]
	}
	if m2 == 0 {
		return comm, false
	}

	// Scratch reused across nodes: the weight from the node into each
	// neighbouring community, and the order those communities were first met.
	into := make([]float64, n)
	met := make([]bool, n)
	var near []int32
	const eps = 1e-12

	moved := false
	for range sweepMax {
		changed := false
		for i := range n {
			own := comm[i]
			near = append(near[:0], own)
			met[own] = true
			for _, a := range g.adj[i] {
				c := comm[a.to]
				if !met[c] {
					met[c] = true
					near = append(near, c)
				}
				into[c] += a.w
			}
			tot[own] -= k[i]

			best, gain := own, into[own]-tot[own]*k[i]/m2
			for _, c := range near[1:] {
				d := into[c] - tot[c]*k[i]/m2
				if d > gain+eps || (d > gain-eps && best != own && c < best) {
					best, gain = c, d
				}
			}
			tot[best] += k[i]
			if best != own {
				comm[i] = best
				changed, moved = true, true
			}
			for _, c := range near {
				into[c], met[c] = 0, false
			}
		}
		if !changed {
			break
		}
	}
	return comm, moved
}

// renumber maps community labels onto 0..k-1 in order of first appearance.
func renumber(comm []int32) ([]int32, int) {
	seen := map[int32]int32{}
	out := make([]int32, len(comm))
	for i, c := range comm {
		id, ok := seen[c]
		if !ok {
			id = int32(len(seen))
			seen[c] = id
		}
		out[i] = id
	}
	return out, len(seen)
}

// aggregate collapses each community into one node. The weight inside a
// community becomes that node's self-loop, so the next pass weighs it exactly as
// this one did.
func aggregate(g *wgraph, comm []int32, k int) *wgraph {
	next := &wgraph{adj: make([][]arc, k), loop: make([]float64, k)}
	sums := make([]map[int32]float64, k)
	for i := range g.adj {
		ci := comm[i]
		next.loop[ci] += g.loop[i]
		for _, a := range g.adj[i] {
			cj := comm[a.to]
			if cj == ci {
				// Each internal edge is met from both ends.
				next.loop[ci] += a.w / 2
				continue
			}
			if sums[ci] == nil {
				sums[ci] = map[int32]float64{}
			}
			sums[ci][cj] += a.w
		}
	}
	for c, s := range sums {
		for to, w := range s {
			next.adj[c] = append(next.adj[c], arc{to, w})
		}
		slices.SortFunc(next.adj[c], func(x, y arc) int { return int(x.to - y.to) })
	}
	return next
}

// split breaks every community into its connected components, so no community
// is two groups that merely share a label.
func split(g *wgraph, comm []int32) []int32 {
	parent := make([]int32, len(comm))
	for i := range parent {
		parent[i] = int32(i)
	}
	find := func(x int32) int32 {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i := range g.adj {
		for _, a := range g.adj[i] {
			if comm[i] != comm[a.to] {
				continue
			}
			if x, y := find(int32(i)), find(a.to); x != y {
				parent[max(x, y)] = min(x, y)
			}
		}
	}
	out := make([]int32, len(comm))
	for i := range out {
		out[i] = find(int32(i))
	}
	return out
}

// number labels communities by size, largest first, then by lowest node index,
// which is lowest key.
func number(comm []int32) []int32 {
	size := map[int32]int{}
	first := map[int32]int{}
	for i, c := range comm {
		if _, ok := first[c]; !ok {
			first[c] = i
		}
		size[c]++
	}
	labels := make([]int32, 0, len(size))
	for c := range size {
		labels = append(labels, c)
	}
	slices.SortFunc(labels, func(x, y int32) int {
		if size[x] != size[y] {
			return size[y] - size[x]
		}
		return first[x] - first[y]
	})
	id := make(map[int32]int32, len(labels))
	for i, c := range labels {
		id[c] = int32(i)
	}
	out := make([]int32, len(comm))
	for i, c := range comm {
		out[i] = id[c]
	}
	return out
}

// modularity is Newman's Q of a partition numbered 0..k-1: the share of edge
// weight inside communities, less what a random graph with the same degrees
// would put there. It sums in community order, so the same partition reports
// the same number to the last bit.
func modularity(g *wgraph, comm []int32) float64 {
	var k int32
	for _, c := range comm {
		k = max(k, c+1)
	}
	in := make([]float64, k)
	deg := make([]float64, k)
	var m2 float64
	for i := range g.adj {
		c := comm[i]
		d := g.degree(i)
		m2 += d
		deg[c] += d
		in[c] += 2 * g.loop[i]
		for _, a := range g.adj[i] {
			if comm[a.to] == c {
				in[c] += a.w
			}
		}
	}
	if m2 == 0 {
		return 0
	}
	var q float64
	for c := range k {
		q += in[c]/m2 - math.Pow(deg[c]/m2, 2)
	}
	return q
}

// members lists each community's nodes at one level, in key order.
func (p partition) members(level int) [][]int32 {
	comm := p.levels[level]
	var k int32
	for _, c := range comm {
		k = max(k, c+1)
	}
	out := make([][]int32, k)
	for i, c := range comm {
		out[c] = append(out[c], int32(i))
	}
	return out
}

// partitionAt reads the edge graph at an instant and partitions it, refusing a
// graph over the ceiling by name.
func partitionAt(ctx context.Context, st *store, relation string, w when) (partition, error) {
	links, ok, err := st.links(ctx, relation, w, edgeMax)
	if err != nil {
		return partition{}, err
	}
	if !ok {
		return partition{}, errTooMany
	}
	return detect(links), nil
}

// errTooMany is the ceiling, stated as the refusal a caller acts on.
var errTooMany = fmt.Errorf("the edge graph holds more than %d edge assertions at this instant; "+
	"narrow it with relation or an earlier as_of", edgeMax)

// ── the wire ─────────────────────────────────────────────────────────────────

type graphCommunitiesIn struct {
	// AsOf partitions the graph as it stood at an instant of the world, RFC
	// 3339: the edges that held then. Absent is now.
	AsOf string `json:"as_of,omitempty"`
	// AsKnown is how much this plane had heard, RFC 3339: the answer as it would
	// have been given then, which is what makes a past answer reproducible.
	// Absent is now.
	AsKnown string `json:"as_known,omitempty"`
	// Level is how fine a partition to answer with. 0, the default, is the
	// coarsest — the partition detection stops at, where modularity is highest —
	// and each level above it is the pass before, finer. `levels` in the answer
	// says how many there are.
	Level int `json:"level,omitempty"`
	// Relation narrows the graph to one edge relation, by its key. Absent
	// partitions every edge.
	Relation string `json:"relation,omitempty"`
}

// graphCommunity is one community.
type graphCommunity struct {
	// ID numbers the community by size, largest first, then by its lowest key.
	// The same graph at the same instant and level numbers it the same way every
	// time; another instant may not.
	ID int `json:"id"`
	// Size is how many entities belong to it, whether or not all are listed.
	Size int `json:"size"`
	// Members are the entities in it, in key order.
	Members []string `json:"members"`
}

type graphCommunitiesOut struct {
	// AsOf is the instant the graph was read at, RFC 3339: the one asked for, or
	// the server's clock when none was.
	AsOf string `json:"as_of"`
	// AsKnown is the knowledge instant it was taken at, RFC 3339, the same way.
	AsKnown string `json:"as_known"`
	// Level is the level answered.
	Level int `json:"level"`
	// Levels is how many levels detection found. A graph where no two entities
	// belong together more than chance has one, of single entities.
	Levels int `json:"levels"`
	// Modularity is Newman's Q of this level's partition, from -0.5 to 1: the
	// share of edge weight inside communities, less what chance would put there.
	// Above about 0.3 is usually read as real structure rather than chance.
	Modularity float64 `json:"modularity"`
	// Entities is how many entities the edge graph holds. An entity with no edge
	// in force at AsOf is in no community.
	Entities int `json:"entities"`
	// Edges is how many distinct undirected edges were partitioned.
	Edges int `json:"edges"`
	// Ceiling is the most edge assertions one partition reads — every row, so an
	// edge three sources asserted counts three times. A graph over it is refused
	// rather than partitioned from part of itself.
	Ceiling int `json:"ceiling"`
	// Communities are the communities, largest first.
	Communities []graphCommunity `json:"communities"`
	// Truncated says the member lists stop at Bound entities in all, filled in
	// community order. Every community is still listed with its full Size.
	Truncated bool `json:"truncated"`
	// Bound is the most members listed in one answer.
	Bound int `json:"bound"`
}

// communities partitions the edge graph into sets of entities more densely
// connected to each other than to the rest.
//
// The graph is the one a walk reads, direction dropped, and the partition is
// deterministic: the same graph at the same point numbers the same communities.
//
// Time: as_of and as_known place the graph that is partitioned; either absent is
// now.
//
// Example: {"as_of": "2026-09-01T00:00:00Z"}
func (o ops) communities(ctx context.Context, in *graphCommunitiesIn) (*graphCommunitiesOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	w, err := moment(in.AsOf, in.AsKnown)
	if err != nil {
		return nil, err
	}
	p, err := partitionAt(ctx, st, key(in.Relation), w)
	if errors.Is(err, errTooMany) {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if err != nil {
		return nil, err
	}
	if in.Level < 0 || in.Level >= len(p.levels) {
		return nil, zip.ErrBadRequest(fmt.Sprintf("level %d is not one of the %d this graph has, 0 to %d", in.Level, len(p.levels), len(p.levels)-1))
	}
	out := &graphCommunitiesOut{
		AsOf: w.valid.Format(time.RFC3339), AsKnown: w.known.Format(time.RFC3339), Level: in.Level, Levels: len(p.levels),
		Modularity: p.modularity[in.Level], Entities: len(p.keys), Edges: p.edges,
		Ceiling: edgeMax, Communities: []graphCommunity{}, Bound: walkBound,
	}
	listed := 0
	for id, ms := range p.members(in.Level) {
		c := graphCommunity{ID: id, Size: len(ms), Members: []string{}}
		for _, m := range ms {
			if listed == walkBound {
				out.Truncated = true
				break
			}
			c.Members = append(c.Members, p.keys[m])
			listed++
		}
		out.Communities = append(out.Communities, c)
	}
	return out, nil
}
