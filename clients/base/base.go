// Package base is the dev edition's document store: dynamic collections of JSON
// records over one embedded SQLite file. It exists so an application running on
// a laptop has somewhere to put its data without standing up a database and
// without declaring a schema first. A record is whatever JSON the caller sends,
// and a collection comes into being the moment something is written to it — so
// there is nothing to migrate, because there is nothing to migrate FROM.
//
// The store is one table keyed (org, collection, id). The org is the tenant
// boundary and it comes from the validated caller, never from the body, so two
// tenants sharing the one file can no more read each other's records than if
// they held separate files: every statement in this package filters on it.
package base

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// MaxDoc bounds one stored document. A document store with no ceiling is a way
// to fill a laptop's disk with a single request, and a record that large is a
// blob wearing a record's clothes — it belongs in object storage.
const MaxDoc = 256 << 10

// maxLimit and defaultLimit bound a page. A caller that names no limit gets a
// screenful; one that asks for everything gets a page, because the honest
// answer to "give me all of it" is "here is the first page and an offset".
const (
	defaultLimit = 50
	maxLimit     = 500
)

// name is what may become a collection: a short lowercase slug. A collection
// name is a storage key the caller chooses, so it is CHECKED against a pattern
// rather than escaped on the way in — anything that is not plainly a name is
// either a mistake or an attempt at something, and both get the same answer.
var name = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// schema is the whole store. One table, org first, so the primary key doubles
// as the index every read already wants: an org's collection is a contiguous
// prefix scan, and a record is a point lookup.
const schema = `
CREATE TABLE IF NOT EXISTS records (
  org        TEXT    NOT NULL,
  collection TEXT    NOT NULL,
  id         TEXT    NOT NULL,
  doc        TEXT    NOT NULL,
  created    INTEGER NOT NULL,
  updated    INTEGER NOT NULL,
  PRIMARY KEY (org, collection, id)
)`

// state is base's own data; shared deps live in the embedded cloud.Base.
type state struct{ db *sql.DB }

// mounted is the open store, kept for Shutdown. build is its only writer.
var mounted *sql.DB

// Mount wires the document store onto app.
func Mount(app *zip.App, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "base", build, routes)
}

// build opens the store and creates the table. It fails closed: a subsystem
// that cannot reach its data must not answer requests as if it could.
func build(b cloud.Base) (state, error) {
	if b.DataDir == "" {
		return state{}, errors.New("empty DataDir")
	}
	db, err := cek.Open(filepath.Join(b.DataDir, "base.db"))
	if err != nil {
		return state{}, err
	}
	// SQLite admits one writer at a time, and the dev edition may open this file
	// with no busy timeout at all (cek's unencrypted path is a bare sql.Open).
	// One connection turns that contention into a queue instead of an error.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		_ = db.Close()
		return state{}, fmt.Errorf("create schema: %w", err)
	}
	mounted = db
	return state{db: db}, nil
}

// Shutdown closes the store. Idempotent.
func Shutdown(context.Context) error {
	if mounted == nil {
		return nil
	}
	err := mounted.Close()
	mounted = nil
	return err
}

// routes is the ONE registration point. Static paths precede their :param
// siblings so the first-match scan resolves them first.
func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1/base")
	zip.Get[CollectionsIn, CollectionsOut](g, "/collections", collections(s),
		zip.WithOperationID("base_collections"),
		zip.WithSummary("List the collections that hold records"),
		zip.WithTags("base"))
	zip.Get[ListIn, ListOut](g, "/collections/:collection", list(s),
		zip.WithOperationID("base_list"),
		zip.WithSummary("List the records in one collection"),
		zip.WithTags("base"))
	zip.Post[CreateIn, Record](g, "/collections/:collection", create(s),
		zip.WithOperationID("base_create"),
		zip.WithSummary("Create a record"),
		zip.WithTags("base"),
		zip.WithStatus(201))
	zip.Get[GetIn, Record](g, "/collections/:collection/:id", get(s),
		zip.WithOperationID("base_get"),
		zip.WithSummary("Read one record"),
		zip.WithTags("base"))
	zip.Put[UpdateIn, Record](g, "/collections/:collection/:id", update(s),
		zip.WithOperationID("base_update"),
		zip.WithSummary("Replace one record"),
		zip.WithTags("base"),
	)
	zip.Delete[DeleteIn, DeleteOut](g, "/collections/:collection/:id", remove(s),
		zip.WithOperationID("base_delete"),
		zip.WithSummary("Delete one record"),
		zip.WithTags("base"))
}

// Record is one stored document. The document itself is opaque to this package
// — callers put shapes in and get the same bytes back — so the only structure
// here is the addressing (collection + id) and when it last changed.
type Record struct {
	// Collection the record belongs to.
	Collection string `json:"collection"`
	// ID is the server-generated identity of the record, stable for its life.
	ID string `json:"id"`
	// Doc is the record itself: any JSON value, returned exactly as stored.
	Doc json.RawMessage `json:"doc"`
	// Created is when the record was first written.
	Created time.Time `json:"created"`
	// Updated is when the record last changed; equal to Created until it does.
	Updated time.Time `json:"updated"`
}

