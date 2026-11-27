// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/types"
	"github.com/zap-proto/zip"
)

func statusOf(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func raws(t *testing.T, records ...map[string]any) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(records))
	for i, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = b
	}
	return out
}

const activitiesDoc = `[{
	"name": "activities", "type": "base",
	"fields": [
		{"name": "source_id", "type": "text"},
		{"name": "subject", "type": "text"},
		{"name": "secret", "type": "text", "hidden": true},
		{"name": "createdBy", "type": "actor", "onCreate": true},
		{"name": "updatedBy", "type": "actor", "onCreate": true, "onUpdate": true}
	],
	"indexes": ["CREATE UNIQUE INDEX idx_activities_source ON activities (source_id) WHERE source_id != ''"]
}]`

// The ops act for the caller's org and nobody else's: no org is refused, the
// embed off is unavailable rather than empty.
func TestPlaneNeedsAnOrgAndAnEmbed(t *testing.T) {
	prev := mounted
	mounted = nil
	t.Cleanup(func() { mounted = prev })

	in := &client.BaseImportIn{Collections: json.RawMessage(activitiesDoc)}
	if _, err := planeImport(context.Background(), in); statusOf(err) != http.StatusForbidden {
		t.Fatalf("no org: want 403, got %v", err)
	}
	if _, err := planeImport(plane.For(context.Background(), "acme"), in); statusOf(err) != http.StatusServiceUnavailable {
		t.Fatalf("no embed: want 503, got %v", err)
	}
}

func TestPlaneImportGrowsAndRefusesBasesOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		ctx := plane.For(context.Background(), "acme")

		got, err := planeImport(ctx, &client.BaseImportIn{Collections: json.RawMessage(activitiesDoc)})
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if strings.Join(got.Created, ",") != "activities" {
			t.Fatalf("created %v, want [activities]", got.Created)
		}

		// Again: nothing to do, and nothing an org added is lost.
		app, _ := mounted.pool.appFor("acme")
		c, _ := app.FindCollectionByNameOrId("activities")
		c.Fields.Add(&core.TextField{Name: "mine"})
		if err := app.Save(c); err != nil {
			t.Fatal(err)
		}
		again, err := planeImport(ctx, &client.BaseImportIn{Collections: json.RawMessage(activitiesDoc)})
		if err != nil {
			t.Fatalf("import again: %v", err)
		}
		if len(again.Created) != 0 || len(again.Grown) != 0 {
			t.Fatalf("a second import changed something: %+v", again)
		}
		c, _ = app.FindCollectionByNameOrId("activities")
		if c.Fields.GetByName("mine") == nil {
			t.Fatal("an import dropped a field the org added")
		}

		// The CRM module is a name.
		crm, err := planeImport(ctx, &client.BaseImportIn{Module: "crm"})
		if err != nil {
			t.Fatalf("import crm: %v", err)
		}
		for _, want := range []string{"people", "companies", "opportunities", "views", "segments", "list_members"} {
			if !strings.Contains(","+strings.Join(crm.Created, ",")+",", ","+want+",") {
				t.Errorf("the crm module did not create %q (created %v)", want, crm.Created)
			}
		}

		for name, in := range map[string]*client.BaseImportIn{
			"both":       {Module: "crm", Collections: json.RawMessage(activitiesDoc)},
			"neither":    {},
			"underscore": {Collections: json.RawMessage(`[{"name":"_mine","type":"base","fields":[]}]`)},
			"auth":       {Collections: json.RawMessage(`[{"name":"members","type":"auth","fields":[]}]`)},
			"view":       {Collections: json.RawMessage(`[{"name":"v","type":"view","viewQuery":"select id from users"}]`)},
			"system":     {Collections: json.RawMessage(`[{"name":"sys","type":"base","system":true,"fields":[]}]`)},
			"no module":  {Module: "nope"},
		} {
			if _, err := planeImport(ctx, in); err == nil {
				t.Errorf("%s: the import was accepted", name)
			}
		}
	})
}

func TestPlaneUpsertIsIdempotentByKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		ctx := plane.For(context.Background(), "acme")
		if _, err := planeImport(ctx, &client.BaseImportIn{Collections: json.RawMessage(activitiesDoc)}); err != nil {
			t.Fatal(err)
		}

		first, err := planeUpsert(ctx, &client.BaseUpsertIn{
			Collection: "activities", Key: "source_id", Source: "email",
			Records: raws(t, map[string]any{"source_id": "msg-1", "subject": "hello"}),
		})
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if len(first.Records) != 1 || !first.Records[0].Created {
			t.Fatalf("first write: %+v", first)
		}

		again, err := planeUpsert(ctx, &client.BaseUpsertIn{
			Collection: "activities", Key: "source_id", Source: "email",
			Records: raws(t, map[string]any{"source_id": "msg-1", "subject": "hello again"}),
		})
		if err != nil {
			t.Fatalf("upsert again: %v", err)
		}
		if again.Records[0].Created || again.Records[0].ID != first.Records[0].ID {
			t.Fatalf("a replay made a second record: %+v vs %+v", again, first)
		}

		app, _ := mounted.pool.appFor("acme")
		rec, err := app.FindRecordById("activities", first.Records[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.GetString("subject") != "hello again" {
			t.Fatalf("the replay did not update: %q", rec.GetString("subject"))
		}
		if a := rec.Get("createdBy").(types.Actor); a.Source != "email" {
			t.Fatalf("the writer was not stamped with its source: %+v", a)
		}

		// By id, too.
		byID, err := planeUpsert(ctx, &client.BaseUpsertIn{
			Collection: "activities",
			Records:    raws(t, map[string]any{"id": first.Records[0].ID, "subject": "by id"}),
		})
		if err != nil || byID.Records[0].Created {
			t.Fatalf("upsert by id: %+v %v", byID, err)
		}

		// One transaction: a bad record leaves the good one unwritten.
		_, err = planeUpsert(ctx, &client.BaseUpsertIn{
			Collection: "activities",
			Records:    raws(t, map[string]any{"subject": "good"}, map[string]any{"source_id": "msg-1", "subject": "duplicate key"}),
		})
		if err == nil {
			t.Fatal("a batch with a duplicate unique value was accepted")
		}
		if n, _ := app.CountRecords("activities", nil); n != 1 {
			t.Fatalf("a failed batch left %d records, want 1", n)
		}

		for name, in := range map[string]*client.BaseUpsertIn{
			"no records":     {Collection: "activities"},
			"bases own":      {Collection: "_superusers", Records: raws(t, map[string]any{"email": "x@y.z"})},
			"a person":       {Collection: "activities", Source: "manual", Records: raws(t, map[string]any{"subject": "x"})},
			"unknown source": {Collection: "activities", Source: "carrier-pigeon", Records: raws(t, map[string]any{"subject": "x"})},
			"unknown key":    {Collection: "activities", Key: "nope", Records: raws(t, map[string]any{"subject": "x"})},
			"no collection":  {Collection: "ghosts", Records: raws(t, map[string]any{"subject": "x"})},
		} {
			if _, err := planeUpsert(ctx, in); err == nil {
				t.Errorf("%s: the upsert was accepted", name)
			}
		}
	})
}

func TestPlaneReadsAreTheOrgsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		acme := plane.For(context.Background(), "acme")
		zoo := plane.For(context.Background(), "zoo")
		for _, ctx := range []context.Context{acme, zoo} {
			if _, err := planeImport(ctx, &client.BaseImportIn{Collections: json.RawMessage(activitiesDoc)}); err != nil {
				t.Fatal(err)
			}
		}
		w, err := planeUpsert(acme, &client.BaseUpsertIn{
			Collection: "activities",
			Records: raws(t,
				map[string]any{"subject": "b", "secret": "s1"},
				map[string]any{"subject": "a", "secret": "s2"},
				map[string]any{"subject": "c", "secret": "s3"},
			),
		})
		if err != nil {
			t.Fatal(err)
		}

		got, err := planeGet(acme, &client.BaseGetIn{Collection: "activities", ID: w.Records[0].ID})
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !strings.Contains(string(got.Record), `"secret":"s1"`) {
			t.Fatalf("a program reading for the org sees every field: %s", got.Record)
		}
		if _, err := planeGet(zoo, &client.BaseGetIn{Collection: "activities", ID: w.Records[0].ID}); statusOf(err) != http.StatusNotFound {
			t.Fatalf("another org read acme's record: %v", err)
		}

		page, err := planeList(acme, &client.BaseListIn{Collection: "activities", Filter: `subject != "c"`, Sort: "subject", PerPage: 1})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if page.TotalItems != 2 || len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), `"subject":"a"`) {
			t.Fatalf("list: total %d, items %s", page.TotalItems, page.Items)
		}

		where, err := planeList(acme, &client.BaseListIn{
			Collection: "activities",
			Where:      json.RawMessage(`{"or":[{"field":"subject","op":"is","value":"a"},{"field":"subject","op":"is","value":"c"}]}`),
			Sort:       "-subject",
		})
		if err != nil {
			t.Fatalf("list where: %v", err)
		}
		if where.TotalItems != 2 || !strings.Contains(string(where.Items[0]), `"subject":"c"`) {
			t.Fatalf("list where: total %d, items %s", where.TotalItems, where.Items)
		}

		other, err := planeList(zoo, &client.BaseListIn{Collection: "activities"})
		if err != nil {
			t.Fatal(err)
		}
		if other.TotalItems != 0 {
			t.Fatalf("zoo's list holds %d of acme's records", other.TotalItems)
		}

		if _, err := planeList(acme, &client.BaseListIn{Collection: "_superusers"}); statusOf(err) != http.StatusForbidden {
			t.Fatalf("a list of Base's own: %v", err)
		}
		// an expand reaches no further than a read: not into an auth collection
		app, _ := mounted.pool.appFor("acme")
		act, _ := app.FindCollectionByNameOrId("activities")
		users, _ := app.FindCollectionByNameOrId("users")
		act.Fields.Add(&core.RelationField{Name: "who", CollectionId: users.Id, MaxSelect: 1})
		if err := app.Save(act); err != nil {
			t.Fatal(err)
		}
		if _, err := planeGet(acme, &client.BaseGetIn{Collection: "activities", ID: w.Records[0].ID, Expand: "who"}); statusOf(err) != http.StatusBadRequest {
			t.Fatalf("an expand into the users auth collection: %v", err)
		}

		// an auth collection's records are credentials
		if _, err := planeList(acme, &client.BaseListIn{Collection: "users"}); statusOf(err) != http.StatusForbidden {
			t.Fatalf("a list of the users auth collection: %v", err)
		}
		if _, err := planeImport(acme, &client.BaseImportIn{Collections: json.RawMessage(`[{"name":"users","fields":[{"name":"x","type":"text"}]}]`)}); statusOf(err) != http.StatusForbidden {
			t.Fatalf("an import over the users auth collection: %v", err)
		}
	})
}

// The CRM install an entitlement runs (POST /v1/base/modules/crm, base_import with
// a module) builds its collections, indexes and relations on whichever engine the
// Base is on, and a second install changes nothing.
func TestPlaneInstallsTheCRMOnEitherEngine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		ctx := plane.For(context.Background(), "acme")

		first, err := planeImport(ctx, &client.BaseImportIn{Module: "crm"})
		if err != nil {
			t.Fatalf("install the CRM: %v", err)
		}
		if len(first.Created) == 0 {
			t.Fatalf("the CRM install created nothing: %+v", first)
		}
		again, err := planeImport(ctx, &client.BaseImportIn{Module: "crm"})
		if err != nil {
			t.Fatalf("install the CRM again: %v", err)
		}
		if len(again.Created) != 0 {
			t.Fatalf("a second install created %v", again.Created)
		}

		w, err := planeUpsert(ctx, &client.BaseUpsertIn{
			Collection: "people",
			Records:    raws(t, map[string]any{"id": "alice0000000001", "name": "Alice", "emails": []string{"a@example.com"}}),
		})
		if err != nil {
			t.Fatalf("file a person: %v", err)
		}
		if len(w.Records) != 1 {
			t.Fatalf("filed %d people, want 1", len(w.Records))
		}
		got, err := planeList(ctx, &client.BaseListIn{Collection: "people"})
		if err != nil || len(got.Items) != 1 {
			t.Fatalf("list the people: %v %+v", err, got)
		}
	})
}
