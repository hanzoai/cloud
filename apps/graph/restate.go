package graph

// restate.go is how a source that knows the WHOLE of something files it. Not
// one claim at a time, but "as of this instant, this is everything I state about
// this scope": what it states that is not held begins at the instant, and what is
// held that it no longer states ends there. A source that restates on every
// change leaves a history; one that restates the same thing twice files nothing.
//
// Ending is the same statement filed again with its Until, which is how this
// plane has always corrected a statement (versions). No retraction of a pair is
// filed, because a pair can span scopes — one owner owns paths in many repos —
// and a void would end the rest.
//
// It is a PLANE op. The source is another process of the fleet — the code index,
// on a push — acting for the org and project stated on the call.

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// exposeRestate publishes the op. Use calls it.
func exposeRestate() {
	cloud.Plane().Post("/graph/restate", planeRestate,
		zip.WithOperationID(client.GraphRestate),
		zip.WithSummary("File everything a source now states about a scope, as of one instant"))
}

// restating serializes the restatements of one org: each reads what its scope
// holds and writes the difference, so two interleaved on one scope would each
// diff against a hold the other is changing, and one at a time per org bounds
// what a tenant's pushes can hold in this process at once.
var restating sync.Map // org → *sync.Mutex

// A restatement is bounded three ways, because a push decides its size and a
// push costs its author nothing: what it states (client.RestateMax), the history
// of its scope it reads to diff against (historyMax), and how much the graph it
// files into may already hold (graphMax). Past any bound it is refused whole —
// part of a whole would end the rest.
const (
	historyMax = 5 * client.RestateMax
	graphMax   = 1_000_000
)

// planeRestate files a restatement into the graph of the org and project the
// call acts for. The org is the CALLER's plane identity, never the argument, and
// every assertion it files is by that org.
func planeRestate(ctx context.Context, in *client.Restate) (*client.Restated, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("graph restate: org required")
	}
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "graph not mounted")
	}
	project := principal.ProjectScopeFrom(ctx)
	ns, err := principal.OrgNamespace(org, project)
	if err != nil {
		return nil, err
	}
	st, err := mounted.stores.For(ns)
	if err != nil {
		return nil, err
	}
	mu, _ := restating.LoadOrStore(org, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	return restate(ctx, st, org, in, time.Now().UTC())
}

