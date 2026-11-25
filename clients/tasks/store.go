// SPDX-License-Identifier: Apache-2.0 OR MIT

package tasks

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zap-proto/zip"
)

// EnqueueOrg submits a durable task in the mounted Cloud Tasks queue for a
// validated org and returns the server-generated task ID.
func EnqueueOrg(ctx context.Context, org, kind string, body json.RawMessage) (string, error) {
	if !label.MatchString(kind) {
		return "", zip.ErrBadRequest("kind must match " + label.String())
	}
	if err := payload("payload", body); err != nil {
		return "", err
	}
	db, err := mountedDB()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().UnixMilli()
	id := newID()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO tasks (org, id, kind, payload, status, attempts, max_attempts, lease, created, updated)
		 VALUES (?, ?, ?, ?, ?, 0, ?, 0, ?, ?)`,
		org, id, kind, string(body), Queued, defaultAttempts, now, now); err != nil {
		return "", zip.ErrInternal("write failed")
	}
	return id, nil
}
