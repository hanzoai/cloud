// SPDX-License-Identifier: Apache-2.0 OR MIT

package base

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zap-proto/zip"
)

// CollectionsOrg lists collections and record counts for a validated org in the
// mounted Cloud Base store.
func CollectionsOrg(ctx context.Context, org string) ([]Collection, error) {
	db, err := mountedDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT collection, COUNT(*) FROM records WHERE org = ? GROUP BY collection ORDER BY collection`,
		org)
	if err != nil {
		return nil, zip.ErrInternal("read failed")
	}
	defer func() { _ = rows.Close() }()
	out := []Collection{}
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.Name, &c.Records); err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		out = append(out, c)
	}
	if rows.Err() != nil {
		return nil, zip.ErrInternal("read failed")
	}
	return out, nil
}

// ListOrg pages one collection for a validated org in the mounted Cloud Base store.
func ListOrg(ctx context.Context, org, collection string, limit, offset int) ([]Record, error) {
	if !name.MatchString(collection) {
		return nil, zip.ErrBadRequest("collection must match " + name.String())
	}
	db, err := mountedDB()
	if err != nil {
		return nil, err
	}
	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, doc, created, updated FROM records
		  WHERE org = ? AND collection = ? ORDER BY created, id LIMIT ? OFFSET ?`,
		org, collection, limit, offset)
	if err != nil {
		return nil, zip.ErrInternal("read failed")
	}
	defer func() { _ = rows.Close() }()
	out := []Record{}
	for rows.Next() {
		r := Record{Collection: collection}
		var doc []byte
		var created, updated int64
		if err := rows.Scan(&r.ID, &doc, &created, &updated); err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		r.Doc, r.Created, r.Updated = json.RawMessage(doc), at(created), at(updated)
		out = append(out, r)
	}
	if rows.Err() != nil {
		return nil, zip.ErrInternal("read failed")
	}
	return out, nil
}

// GetOrg reads a single record for a validated org in the mounted Cloud Base store.
func GetOrg(ctx context.Context, org, collection, id string) (*Record, error) {
	if !name.MatchString(collection) {
		return nil, zip.ErrBadRequest("collection must match " + name.String())
	}
	db, err := mountedDB()
	if err != nil {
		return nil, err
	}
	return one(ctx, db, org, collection, id)
}

// CreateOrg writes a new record for a validated org into the mounted Cloud Base store.
func CreateOrg(ctx context.Context, org, collection string, doc json.RawMessage) (*Record, error) {
	if err := check(collection, doc); err != nil {
		return nil, err
	}
	db, err := mountedDB()
	if err != nil {
		return nil, err
	}
	ms := time.Now().UTC().UnixMilli()
	r := Record{
		Collection: collection,
		ID:         newID(),
		Doc:        doc,
		Created:    at(ms),
		Updated:    at(ms),
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO records (org, collection, id, doc, created, updated) VALUES (?, ?, ?, ?, ?, ?)`,
		org, r.Collection, r.ID, string(doc), ms, ms); err != nil {
		return nil, zip.ErrInternal("write failed")
	}
	return &r, nil
}

// UpdateOrg replaces a record for a validated org in the mounted Cloud Base store.
func UpdateOrg(ctx context.Context, org, collection, id string, doc json.RawMessage) (*Record, error) {
	if err := check(collection, doc); err != nil {
		return nil, err
	}
	db, err := mountedDB()
	if err != nil {
		return nil, err
	}
	res, err := db.ExecContext(ctx,
		`UPDATE records SET doc = ?, updated = ? WHERE org = ? AND collection = ? AND id = ?`,
		string(doc), time.Now().UTC().UnixMilli(), org, collection, id)
	if err != nil {
		return nil, zip.ErrInternal("write failed")
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return nil, zip.ErrNotFound("no such record")
	}
	return one(ctx, db, org, collection, id)
}