// restate is the op against one tenant's store, at a server instant. What it
// reads, what it judges and what it files are one transaction (write), so its
// ends and its begins land together or not at all, and no write lands between
// the diff and the insert.
//
// The scope and the relations are keys, folded as admission folds the keys they
// are matched against. A declaration is never filed or ended by a restatement:
// it binds what every member may assert, so only an admin files one (judge).
func restate(ctx context.Context, st *store, by string, in *client.Restate, now time.Time) (*client.Restated, error) {
	scope := key(in.Scope)
	if scope == "" || !utf8.ValidString(scope) {
		return nil, zip.ErrBadRequest("a restatement names its scope")
	}
	relations := folded(in.Relations)
	if len(relations) == 0 {
		return nil, zip.ErrBadRequest("a restatement names the relations it speaks for")
	}
	for _, r := range relations {
		if r == "" || declares(r) {
			return nil, zip.ErrBadRequest(fmt.Sprintf("%q is not a relation a restatement can speak for", r))
		}
	}
	if strings.TrimSpace(in.Source) == "" {
		return nil, zip.ErrBadRequest("a restatement names its source")
	}
	if len(in.Stated) > client.RestateMax {
		return nil, tooLarge("a restatement states at most %d statements; this one states %d", client.RestateMax, len(in.Stated))
	}
	at, err := instant(in.At)
	if err != nil {
		return nil, err
	}
	// The store keeps seconds, so the instant is compared as one.
	at = at.Truncate(time.Second)

	out := &client.Restated{}
	_, err = st.write(ctx, func(st *store) ([]Fact, error) {
		if n, err := st.size(ctx); err != nil {
			return nil, err
		} else if n > graphMax {
			return nil, tooLarge("this graph holds %d assertions, and a restatement files into one holding at most %d", n, graphMax)
		}
		rows, err := st.scoped(ctx, scope, relations, historyMax)
		if err != nil {
			return nil, err
		}
		if len(rows) > historyMax {
			return nil, tooLarge("%s has more than %d assertions of history, the most a restatement reads", scope, historyMax)
		}
		rows = slices.DeleteFunc(rows, func(f Fact) bool { return schematic(f.Entity) })
		// A restatement follows the last one filed. One filed in the same second
		// follows it by a second, since a statement cannot end at the instant it
		// began; one older than it is stale.
		switch last := latest(rows); {
		case last.After(at):
			out.Stale = true
			return nil, nil
		case last.Equal(at):
			at = at.Add(time.Second)
		}
		want, refused := stating(in, scope, relations, at, by, now)
		out.Refused = refused
		card, err := st.arity(ctx, relations, now)
		if err != nil {
			return nil, err
		}
		held := map[statement]Fact{}
		for _, f := range holding(rows, when{valid: at, known: forever}, card) {
			held[statementOf(f)] = f
		}

		var begin, end []Fact
		for k, f := range want {
			if _, ok := held[k]; !ok {
				begin = append(begin, f)
			}
		}
		for k, h := range held {
			if _, ok := want[k]; ok {
				continue
			}
			e, err := ending(h, at, in.Source, by, now)
			if err != nil {
				out.Refused = append(out.Refused, err.Error())
				continue
			}
			end = append(end, e)
		}
		slices.SortFunc(begin, said)
		slices.SortFunc(end, said)

		bad, err := st.conform(ctx, begin)
		if err != nil {
			return nil, err
		}
		for i, f := range begin {
			if bad[i] != "" {
				out.Refused = append(out.Refused, bad[i])
				continue
			}
			end = append(end, f)
		}
		return end, nil
	}, func(fresh []Fact) error {
		// A restatement begins what it states with no Until and ends what it no
		// longer states with one, so the rows it made say which each was.
		for _, f := range fresh {
			if f.Until.IsZero() {
				out.Begun++
			} else {
				out.Ended++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// stating is what a restatement states, admitted as of its instant, and why each
// statement it cannot file was refused.
func stating(in *client.Restate, scope string, relations []string, at time.Time, by string, now time.Time) (map[statement]Fact, []string) {
	want := map[statement]Fact{}
	var refused []string
	for _, s := range in.Stated {
		f, err := stamped(Fact{
			Entity: s.Entity, Relation: s.Relation, Value: s.Value, Names: s.Names,
			At: at, Seen: at, Source: in.Source, Evidence: s.Evidence, By: by, Confidence: 1,
		}, now)
		switch {
		case err != nil:
		case schematic(f.Entity):
			err = fmt.Errorf("%s is a declaration, which a restatement never files", f.Entity)
		case !slices.Contains(relations, f.Relation):
			err = fmt.Errorf("%s %s %s: %s is not a relation this restatement speaks for", f.Entity, f.Relation, f.Value, f.Relation)
		case !within(f, scope):
			err = fmt.Errorf("%s %s %s: outside %s", f.Entity, f.Relation, f.Value, scope)
		}
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		want[statementOf(f)] = f
	}
	return want, refused
}

// tooLarge refuses a restatement past one of its bounds.
func tooLarge(format string, args ...any) error {
	return zip.Errorf(http.StatusRequestEntityTooLarge, format, args...)
}

// statement is what versions of one claim share: its pair, its value and
// whether that value names an entity. At is left out, because a restatement
// asks whether a thing is held, not since when.
type statement struct {
	pair
	value string
	names bool
}

func statementOf(f Fact) statement {
	return statement{pair{f.Entity, f.Relation}, f.Value, f.Names}
}

// within reports whether an assertion is about something under a scope: its
// entity, or the entity its edge names.
func within(f Fact, scope string) bool {
	return strings.HasPrefix(f.Entity, scope) || (f.Names && strings.HasPrefix(f.Value, scope))
}

// latest is the last instant rows began or ended anything: the last
// restatement of their scope. An end that was still ahead when it was filed is a
// forecast, which no restatement files — an ending is known no earlier than it
// ends — so it is not one.
func latest(rows []Fact) time.Time {
	var last time.Time
	for _, f := range rows {
		if f.At.After(last) {
			last = f.At
		}
		if f.Until.After(last) && !f.Until.After(f.Knowable) {
			last = f.Until
		}
	}
	return last
}

// ending is the version of a held statement that ends it at an instant: the
// same statement filed again with its Until. It is known a second after the
// version it corrects at the earliest, because a correction known in the same
// second as what it corrects is kept beside it and corrects nothing.
func ending(h Fact, at time.Time, source, by string, now time.Time) (Fact, error) {
	seen := h.Knowable.Add(time.Second)
	if at.After(seen) {
		seen = at
	}
	return stamped(Fact{
		Entity: h.Entity, Relation: h.Relation, Value: h.Value, Names: h.Names,
		At: h.At, Until: at, Seen: seen, Source: source, Evidence: h.Evidence, By: by,
		Confidence: h.Confidence,
	}, now)
}

// said orders assertions by what they say, so a restatement files in one order
// whatever order it was stated in.
func said(a, b Fact) int {
	return cmp.Or(strings.Compare(a.Entity, b.Entity), strings.Compare(a.Relation, b.Relation), strings.Compare(a.Value, b.Value))
}
