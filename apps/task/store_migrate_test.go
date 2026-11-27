package task

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/migratetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// legacyIssuesDDL is the issues table as it existed BEFORE the polymorphic-spine
// columns (kind/source/repo/ext_ref) were introduced — the shape a production
// the store carries today. A migrate() that indexes repo/kind before ALTER-adding
// them fails here with "no such column", which fails mount and crashloops the pod.
const legacyIssuesDDL = `
CREATE TABLE issues (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL,
  org          TEXT NOT NULL,
  number       INTEGER NOT NULL,
  title        TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'backlog',
  priority     TEXT NOT NULL DEFAULT 'none',
  assignee     TEXT NOT NULL DEFAULT '',
  labels       TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);`

// TestMigrateOverLegacyIssuesTable reproduces the deploy crashloop: opening a
// a store whose issues table predates the spine columns must migrate cleanly,
// not error on "CREATE INDEX ix_issues_org_repo ... no such column: repo".
func TestMigrateOverLegacyIssuesTable(t *testing.T) {
	migratetest.Case{
		Name:      "task",
		LegacyDDL: legacyIssuesDDL,
		Open: func(dir string) (io.Closer, error) {
			db, err := cloud.OrgDB(dir, namespace.System(), store)
			if err != nil {
				return nil, err
			}
			st, err := openStore(db)
			if err != nil {
				return nil, err
			}
			return st, nil
		},
		// The forward-added columns must now exist and be usable — create an issue
		// carrying a repo + kind (the write ix_issues_org_repo / ix_issues_org_kind
		// index) AND a schedule (the write ix_issues_org_project_due index), then
		// read it back scoped by org.
		Probe: func(t *testing.T, c io.Closer) {
			st := c.(*Store)
			ctx := context.Background()
			iss, err := st.CreateIssue(ctx, Issue{
				ID: "iss_1", ProjectID: "proj_1", Org: "acme",
				Kind: "pr", Source: "git", Repo: "cloud", ExtRef: "feat/x",
				Title: "t", Status: "backlog", StartAt: 1700, DueAt: 1900,
				CreatedAt: 1, UpdatedAt: 1,
			})
			if err != nil {
				t.Fatalf("create issue after migrate: %v", err)
			}
			got, err := st.GetIssue(ctx, "acme", "proj_1", iss.Number)
			if err != nil {
				t.Fatalf("get issue after migrate: %v", err)
			}
			if got.Repo != "cloud" || got.Kind != "pr" {
				t.Fatalf("issue spine columns = (repo=%q, kind=%q), want (cloud, pr)", got.Repo, got.Kind)
			}
			if got.StartAt != 1700 || got.DueAt != 1900 {
				t.Fatalf("issue schedule columns = (%d,%d), want (1700,1900)", got.StartAt, got.DueAt)
			}
			// A row that predates the schedule reads as unscheduled rather than as a
			// bar at the epoch, and the timeline filter therefore leaves it alone.
			legacy, err := st.CreateIssue(ctx, Issue{
				ID: "iss_2", ProjectID: "proj_1", Org: "acme",
				Title: "predates the timeline", Status: "backlog", CreatedAt: 1, UpdatedAt: 1,
			})
			if err != nil {
				t.Fatalf("create undated issue: %v", err)
			}
			if legacy.StartAt != 0 || legacy.DueAt != 0 {
				t.Fatalf("undated issue = (%d,%d), want (0,0)", legacy.StartAt, legacy.DueAt)
			}
			timeline, err := st.ListIssues(ctx, "acme", "proj_1", IssueFilter{Scheduled: true})
			if err != nil || len(timeline) != 1 || timeline[0].ID != "iss_1" {
				t.Fatalf("timeline after migrate: %+v err=%v, want just iss_1", timeline, err)
			}
		},
	}.Run(t)
}

// Two pods starting at once migrate a table that predates the request column once between
// them, an old pod that is still running keeps inserting by its explicit columns, and a
// request is filed once from every pod: sixteen callers of one request over two stores
// answer one number and leave one row. The same request in another project is its own row.
//
// MUTATION: look for the request without the unique index (or file it outside the one
// transaction) and the callers file more than one row, or the second migration fails.
func TestTwoPodsMigrateTheIssuesTableAndFileARequestOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ns := namespace.System()
		first := open(ns, "issues-once")
		if _, err := first.Exec(legacyIssuesDDL); err != nil {
			t.Fatal(err)
		}
		old := func(i int) error {
			_, err := first.Exec(`INSERT INTO issues (id, project_id, org, number, title, created_at, updated_at) VALUES ($1, 'p1', 'acme', $2, 't', 1, 1)`,
				fmt.Sprintf("old%d", i), i)
			return err
		}
		for i := 1; i <= 5; i++ {
			if err := old(i); err != nil {
				t.Fatal(err)
			}
		}

		// Two pods migrate at once. On a server each has its own pool on the one database;
		// a SQLite file is one pod's, so its two pods share the handle.
		second := first
		if os.Getenv(locus.Env) != "" {
			second = open(ns, "issues-once")
		}
		var (
			wg     sync.WaitGroup
			stores = make([]*Store, 2)
			errs   = make([]error, 2)
		)
		for i, db := range []*sql.DB{first, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				stores[i], errs[i] = openStore(db)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("pod %d migrating the table: %v", i, err)
			}
		}

		// A pod that has not been replaced inserts without the new column.
		for i := 6; i < 106; i++ {
			if err := old(i); err != nil {
				t.Fatalf("an old pod's insert after the migration: %v", err)
			}
		}

		ctx := t.Context()
		const callers = 16
		var (
			numbers = make([]int, callers)
			filed   = make([]error, callers)
		)
		for i := range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				issue := Issue{ID: fmt.Sprintf("n%d", i), ProjectID: "p1", Org: "acme", Title: "pr", Kind: "pr", Source: "agent", CreatedAt: 2, UpdatedAt: 2}
				got, err := stores[i%2].CreateIssueOnce(ctx, issue, "sub\x1fpull/s\x1freq")
				numbers[i], filed[i] = got.Number, err
			}()
		}
		wg.Wait()
		for i := range callers {
			if filed[i] != nil {
				t.Errorf("caller %d: %v", i, filed[i])
			} else if numbers[i] != numbers[0] {
				t.Errorf("caller %d was given number %d, caller 0 %d", i, numbers[i], numbers[0])
			}
		}
		var rows int
		if err := first.QueryRow(`SELECT COUNT(*) FROM issues WHERE request <> ''`).Scan(&rows); err != nil || rows != 1 {
			t.Errorf("%d rows filed for the request (%v), want 1", rows, err)
		}
		other, err := stores[0].CreateIssueOnce(ctx, Issue{ID: "z1", ProjectID: "p2", Org: "acme", Title: "pr", CreatedAt: 2, UpdatedAt: 2}, "sub\x1fpull/s\x1freq")
		if err != nil || other.Number != 1 {
			t.Errorf("the request in another project filed %+v (%v), want its own row, number 1", other, err)
		}
		if _, err := stores[0].GetIssue(ctx, "acme", "p1", 3); err != nil {
			t.Errorf("a row from before the column: %v", err)
		}
	})
}
