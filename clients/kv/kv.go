// SPDX-License-Identifier: Apache-2.0

// Package kv provides the local Key-Value store over embedded SQLite.
package kv

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const schema = `
CREATE TABLE IF NOT EXISTS kv_entries (
  org       TEXT    NOT NULL,
  bucket    TEXT    NOT NULL,
  key       TEXT    NOT NULL,
  value     TEXT    NOT NULL,
  revision  INTEGER NOT NULL,
  created   INTEGER NOT NULL,
  updated   INTEGER NOT NULL,
  PRIMARY KEY (org, bucket, key)
);
CREATE TABLE IF NOT EXISTS kv_history (
  org       TEXT    NOT NULL,
  bucket    TEXT    NOT NULL,
  key       TEXT    NOT NULL,
  revision  INTEGER NOT NULL,
  value     TEXT    NOT NULL,
  created   INTEGER NOT NULL,
  PRIMARY KEY (org, bucket, key, revision)
);
`

type state struct{ db *sql.DB }

var mounted *sql.DB

// Mount registers the KV subsystem.
func Mount(app *zip.App, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "kv", build, routes)
}

func build(b cloud.Base) (state, error) {
	if b.DataDir == "" {
		return state{}, errors.New("empty DataDir")
	}
	db, err := cek.Open(filepath.Join(b.DataDir, "kv.db"))
	if err != nil {
		return state{}, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		_ = db.Close()
		return state{}, fmt.Errorf("create kv schema: %w", err)
	}
	mounted = db
	return state{db: db}, nil
}

// Shutdown closes the database handle.
func Shutdown(context.Context) error {
	if mounted == nil {
		return nil
	}
	err := mounted.Close()
	mounted = nil
	return err
}

func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1/kv")

	// Plain /v1/kv list
	g.Get("", func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		rows, err := s.State.db.QueryContext(c.Context(),
			`SELECT bucket, key, value, revision, created, updated FROM kv_entries WHERE org = ? ORDER BY bucket, key LIMIT 100`, org)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "read failed"})
		}
		defer rows.Close()

		var items []map[string]any
		for rows.Next() {
			var bucket, key, val string
			var rev, cr, up int64
			if err := rows.Scan(&bucket, &key, &val, &rev, &cr, &up); err == nil {
				var raw json.RawMessage
				var parsed any = val
				if json.Unmarshal([]byte(val), &raw) == nil {
					parsed = raw
				}
				items = append(items, map[string]any{
					"bucket":   bucket,
					"key":      key,
					"value":    parsed,
					"revision": rev,
					"created":  time.UnixMilli(cr).UTC(),
					"updated":  time.UnixMilli(up).UTC(),
				})
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		return c.JSON(http.StatusOK, map[string]any{"keys": items})
	})

	// Single key /v1/kv/:key
	g.Get("/:key", func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		k := c.Param("key")
		var val string
		var rev, cr, up int64
		err := s.State.db.QueryRowContext(c.Context(),
			`SELECT value, revision, created, updated FROM kv_entries WHERE org = ? AND bucket = 'default' AND key = ?`, org, k).
			Scan(&val, &rev, &cr, &up)
		if err == sql.ErrNoRows {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "key not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "read failed"})
		}
		var parsed any = val
		var raw json.RawMessage
		if json.Unmarshal([]byte(val), &raw) == nil {
			parsed = raw
		}
		return c.JSON(http.StatusOK, map[string]any{
			"bucket":   "default",
			"key":      k,
			"value":    parsed,
			"revision": rev,
			"created":  time.UnixMilli(cr).UTC(),
			"updated":  time.UnixMilli(up).UTC(),
		})
	})

	g.Put("/:key", func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		k := c.Param("key")
		body := c.Body()
		if len(body) == 0 {
			body = []byte(`""`)
		}
		var req struct {
			Value any `json:"value"`
		}
		var valStr string
		if json.Unmarshal(body, &req) == nil && req.Value != nil {
			if b, err := json.Marshal(req.Value); err == nil {
				valStr = string(b)
			} else {
				valStr = string(body)
			}
		} else {
			valStr = string(body)
		}

		now := time.Now().UnixMilli()
		var rev int64 = 1

		// Check existing revision
		_ = s.State.db.QueryRowContext(c.Context(),
			`SELECT revision + 1 FROM kv_entries WHERE org = ? AND bucket = 'default' AND key = ?`, org, k).Scan(&rev)

		_, err := s.State.db.ExecContext(c.Context(), `
			INSERT INTO kv_entries (org, bucket, key, value, revision, created, updated)
			VALUES (?, 'default', ?, ?, ?, ?, ?)
			ON CONFLICT(org, bucket, key) DO UPDATE SET
				value = excluded.value,
				revision = excluded.revision,
				updated = excluded.updated
		`, org, k, valStr, rev, now, now)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "write failed"})
		}

		_, _ = s.State.db.ExecContext(c.Context(), `
			INSERT INTO kv_history (org, bucket, key, revision, value, created)
			VALUES (?, 'default', ?, ?, ?, ?)
		`, org, k, rev, valStr, now)

		return c.JSON(http.StatusOK, map[string]any{
			"bucket":   "default",
			"key":      k,
			"revision": rev,
			"updated":  time.UnixMilli(now).UTC(),
		})
	})

	g.Delete("/:key", func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		k := c.Param("key")
		res, err := s.State.db.ExecContext(c.Context(),
			`DELETE FROM kv_entries WHERE org = ? AND bucket = 'default' AND key = ?`, org, k)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "delete failed"})
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "key not found"})
		}
		c.Status(http.StatusNoContent)
		return nil
	})
}
