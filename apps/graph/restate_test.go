package graph

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

var codeRelations = []string{"defines", "depends_on", "owner_of", "decides"}

func restated(t *testing.T, s *store, prefix string, at, now time.Time, stated ...client.Stated) *client.Restated {
	t.Helper()
	out, err := restate(context.Background(), s, "acme", &client.Restate{
		Scope: prefix, Relations: codeRelations, At: stamp(at),
		Source: "https://git.example/acme/p/r/commit/" + at.Format("150405"), Stated: stated,
	}, now)
	if err != nil {
		t.Fatalf("restate at %s: %v", at, err)
	}
	return out
}

// held is what a scope holds at an instant, as sorted "entity relation value".
func held(t *testing.T, s *store, prefix string, at time.Time) []string {
	t.Helper()
	rows, err := s.scoped(context.Background(), prefix, codeRelations, historyMax)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range holding(rows, when{valid: at, known: forever}, cardinality{}) {
		out = append(out, k.Entity+" "+k.Relation+" "+k.Value)
	}
	slices.Sort(out)
	return out
}

func count(t *testing.T, s *store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM assertion`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const repo = "code:p/r#"

var (
	defA  = client.Stated{Entity: repo + "a.go", Relation: "defines", Value: repo + "a.go::A", Names: true, Evidence: "a.go"}
	defB  = client.Stated{Entity: repo + "a.go", Relation: "defines", Value: repo + "a.go::B", Names: true, Evidence: "a.go"}
	owner = client.Stated{Entity: "@core", Relation: "owner_of", Value: repo + "apps", Names: true, Evidence: "CODEOWNERS"}
)

// RESTATING THE SAME COMMIT FILES NOTHING. A durable index retries, and a push
// can be redelivered; the graph must read the same after either.
func TestRestatingTheSameThingFilesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		t1 := t0
		now := t0.Add(time.Hour)
		first := restated(t, s, repo, t1, now, defA, defB, owner)
		if first.Begun != 3 || first.Ended != 0 || len(first.Refused) != 0 {
			t.Fatalf("first restatement = %+v, want 3 begun", first)
		}
		rows := count(t, s)
		again := restated(t, s, repo, t1, now.Add(time.Minute), defB, owner, defA)
		if again.Begun != 0 || again.Ended != 0 || again.Stale {
			t.Fatalf("restating the same commit = %+v, want nothing filed", again)
		}
		if count(t, s) != rows {
			t.Fatalf("restating the same commit grew the store from %d to %d rows", rows, count(t, s))
		}
	})
}

// WHAT A LATER COMMIT NO LONGER STATES ENDS THERE, and not before: the graph
// answers for each instant what the code said then. Both restatements are filed
// in the same server second, which is the case a correction by knowable alone
// would lose.
func TestWhatIsNoLongerStatedEndsAtTheLaterCommit(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		t1, t2 := t0, t0.Add(time.Hour)
		now := t2.Add(time.Hour)
		restated(t, s, repo, t1, now, defA, defB)
		out := restated(t, s, repo, t2, now, defA)
		if out.Begun != 0 || out.Ended != 1 {
			t.Fatalf("second restatement = %+v, want one ended", out)
		}
		if got, want := held(t, s, repo, t2), []string{repo + "a.go defines " + repo + "a.go::A"}; !slices.Equal(got, want) {
			t.Errorf("held at the later commit = %v, want %v", got, want)
		}
		if got := held(t, s, repo, t2.Add(-time.Second)); len(got) != 2 {
			t.Errorf("held just before the later commit = %v, want both definitions", got)
		}
		if again := restated(t, s, repo, t2, now, defA); again.Begun != 0 || again.Ended != 0 {
			t.Errorf("restating the later commit again = %+v, want nothing filed", again)
		}
	})
}

// A decision's text is one value at a time: a changed text supersedes the old
// at the commit that changed it.
func TestAChangedPropertySupersedes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		t1, t2 := t0, t0.Add(time.Hour)
		now := t2.Add(time.Hour)
		doc := repo + "docs/adr/0001.md"
		restated(t, s, repo, t1, now, client.Stated{Entity: doc, Relation: "decides", Value: "use SQLite"})
		restated(t, s, repo, t2, now, client.Stated{Entity: doc, Relation: "decides", Value: "use Base"})
		if got, want := held(t, s, repo, t2), []string{doc + " decides use Base"}; !slices.Equal(got, want) {
			t.Errorf("held = %v, want %v", got, want)
		}
		if got, want := held(t, s, repo, t1), []string{doc + " decides use SQLite"}; !slices.Equal(got, want) {
			t.Errorf("held at the first commit = %v, want %v", got, want)
		}
	})
}

// AN OLDER COMMIT ARRIVING LATE CANNOT REWRITE A NEWER ONE. Indexing is durable
// and concurrent, so the commit filed second is not always the commit made second.
func TestALateOlderRestatementIsStale(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		t1, t2 := t0, t0.Add(time.Hour)
		now := t2.Add(time.Hour)
		restated(t, s, repo, t2, now, defA)
		rows := count(t, s)
		out := restated(t, s, repo, t1, now, defA, defB)
		if !out.Stale || out.Begun != 0 || out.Ended != 0 {
			t.Fatalf("late older restatement = %+v, want stale and nothing filed", out)
		}
		if count(t, s) != rows {
			t.Fatal("a stale restatement filed rows")
		}
	})
}

// ONE OWNER OWNS PATHS IN MANY REPOS. Restating one repo ends only that repo's
// ownership, which is why an ending is per statement and never a retraction of
// the owner's whole pair.
func TestARestatementEndsOnlyItsOwnScope(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		t1, t2 := t0, t0.Add(time.Hour)
		now := t2.Add(time.Hour)
		other := "code:p/other#"
		restated(t, s, repo, t1, now, owner)
		restated(t, s, other, t1, now, client.Stated{Entity: "@core", Relation: "owner_of", Value: other + "lib", Names: true})
		restated(t, s, repo, t2, now)
		if got := held(t, s, repo, t2); len(got) != 0 {
			t.Errorf("repo r still holds %v after a restatement without it", got)
		}
		if got, want := held(t, s, other, t2), []string{"@core owner_of " + other + "lib"}; !slices.Equal(got, want) {
			t.Errorf("repo other holds %v, want %v", got, want)
		}
	})
}

// A restatement speaks for its relations and its scope and nothing else: a
// statement outside either is refused by name, and an assertion anybody filed
// under another relation is never ended by it.
func TestARestatementSpeaksOnlyForItsRelationsAndScope(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		review := mk(t, repo+"a.go", "reviewed_by", "@z", true, "people", t0)
		if _, err := s.record(ctx, []Fact{review}); err != nil {
			t.Fatal(err)
		}
		now := t0.Add(2 * time.Hour)
		out := restated(t, s, repo, t0.Add(time.Hour), now, defA,
			client.Stated{Entity: repo + "a.go", Relation: "reviewed_by", Value: "@y", Names: true},
			client.Stated{Entity: "code:p/elsewhere#x", Relation: "defines", Value: "code:p/elsewhere#x::X", Names: true})
		if out.Begun != 1 || out.Ended != 0 || len(out.Refused) != 2 {
			t.Fatalf("restatement = %+v, want one begun and two refused", out)
		}
		rows, err := s.read(ctx, filter{Entity: repo + "a.go", Relation: "reviewed_by"})
		if err != nil || len(rows) != 1 || !rows[0].Until.IsZero() {
			t.Fatalf("the review was touched: %+v, %v", rows, err)
		}
	})
}

// A scope is required: a restatement that names none would be
// whole about nothing, and one naming the schema's relations would rewrite it.
func TestARestatementNamesWhatItIsWholeAbout(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		for _, in := range []client.Restate{
			{Relations: codeRelations, At: stamp(t0), Source: "s"},
			{Scope: repo, At: stamp(t0), Source: "s"},
			{Scope: repo, Relations: []string{"type"}, At: stamp(t0), Source: "s"},
			{Scope: repo, Relations: codeRelations, At: stamp(t0)},
			{Scope: repo, Relations: codeRelations, At: "yesterday", Source: "s"},
		} {
			if _, err := restate(context.Background(), s, "acme", &in, t0); err == nil {
				t.Errorf("restate(%+v) was accepted", in)
			}
		}
	})
}

// A RESTATEMENT NEVER DECLARES. A declaration binds what every member may assert,
// so only an admin files one; a statement of one is refused, and one an admin
// filed in the scope is never ended by the restatement that does not repeat it.
func TestARestatementNeverTouchesADeclaration(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		decl := mk(t, "relation:owner_of", "owner_of", repo+"apps", true, "admin", t0)
		if _, err := s.record(ctx, []Fact{decl}); err != nil {
			t.Fatal(err)
		}
		now := t0.Add(2 * time.Hour)
		out := restated(t, s, repo, t0.Add(time.Hour), now, defA,
			client.Stated{Entity: "relation:owner_of", Relation: "owner_of", Value: repo + "web", Names: true})
		if out.Begun != 1 || out.Ended != 0 || len(out.Refused) != 1 {
			t.Fatalf("restatement = %+v, want one begun, none ended, the declaration refused", out)
		}
		rows, err := s.read(ctx, filter{Entity: "relation:owner_of", Relation: "owner_of"})
		if err != nil || len(rows) != 1 || !rows[0].Until.IsZero() {
			t.Fatalf("the declaration was touched: %+v, %v", rows, err)
		}
	})
}

// AN END STILL AHEAD WHEN IT WAS FILED IS A FORECAST, NOT A LATER RESTATEMENT.
// A member who files that a dependency ends next year does not freeze the repo's
// graph until then; the next restatement that no longer states it ends it.
func TestAForecastEndIsNotALaterRestatement(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		wrote := t0.Add(time.Minute)
		dep, err := stamped(Fact{
			Entity: repo + "cmd", Relation: "depends_on", Value: repo + "store", Names: true,
			At: t0, Until: t0.AddDate(1, 0, 0), Source: "plan", By: "acme", Confidence: 1,
		}, wrote)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.record(ctx, []Fact{dep}); err != nil {
			t.Fatal(err)
		}
		t1 := t0.Add(time.Hour)
		out := restated(t, s, repo, t1, t1.Add(time.Hour), defA)
		if out.Stale || out.Begun != 1 || out.Ended != 1 {
			t.Fatalf("restatement after a forecast = %+v, want one begun and the forecast ended", out)
		}
		if got, want := held(t, s, repo, t1), []string{repo + "a.go defines " + repo + "a.go::A"}; !slices.Equal(got, want) {
			t.Errorf("held = %v, want %v", got, want)
		}
	})
}

// A scope and its relations are keys, folded as the keys they match are: a
// scope sent decomposed restates the same repo as one sent composed.
func TestARestatementFoldsItsScope(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		composed, decomposed := "code:p/café#", "code:p/café#"
		now := t0.Add(2 * time.Hour)
		stated := client.Stated{Entity: composed + "a.go", Relation: "defines", Value: composed + "a.go::A", Names: true}
		restated(t, s, composed, t0, now, stated)
		out, err := restate(context.Background(), s, "acme", &client.Restate{
			Scope: decomposed, Relations: []string{" defines ", "depends_on", "owner_of", "decides"},
			At: stamp(t0.Add(time.Hour)), Source: "s", Stated: []client.Stated{stated},
		}, now)
		if err != nil || out.Begun != 0 || out.Ended != 0 || len(out.Refused) != 0 {
			t.Fatalf("restating a folded scope = %+v, %v; want nothing filed", out, err)
		}
	})
}

// TWO RESTATEMENTS IN ONE SECOND BOTH FILE. The store keeps seconds, and a
// statement cannot end at the instant it began, so the second follows the first
// by a second rather than refusing to end what it no longer states.
func TestTwoRestatementsInOneSecondBothFile(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		now := t0.Add(time.Hour)
		restated(t, s, repo, t0, now, defA, defB)
		out := restated(t, s, repo, t0, now, defA)
		if out.Stale || out.Ended != 1 || len(out.Refused) != 0 {
			t.Fatalf("second restatement in the same second = %+v, want defB ended", out)
		}
		if got, want := held(t, s, repo, t0.Add(time.Second)), []string{repo + "a.go defines " + repo + "a.go::A"}; !slices.Equal(got, want) {
			t.Errorf("held after both = %v, want %v", got, want)
		}
	})
}

// A RESET TO AN OLDER TREE IS A LATER RESTATEMENT. The instant is the push's, so
// a branch reset back to a commit that dropped a definition ends it there.
func TestAResetToAnOlderTreeEndsWhatItDoesNotState(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		t1, t2 := t0, t0.Add(time.Hour)
		now := t2.Add(time.Hour)
		restated(t, s, repo, t1, now, defA)
		restated(t, s, repo, t2, now, defA, defB)
		out := restated(t, s, repo, t2.Add(time.Minute), now, defA)
		if out.Stale || out.Ended != 1 {
			t.Fatalf("the reset = %+v, want defB ended", out)
		}
	})
}

// A RESTATEMENT IS BOUNDED. It states at most client.RestateMax, and a scope is
// read no further than one row past its bound, so a push decides neither how
// much is filed nor how much is held in memory to diff against.
func TestARestatementIsBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		over := make([]client.Stated, client.RestateMax+1)
		for i := range over {
			over[i] = defA
		}
		_, err := restate(ctx, s, "acme", &client.Restate{
			Scope: repo, Relations: codeRelations, At: stamp(t0), Source: "s", Stated: over,
		}, t0)
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusRequestEntityTooLarge {
			t.Fatalf("a restatement over the bound = %v, want 413", err)
		}
		restated(t, s, repo, t0, t0.Add(time.Hour), defA, defB, owner)
		rows, err := s.scoped(ctx, repo, codeRelations, 1)
		if err != nil || len(rows) != 2 {
			t.Fatalf("a scope read bounded at 1 = %d rows, %v; want 2", len(rows), err)
		}
	})
}
