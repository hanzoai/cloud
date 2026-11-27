package email

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/search"
	"github.com/hanzoai/dbx"
)

// The ledger is read at millions of rows, so every read it makes is held to its
// query plan: each one SEARCHES an index, none SCANS a table. The plans are
// SQLite's own, for the SQL Base builds from the very filter and sort the ledger
// sends over base_list, against the schema it grows, over tables filled and
// ANALYZEd so the planner weighs them as it would a full ledger.

// planned is a Base grown by the ledger's schema, its two big tables filled.
func planned(t *testing.T) *core.BaseApp {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir(), EncryptionEnv: "hz_plan_test"})
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	var docs []map[string]any
	if err := json.Unmarshal([]byte(schema), &docs); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := core.GrowCollections(app, docs); err != nil {
		t.Fatalf("grow: %v", err)
	}
	statuses := []string{"sent", "delivered", "opened", "clicked", "bounced"}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err := app.RunInTransaction(func(tx core.App) error {
		for i := 0; i < 20000; i++ {
			at := stamp(start.Add(time.Duration(i) * time.Minute))
			if _, err := tx.DB().NewQuery(`INSERT INTO email_messages (id, message, status, provider, recipient, at)
				VALUES ({:id}, {:m}, {:s}, {:p}, {:r}, {:at})`).Bind(dbx.Params{
				"id": rid("message", fmt.Sprint(i)), "m": fmt.Sprintf("msg_%024x", i), "s": statuses[i%len(statuses)],
				"p": []string{"ses", "sendgrid"}[i%2], "r": digest(fmt.Sprintf("p%d@acme.io", i%5000)), "at": at,
			}).Execute(); err != nil {
				return err
			}
			for _, span := range []string{spanHour, spanDay} {
				campaign := ""
				if i%3 == 0 {
					campaign = fmt.Sprintf("c%d", i%40)
				}
				if _, err := tx.DB().NewQuery(`INSERT OR IGNORE INTO email_tallies (id, span, at, provider, campaign, sent)
					VALUES ({:id}, {:span}, {:at}, {:p}, {:c}, 1)`).Bind(dbx.Params{
					"id": rid("tally", fmt.Sprint(span, i)), "span": span, "at": at, "p": "ses", "c": campaign,
				}).Execute(); err != nil {
					return err
				}
			}
		}
		_, err := tx.DB().NewQuery("ANALYZE").Execute()
		return err
	})
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	return app
}

// plan is SQLite's plan for the SQL Base builds from a base_list of collection
// with filter and sort, one step a line.
func plan(t *testing.T, app *core.BaseApp, collection, filter, sort string) string {
	t.Helper()
	c, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		t.Fatal(err)
	}
	resolver := core.NewRecordFieldResolver(app, c, nil, true)
	q := app.RecordQuery(c)
	if filter != "" {
		expr, err := search.FilterData(filter).BuildExpr(resolver)
		if err != nil {
			t.Fatalf("filter %q: %v", filter, err)
		}
		q.AndWhere(expr)
	}
	for _, f := range search.ParseSortFromString(sort) {
		if f.Name == "" {
			continue
		}
		expr, err := f.BuildExpr(resolver)
		if err != nil {
			t.Fatal(err)
		}
		q.AndOrderBy(expr)
	}
	if err := resolver.UpdateQuery(q); err != nil {
		t.Fatal(err)
	}
	built := q.Limit(50).Build()
	var steps []struct {
		ID     int    `db:"id"`
		Parent int    `db:"parent"`
		Unused int    `db:"notused"`
		Detail string `db:"detail"`
	}
	if err := app.DB().NewQuery("EXPLAIN QUERY PLAN " + built.SQL()).Bind(built.Params()).All(&steps); err != nil {
		t.Fatalf("explain %q: %v", built.SQL(), err)
	}
	var lines []string
	for _, s := range steps {
		lines = append(lines, s.Detail)
	}
	return strings.Join(lines, "\n")
}

func TestPlans_EveryLedgerReadSearchesAnIndex(t *testing.T) {
	app := planned(t)
	at := stamp(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC))
	cases := []struct {
		name, collection, filter, sort, index string
	}{
		{"a page, newest first", messages, ``, "-at,-id", "idx_email_messages_at"},
		{"the page after", messages, `at <= "` + at + `" && (at < "` + at + `" || id < "abc")`, "-at,-id", "idx_email_messages_at"},
		{"by status", messages, `status = "opened"`, "-at,-id", "idx_email_messages_status"},
		{"by provider over a range", messages, `provider = "ses" && at >= "` + at + `"`, "-at,-id", "idx_email_messages_provider"},
		{"by recipient", messages, `recipient = "` + digest("p7@acme.io") + `"`, "-at,-id", "idx_email_messages_recipient"},
		{"a message by its id", messages, `id = "` + rid("message", "7") + `" || id = "` + rid("message", "8") + `"`, "", "sqlite_autoindex_email_messages_1"},
		{"all mail over a range", tallies, `span = "day" && at >= "` + at + `" && at < "2026-02-01 00:00:00.000Z" && campaign = ""`, "at,id", "idx_email_tallies"},
		{"one campaign over a range", tallies, `span = "hour" && at >= "` + at + `" && campaign = "c3"`, "at,id", "idx_email_tallies_campaign"},
		{"every campaign over a range", tallies, `span = "day" && at >= "` + at + `" && at < "2026-02-01 00:00:00.000Z" && campaign != ""`, "at,id", "idx_email_tallies"},
		{"one provider's last day", tallies, `span = "hour" && campaign = "" && provider = "ses" && at >= "` + at + `"`, "at,id", "idx_email_tallies"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := plan(t, app, c.collection, c.filter, c.sort)
			t.Logf("plan:\n%s", p)
			for _, line := range strings.Split(p, "\n") {
				if strings.HasPrefix(line, "SCAN ") && !strings.Contains(line, "USING ") {
					t.Errorf("a full scan:\n%s", p)
				}
			}
			if !strings.Contains(p, c.index) {
				t.Errorf("not on %s:\n%s", c.index, p)
			}
		})
	}
}