// CollectionsIn takes nothing: a caller may only ever see its own collections,
// so there is nothing left to say.
type CollectionsIn struct{}

// CollectionsOut is the caller's collections and how much is in each.
type CollectionsOut struct {
	// Collections holds one entry per collection that currently has records.
	Collections []Collection `json:"collections"`
}

// Collection is one collection name and its size. There is no collection
// object to create or configure — a collection is exactly the records in it.
type Collection struct {
	// Name of the collection.
	Name string `json:"name"`
	// Records currently stored in it.
	Records int64 `json:"records"`
}

// collections lists the caller's collections with their record counts.
func collections(s *cloud.Service[state]) zip.TypedHandler[CollectionsIn, CollectionsOut] {
	return func(ctx context.Context, _ *CollectionsIn) (*CollectionsOut, error) {
		rows, err := s.State.db.QueryContext(ctx,
			`SELECT collection, COUNT(*) FROM records WHERE org = ? GROUP BY collection ORDER BY collection`,
			principal.Tenant(ctx))
		if err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		defer func() { _ = rows.Close() }()
		out := CollectionsOut{Collections: []Collection{}}
		for rows.Next() {
			var c Collection
			if err := rows.Scan(&c.Name, &c.Records); err != nil {
				return nil, zip.ErrInternal("read failed")
			}
			out.Collections = append(out.Collections, c)
		}
		if rows.Err() != nil {
			return nil, zip.ErrInternal("read failed")
		}
		return &out, nil
	}
}

// ListIn pages one collection.
type ListIn struct {
	// Collection to read.
	Collection string `json:"collection"`
	// Limit caps the page; 0 means the default of 50, and 500 is the ceiling.
	Limit int `json:"limit"`
	// Offset skips this many records, counting from the oldest.
	Offset int `json:"offset"`
}

// ListOut is one page of records, oldest first.
type ListOut struct {
	// Records in this page, ordered by creation and then by id. Records written
	// in the same millisecond therefore come back in a stable but arbitrary
	// order relative to each other, which is all paging needs: the order is a
	// total one, so consecutive offsets tile the collection without gaps or
	// repeats. Empty for a collection that holds none — an unknown collection is
	// not an error, because a collection is only ever its records.
	Records []Record `json:"records"`
}

// list returns a page of a collection, oldest first so an offset stays stable
// as new records arrive at the end.
func list(s *cloud.Service[state]) zip.TypedHandler[ListIn, ListOut] {
	return func(ctx context.Context, in *ListIn) (*ListOut, error) {
		if !name.MatchString(in.Collection) {
			return nil, zip.ErrBadRequest("collection must match " + name.String())
		}
		limit := in.Limit
		switch {
		case limit <= 0:
			limit = defaultLimit
		case limit > maxLimit:
			limit = maxLimit
		}
		offset := in.Offset
		if offset < 0 {
			offset = 0
		}
		rows, err := s.State.db.QueryContext(ctx,
			`SELECT id, doc, created, updated FROM records
			  WHERE org = ? AND collection = ? ORDER BY created, id LIMIT ? OFFSET ?`,
			principal.Tenant(ctx), in.Collection, limit, offset)
		if err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		defer func() { _ = rows.Close() }()
		out := ListOut{Records: []Record{}}
		for rows.Next() {
			r := Record{Collection: in.Collection}
			var doc []byte
			var created, updated int64
			if err := rows.Scan(&r.ID, &doc, &created, &updated); err != nil {
				return nil, zip.ErrInternal("read failed")
			}
			r.Doc, r.Created, r.Updated = json.RawMessage(doc), at(created), at(updated)
			out.Records = append(out.Records, r)
		}
		if rows.Err() != nil {
			return nil, zip.ErrInternal("read failed")
		}
		return &out, nil
	}
}

// CreateIn writes a new record into a collection.
type CreateIn struct {
	// Collection to write into. It is created implicitly by the first record.
	Collection string `json:"collection"`
	// Doc is the record: any JSON value, up to 256 KiB.
	Doc json.RawMessage `json:"doc"`
}

// create stores a document under a fresh server-generated id.
func create(s *cloud.Service[state]) zip.TypedHandler[CreateIn, Record] {
	return func(ctx context.Context, in *CreateIn) (*Record, error) {
		if err := check(in.Collection, in.Doc); err != nil {
			return nil, err
		}
		// The returned stamps are rendered from the value that is STORED, so a
		// caller comparing this record with the one a later read returns finds
		// them equal instead of a millisecond apart.
		ms := time.Now().UTC().UnixMilli()
		r := Record{
			Collection: in.Collection,
			ID:         newID(),
			Doc:        in.Doc,
			Created:    at(ms),
			Updated:    at(ms),
		}
		if _, err := s.State.db.ExecContext(ctx,
			`INSERT INTO records (org, collection, id, doc, created, updated) VALUES (?, ?, ?, ?, ?, ?)`,
			principal.Tenant(ctx), r.Collection, r.ID, string(in.Doc), ms, ms); err != nil {
			return nil, zip.ErrInternal("write failed")
		}
		return &r, nil
	}
}

