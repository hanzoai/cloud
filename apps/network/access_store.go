package network

// access_store.go — where invites and the waitlist are kept: two collections of the
// brand org's own Base.
//
// Only the base app opens a Base, so this writes over the plane (base_import,
// base_upsert, base_get, base_list) acting for the brand org, exactly as the CRM
// does (apps/crm base.go) and the base process stays the single writer. A record
// survives a restart and reads the same from every replica.
//
//	network_invites   one record per invite code: the HMAC of the code, never the code
//	network_waitlist  one record per address or handle that asked to be told
//
// IDS ARE DERIVED. An invite's id is the first 15 hex characters of its code's
// HMAC, a waitlist entry's the first 15 of the SHA-256 of its kind and its
// identifier (the shape Base takes), so a code is found by one read with no index and
// a repeat ask is the same record.
//
// Neither collection carries an API rule, so nothing reaches them but the plane.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/hanzoai/cloud/client"
	basepeer "github.com/hanzoai/cloud/client/base"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// The collections, by name.
const (
	invitesName  = "network_invites"
	waitlistName = "network_waitlist"
)

// baseDate is how Base spells a date.
const baseDate = "2006-01-02 15:04:05.000Z"

// accessSchema is the two collections in the shape Base's import takes.
const accessSchema = `[
 {"name": "network_invites", "type": "base", "fields": [
  {"name": "code_hmac", "type": "text"},
  {"name": "status", "type": "text"},
  {"name": "note", "type": "text"},
  {"name": "created_by", "type": "text"},
  {"name": "issued_at", "type": "date"},
  {"name": "expires_at", "type": "date"},
  {"name": "revoked_at", "type": "date"},
  {"name": "revoked_by", "type": "text"}],
  "indexes": ["CREATE INDEX idx_network_invites_issued ON network_invites (issued_at, id)"]},
 {"name": "network_waitlist", "type": "base", "fields": [
  {"name": "identifier", "type": "text"},
  {"name": "kind", "type": "text"},
  {"name": "role", "type": "text"},
  {"name": "source", "type": "text"},
  {"name": "locale", "type": "text"},
  {"name": "created_at", "type": "date"}],
  "indexes": ["CREATE INDEX idx_network_waitlist_created ON network_waitlist (created_at, id)"]}
]`

// grow grows org's Base by the two collections once per process: what is missing is
// created and nothing that exists is changed, so it is safe on every first use. The
// state remembers which orgs it has grown.
func (o ops) grow(ctx context.Context, org string) error {
	grown := o.s.State.grown
	if _, ok := grown.Load(org); ok {
		return nil
	}
	fctx, done := brandContext(ctx, org)
	defer done()
	if _, err := basepeer.BaseImport(plane.For(fctx, org), &api.BaseImportIn{Collections: json.RawMessage(accessSchema)}); err != nil {
		return fmt.Errorf("network: grow the org's Base: %w", err)
	}
	grown.Store(org, true)
	return nil
}

// brandContext is the brand's own context, bounded by the request: cancelled when
// the caller returns or the request ends, whichever is first. Every Base call acts
// for the brand on it, and never for the org a request names: a plane call prefers
// the request's own caller to one stated over it, so this one starts from none.
func brandContext(ctx context.Context, org string) (context.Context, context.CancelFunc) {
	fctx, cancel := context.WithTimeout(plane.For(context.Background(), org), planeBudget)
	stop := context.AfterFunc(ctx, cancel)
	return fctx, func() { stop(); cancel() }
}

// planeBudget bounds one call to the Base.
const planeBudget = 10 * time.Second

// stamp is a time as Base stores a date, and unstamp reads one, the zero time for none.
func stamp(t time.Time) string { return t.UTC().Format(baseDate) }

func unstamp(s string) time.Time {
	t, _ := time.Parse(baseDate, s)
	return t
}

// none reports whether err is Base's answer for a record that is not there. A
// collection that is not there is another 404 and is a fault, not an absence.
func none(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound && strings.Contains(he.Error(), "no record")
}

// Invite statuses as stored. An invite past its expiry is still "active" in the
// store and is read as expired.
const (
	statusActive  = "active"
	statusRevoked = "revoked"
)

// inviteRow is one invite as the Base holds it.
type inviteRow struct {
	ID        string `json:"id"`
	CodeHMAC  string `json:"code_hmac"`
	Status    string `json:"status"`
	Note      string `json:"note"`
	CreatedBy string `json:"created_by"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
	RevokedAt string `json:"revoked_at"`
	RevokedBy string `json:"revoked_by"`
}

// readInvite is the invite with this id, or nil when there is none.
func readInvite(ctx context.Context, org, id string) (*inviteRow, error) {
	fctx, done := brandContext(ctx, org)
	defer done()
	out, err := basepeer.BaseGet(plane.For(fctx, org), &api.BaseGetIn{Collection: invitesName, ID: id})
	if err != nil {
		if none(err) {
			return nil, nil
		}
		return nil, err
	}
	var r inviteRow
	if err := json.Unmarshal(out.Record, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// readInvites is the page-th page of invites, limit to a page, newest first, and
// whether another page follows it.
func readInvites(ctx context.Context, org string, limit, page int) ([]inviteRow, bool, error) {
	fctx, done := brandContext(ctx, org)
	defer done()
	got, err := basepeer.BaseList(plane.For(fctx, org), &api.BaseListIn{
		Collection: invitesName, Sort: "-issued_at,-id", Page: page, PerPage: limit,
	})
	if err != nil {
		return nil, false, err
	}
	rows := make([]inviteRow, 0, len(got.Items))
	for _, raw := range got.Items {
		var r inviteRow
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, false, err
		}
		rows = append(rows, r)
	}
	return rows, got.Page < got.TotalPages, nil
}

// waitRow is one waitlist entry as the Base holds it.
type waitRow struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Kind       string `json:"kind"`
	Role       string `json:"role"`
	Source     string `json:"source"`
	Locale     string `json:"locale"`
	CreatedAt  string `json:"created_at"`
}

// readWait is the waitlist entry with this id, or nil when there is none.
func readWait(ctx context.Context, org, id string) (*waitRow, error) {
	fctx, done := brandContext(ctx, org)
	defer done()
	out, err := basepeer.BaseGet(plane.For(fctx, org), &api.BaseGetIn{Collection: waitlistName, ID: id})
	if err != nil {
		if none(err) {
			return nil, nil
		}
		return nil, err
	}
	var r waitRow
	if err := json.Unmarshal(out.Record, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// writeInvites writes invites, by their ids, in one call.
func writeInvites(ctx context.Context, org string, rows []inviteRow) error {
	all := make([]any, len(rows))
	for i, r := range rows {
		all[i] = r
	}
	return upsert(ctx, org, invitesName, all...)
}

// writeWait writes one entry.
func writeWait(ctx context.Context, org string, row waitRow) error {
	return upsert(ctx, org, waitlistName, row)
}

// upsert writes records into one collection, by their ids, all in one call.
func upsert(ctx context.Context, org, collection string, rows ...any) error {
	fctx, done := brandContext(ctx, org)
	defer done()
	in := &api.BaseUpsertIn{Collection: collection, Source: "application"}
	for _, r := range rows {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		in.Records = append(in.Records, raw)
	}
	_, err := basepeer.BaseUpsert(plane.For(fctx, org), in)
	return err
}
