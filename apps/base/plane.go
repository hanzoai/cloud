// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

// plane.go — the one door another app has into an org's Base.
//
// Every app runs as its own process, and only this one opens Bases, so a sibling
// that writes an org's records cannot reach the engine in memory: it used to call
// EnsureSpace in-process, which read this package's mounted state in a process
// where it was never set, so every project's data space waited forever. The
// sibling asks here instead, on the plane, and this process stays the single
// writer of every Base it opens.
//
// THE ORG IS THE CALLER'S. Each op reads it from cloud.Who and refuses an empty
// one; no input names an org, so no caller can write into another's Base.
//
// The ops act with the org's own authority over its Base, as an in-process write
// does: collection rules are a person's limits on a request, and these are a
// first-party program writing the org's data. What they may touch is still
// narrowed — never a collection Base keeps for itself, never a schema type but an
// ordinary base collection — and every write names who made it, so an actor
// field and a collection's history say "application", not nobody.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	baseapp "github.com/hanzoai/base"
	"github.com/hanzoai/base/apis"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/modules"
	"github.com/hanzoai/base/tools/search"
	"github.com/hanzoai/base/tools/types"
	"github.com/zap-proto/zip"
)

// maxUpsert is the most records one upsert writes.
const maxUpsert = 500

// exposePlane publishes the ops. Use calls it whatever the embed says, so the
// plane's document names them; with the embed off each one answers 503.
func exposePlane() { PlaneOps(cloud.Plane()) }

// PlaneOps registers the ops on r: the plane, in production; a peer of its own, in
// a test of another app that writes an org's Base (internal/basetest).
func PlaneOps(r *zip.App) {
	r.Post("/base/import", planeImport,
		zip.WithOperationID(client.BaseImport),
		zip.WithSummary("Grow the caller's org Base with a module or with collections"))
	r.Post("/base/upsert", planeUpsert,
		zip.WithOperationID(client.BaseUpsert),
		zip.WithSummary("Create or update records in the caller's org Base"))
	r.Post("/base/get", planeGet,
		zip.WithOperationID(client.BaseGet),
		zip.WithSummary("Read one record of the caller's org Base"))
	r.Post("/base/list", planeList,
		zip.WithOperationID(client.BaseList),
		zip.WithSummary("Read a page of a collection of the caller's org Base"))
	r.Post("/base/delete", planeDelete,
		zip.WithOperationID(client.BaseDelete),
		zip.WithSummary("Delete records from the caller's org Base"))
}