// GetIn addresses one record.
type GetIn struct {
	// Collection holding the record.
	Collection string `json:"collection"`
	// ID of the record.
	ID string `json:"id"`
}

// get returns one record, or 404.
func get(s *cloud.Service[state]) zip.TypedHandler[GetIn, Record] {
	return func(ctx context.Context, in *GetIn) (*Record, error) {
		if !name.MatchString(in.Collection) {
			return nil, zip.ErrBadRequest("collection must match " + name.String())
		}
		return one(ctx, s.State.db, principal.Tenant(ctx), in.Collection, in.ID)
	}
}

// UpdateIn replaces one record's document.
type UpdateIn struct {
	// Collection holding the record.
	Collection string `json:"collection"`
	// ID of the record.
	ID string `json:"id"`
	// Doc replaces the stored document wholesale. There is no partial update:
	// a caller that read a record and sends it back changed is describing the
	// record it wants, which is a fact this store can act on without guessing.
	Doc json.RawMessage `json:"doc"`
}

// update replaces a record's document, or 404 when there is nothing to replace.
func update(s *cloud.Service[state]) zip.TypedHandler[UpdateIn, Record] {
	return func(ctx context.Context, in *UpdateIn) (*Record, error) {
		if err := check(in.Collection, in.Doc); err != nil {
			return nil, err
		}
		org := principal.Tenant(ctx)
		res, err := s.State.db.ExecContext(ctx,
			`UPDATE records SET doc = ?, updated = ? WHERE org = ? AND collection = ? AND id = ?`,
			string(in.Doc), time.Now().UTC().UnixMilli(), org, in.Collection, in.ID)
		if err != nil {
			return nil, zip.ErrInternal("write failed")
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return nil, zip.ErrNotFound("no such record")
		}
		return one(ctx, s.State.db, org, in.Collection, in.ID)
	}
}

// DeleteIn addresses the record to remove. A DELETE carries no body: the URL
// says what goes.
type DeleteIn struct {
	// Collection holding the record.
	Collection string `json:"collection"`
	// ID of the record.
	ID string `json:"id"`
}

// DeleteOut echoes what went, so a caller reading a transcript of tool calls
// can see WHICH record a delete removed rather than only that one did.
type DeleteOut struct {
	// Collection the record was in.
	Collection string `json:"collection"`
	// ID of the removed record.
	ID string `json:"id"`
}

// remove deletes one record, or 404 when it was already gone.
func remove(s *cloud.Service[state]) zip.TypedHandler[DeleteIn, DeleteOut] {
	return func(ctx context.Context, in *DeleteIn) (*DeleteOut, error) {
		if !name.MatchString(in.Collection) {
			return nil, zip.ErrBadRequest("collection must match " + name.String())
		}
		res, err := s.State.db.ExecContext(ctx,
			`DELETE FROM records WHERE org = ? AND collection = ? AND id = ?`,
			principal.Tenant(ctx), in.Collection, in.ID)
		if err != nil {
			return nil, zip.ErrInternal("write failed")
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return nil, zip.ErrNotFound("no such record")
		}
		return &DeleteOut{Collection: in.Collection, ID: in.ID}, nil
	}
}

// one reads a single record. It is the read behind get and behind the read-back
// of an update, so a caller is always shown the row the store actually holds.
func one(ctx context.Context, db *sql.DB, org, collection, id string) (*Record, error) {
	r := Record{Collection: collection, ID: id}
	var doc []byte
	var created, updated int64
	err := db.QueryRowContext(ctx,
		`SELECT doc, created, updated FROM records WHERE org = ? AND collection = ? AND id = ?`,
		org, collection, id).Scan(&doc, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, zip.ErrNotFound("no such record")
	}
	if err != nil {
		return nil, zip.ErrInternal("read failed")
	}
	r.Doc, r.Created, r.Updated = json.RawMessage(doc), at(created), at(updated)
	return &r, nil
}

// check refuses a write this store will not accept: an ill-formed collection
// name, a document that is not JSON, or one that is too big. All three are the
// caller's mistake, so all three are 400.
func check(collection string, doc json.RawMessage) error {
	if !name.MatchString(collection) {
		return zip.ErrBadRequest("collection must match " + name.String())
	}
	if len(doc) == 0 || !json.Valid(doc) {
		return zip.ErrBadRequest("doc must be a JSON value")
	}
	if len(doc) > MaxDoc {
		return zip.ErrBadRequest(fmt.Sprintf("doc is %d bytes, over the %d-byte limit", len(doc), MaxDoc))
	}
	return nil
}

// at renders a stored millisecond stamp. Time is stored as an integer because
// that is what it is compared and ordered by, and rendered as a timestamp
// because that is what a reader wants to see.
func at(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// newID mints a record id: 128 random bits, hex. Ids are server-generated so a
// caller can neither choose one nor guess a record it was never shown.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read is documented never to fail
	return hex.EncodeToString(b[:])
}