// Removes records from one collection of the caller's org Base, by id, all in one
// transaction or none. An id that names no record is not an error: deleting what
// is already gone changes nothing. The collection's relations cascade as Base's
// own delete does, and every delete is stamped with the source and the caller.
func planeDelete(ctx context.Context, in *client.BaseDeleteIn) (*client.BaseDeleted, error) {
	if len(in.IDs) == 0 {
		return nil, zip.ErrBadRequest("base_delete: no ids")
	}
	if len(in.IDs) > maxUpsert {
		return nil, zip.ErrBadRequest(fmt.Sprintf("base_delete: at most %d ids a call", maxUpsert))
	}
	actor, err := actorOf(ctx, in.Source)
	if err != nil {
		return nil, err
	}
	out := &client.BaseDeleted{}
	err = withOrgBase(ctx, func(app *baseapp.Base) error {
		c, err := writable(app, in.Collection)
		if err != nil {
			return err
		}
		writeCtx := core.WithActor(ctx, actor)
		return app.RunInTransaction(func(tx core.App) error {
			for _, id := range in.IDs {
				r, err := tx.FindRecordById(c, strings.TrimSpace(id))
				if err != nil {
					if isNotFound(err) {
						continue
					}
					return err
				}
				if err := tx.DeleteWithContext(writeCtx, r); err != nil {
					return zip.ErrBadRequest(fmt.Sprintf("base_delete: %q: %v", id, err))
				}
				out.Deleted++
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// withOrgBase runs fn against the caller's org Base, pinned for the call so the
// pool never closes it underneath.
func withOrgBase(ctx context.Context, fn func(app *baseapp.Base) error) error {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return zip.ErrForbidden("base: no org on the call")
	}
	s := mounted
	if s == nil || s.pool == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "base: hosted Bases are not embedded in this deployment")
	}
	app, release, err := s.pool.pin(org)
	if err != nil {
		return fmt.Errorf("base: open the org's Base: %w", err)
	}
	defer release()
	return fn(app)
}

// writable finds a collection a sibling may write: an ordinary base collection,
// never one Base keeps for itself.
func writable(app core.App, name string) (*core.Collection, error) {
	c, err := readable(app, name)
	if err != nil {
		return nil, err
	}
	if !c.IsBase() {
		return nil, zip.ErrForbidden(fmt.Sprintf("base: %q is not a collection records are written to", c.Name))
	}
	return c, nil
}

// readable finds a collection a sibling may read: an org's base or view
// collection, never an auth collection (its records are credentials) and never
// a table Base keeps for itself.
func readable(app core.App, name string) (*core.Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, zip.ErrBadRequest("base: a collection is required")
	}
	c, err := app.FindCachedCollectionByNameOrId(name)
	if err != nil {
		return nil, zip.ErrNotFound(fmt.Sprintf("base: no collection %q", name))
	}
	if c.System || strings.HasPrefix(c.Name, "_") || c.IsAuth() {
		return nil, zip.ErrForbidden(fmt.Sprintf("base: %q is Base's own", c.Name))
	}
	return c, nil
}

// actorOf is who a plane write names: the source the caller stated, and the
// person or program the call acts for.
func actorOf(ctx context.Context, source string) (types.Actor, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = core.ActorApplication
	}
	if source == core.ActorManual || !slices.Contains(core.ActorSources, source) {
		return types.Actor{}, zip.ErrBadRequest(fmt.Sprintf("base: %q is not a source a program writes as", source))
	}
	who := cloud.Who(ctx)
	name := who.Name
	if name == "" {
		name = who.Email
	}
	if name == "" {
		name = source
	}
	return types.Actor{Source: source, Id: who.User, Name: name}, nil
}

// Grows the caller's org Base by a module Base ships or by collections the calling
// app owns. What is missing is created; nothing that exists is dropped or changed,
// so a field an org added by hand survives every import after it. Only ordinary
// base collections are accepted.
func planeImport(ctx context.Context, in *client.BaseImportIn) (*client.BaseGrowth, error) {
	module := strings.TrimSpace(in.Module)
	if (module == "") == (len(in.Collections) == 0) {
		return nil, zip.ErrBadRequest("base_import: name a module or send collections, one of the two")
	}

	var docs []map[string]any
	var names []string
	if module == "" {
		if err := json.Unmarshal(in.Collections, &docs); err != nil {
			return nil, zip.ErrBadRequest("base_import: collections must be a JSON array of collections: " + err.Error())
		}
		for _, d := range docs {
			name, _ := d["name"].(string)
			typ, _ := d["type"].(string)
			if (typ != "" && typ != core.CollectionTypeBase) || strings.HasPrefix(name, "_") {
				return nil, zip.ErrForbidden(fmt.Sprintf("base_import: %q is not an ordinary base collection", name))
			}
			if sys, _ := d["system"].(bool); sys {
				return nil, zip.ErrForbidden(fmt.Sprintf("base_import: %q asks to be a system collection", name))
			}
			names = append(names, name)
		}
	} else if m, ok := modules.Get(module); !ok {
		return nil, zip.ErrNotFound(fmt.Sprintf("base_import: Base ships no module %q", module))
	} else {
		names = m.Names()
	}

	var growth *core.Growth
	err := withOrgBase(ctx, func(app *baseapp.Base) error {
		// ONE GROWTH AT A TIME PER BASE. Growing reads what exists and creates what
		// does not, so two imports at once (two replicas, or a burst of intake forms
		// installing the CRM) both read a collection as missing and both create it,
		// and PostgreSQL refuses the second on _collections_pkey. A transaction-scoped
		// advisory lock on the Base's own schema makes the read-and-create one step
		// on every replica; SQLite's single writer already did.
		return app.RunInTransaction(func(tx core.App) error {
			if tx.Dialect().Name() == "postgres" {
				if _, err := tx.DB().NewQuery("SELECT pg_advisory_xact_lock(hashtext(current_schema() || ':base:grow'))").Execute(); err != nil {
					return err
				}
			}
			// what exists is judged as it is, too: an import never reaches a
			// collection the org itself could not change
			for _, name := range names {
				if c, err := tx.FindCollectionByNameOrId(name); err == nil && !apis.SchemaManageable(c) {
					return zip.ErrForbidden(fmt.Sprintf("base_import: %q is not an ordinary base collection", c.Name))
				}
			}
			var err error
			if module != "" {
				growth, err = modules.Install(tx, module)
			} else {
				growth, err = core.GrowCollections(tx, docs)
			}
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	return growthOf(growth), nil
}

func growthOf(g *core.Growth) *client.BaseGrowth {
	out := &client.BaseGrowth{Created: g.Created, Grown: namesOf(g.Grown), Kept: namesOf(g.Kept)}
	if out.Created == nil {
		out.Created = []string{}
	}
	return out
}

func namesOf(m map[string][]string) []client.BaseNames {
	out := make([]client.BaseNames, 0, len(m))
	for c, names := range m {
		out = append(out, client.BaseNames{Collection: c, Names: names})
	}
	slices.SortFunc(out, func(a, b client.BaseNames) int { return strings.Compare(a.Collection, b.Collection) })
	return out
}

// Writes records into one collection of the caller's org Base, all in one
// transaction or none: a record carrying an id, or a value of the key field
// that matches an existing record, updates that record with the fields it names;
// any other is created. Writing the same record twice under a key is therefore
// one record. Every write is stamped with the source and the caller.
func planeUpsert(ctx context.Context, in *client.BaseUpsertIn) (*client.BaseUpserted, error) {
	if len(in.Records) == 0 {
		return nil, zip.ErrBadRequest("base_upsert: no records")
	}
	if len(in.Records) > maxUpsert {
		return nil, zip.ErrBadRequest(fmt.Sprintf("base_upsert: at most %d records a call", maxUpsert))
	}
	actor, err := actorOf(ctx, in.Source)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.Key)

	out := &client.BaseUpserted{Records: make([]client.BaseWritten, 0, len(in.Records))}
	err = withOrgBase(ctx, func(app *baseapp.Base) error {
		c, err := writable(app, in.Collection)
		if err != nil {
			return err
		}
		if key != "" && key != core.FieldNameId && c.Fields.GetByName(key) == nil {
			return zip.ErrBadRequest(fmt.Sprintf("base_upsert: %q has no field %q", c.Name, key))
		}

		writeCtx := core.WithActor(ctx, actor)
		return app.RunInTransaction(func(tx core.App) error {
			for i, raw := range in.Records {
				var data map[string]any
				if err := json.Unmarshal(raw, &data); err != nil || data == nil {
					return zip.ErrBadRequest(fmt.Sprintf("base_upsert: record %d is not a JSON object", i))
				}
				record, created, err := matchOrNew(tx, c, key, data)
				if err != nil {
					return err
				}
				for k, v := range data {
					if k == core.FieldNameId && !created {
						continue
					}
					record.Set(k, v)
				}
				if err := tx.SaveWithContext(writeCtx, record); err != nil {
					return zip.ErrBadRequest(fmt.Sprintf("base_upsert: record %d: %v", i, err))
				}
				out.Records = append(out.Records, client.BaseWritten{ID: record.Id, Created: created})
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// matchOrNew finds the record data names, by id or by key, or starts a new one.
func matchOrNew(app core.App, c *core.Collection, key string, data map[string]any) (*core.Record, bool, error) {
	if id, _ := data[core.FieldNameId].(string); id != "" {
		if r, err := app.FindRecordById(c, id); err == nil {
			return r, false, nil
		}
		return core.NewRecord(c), true, nil
	}
	if key != "" && key != core.FieldNameId {
		if v, ok := data[key]; ok && v != nil && v != "" {
			r, err := app.FindFirstRecordByData(c, key, v)
			if err == nil {
				return r, false, nil
			}
			if !isNotFound(err) {
				return nil, false, err
			}
		}
	}
	return core.NewRecord(c), true, nil
}

// Reads one record of the caller's org Base, every field included, with the
// relations Expand names expanded.
func planeGet(ctx context.Context, in *client.BaseGetIn) (*client.BaseRecord, error) {
	var out *client.BaseRecord
	err := withOrgBase(ctx, func(app *baseapp.Base) error {
		c, err := readable(app, in.Collection)
		if err != nil {
			return err
		}
		r, err := app.FindRecordById(c, strings.TrimSpace(in.ID))
		if err != nil {
			return zip.ErrNotFound(fmt.Sprintf("base_get: no record %q in %q", in.ID, c.Name))
		}
		if err := expand(app, []*core.Record{r}, in.Expand); err != nil {
			return err
		}
		raw, err := exportRecord(r)
		if err != nil {
			return err
		}
		out = &client.BaseRecord{Record: raw}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Reads a page of one collection of the caller's org Base: Filter in Base's
// filter language and Where as a structured filter, both applied, sorted and
// paged as the records API does, every field included.
func planeList(ctx context.Context, in *client.BaseListIn) (*client.BaseRecords, error) {
	var out *client.BaseRecords
	err := withOrgBase(ctx, func(app *baseapp.Base) error {
		c, err := readable(app, in.Collection)
		if err != nil {
			return err
		}

		resolver := core.NewRecordFieldResolver(app, c, nil, true)
		provider := search.NewProvider(resolver).Query(app.RecordQuery(c))
		if f := strings.TrimSpace(in.Filter); f != "" {
			provider.AddFilter(search.FilterData(f))
		}
		if len(in.Where) > 0 && string(in.Where) != "null" {
			where, params, err := core.CompileWhere(app, c, nil, in.Where, time.Now())
			if err != nil {
				return zip.ErrBadRequest("base_list: where: " + err.Error())
			}
			expr, err := where.BuildExpr(resolver, params)
			if err != nil {
				return zip.ErrBadRequest("base_list: where: " + err.Error())
			}
			provider.Query(app.RecordQuery(c).AndWhere(expr))
		}

		q := url.Values{}
		if in.Sort != "" {
			q.Set(search.SortQueryParam, in.Sort)
		}
		if in.Page > 0 {
			q.Set(search.PageQueryParam, strconv.Itoa(in.Page))
		}
		if in.PerPage > 0 {
			q.Set(search.PerPageQueryParam, strconv.Itoa(in.PerPage))
		}
		if in.SkipTotal {
			q.Set(search.SkipTotalQueryParam, "true")
		}

		records := []*core.Record{}
		result, err := provider.ParseAndExec(q.Encode(), &records)
		if err != nil {
			return zip.ErrBadRequest("base_list: " + err.Error())
		}
		if err := expand(app, records, in.Expand); err != nil {
			return err
		}

		out = &client.BaseRecords{
			Items:      make([]json.RawMessage, len(records)),
			Page:       result.Page,
			PerPage:    result.PerPage,
			TotalItems: result.TotalItems,
			TotalPages: result.TotalPages,
		}
		for i, r := range records {
			if out.Items[i], err = exportRecord(r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// expand expands the comma-separated relations of names on records.
func expand(app core.App, records []*core.Record, names string) error {
	if strings.TrimSpace(names) == "" {
		return nil
	}
	var list []string
	for _, n := range strings.Split(names, ",") {
		if n = strings.TrimSpace(n); n != "" {
			list = append(list, n)
		}
	}
	// an expand reaches what a read may: never an auth collection's records or
	// a table Base keeps for itself, whatever the relation points at
	fetch := func(c *core.Collection, ids []string) ([]*core.Record, error) {
		if c.System || strings.HasPrefix(c.Name, "_") || c.IsAuth() {
			return nil, fmt.Errorf("%q is Base's own", c.Name)
		}
		return app.FindRecordsByIds(c.Id, ids)
	}
	if failed := app.ExpandRecords(records, list, fetch); len(failed) > 0 {
		for name, err := range failed {
			return zip.ErrBadRequest(fmt.Sprintf("base: expand %q: %v", name, err))
		}
	}
	return nil
}

// exportRecord is every field of r, the hidden ones included: the caller is a
// program acting for the org, not a page rendering it.
func exportRecord(r *core.Record) (json.RawMessage, error) {
	return json.Marshal(r.Clone().Unhide(r.Collection().Fields.FieldNames()...))
}

func isNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
