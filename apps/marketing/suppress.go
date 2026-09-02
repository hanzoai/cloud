// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/notify"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// suppress.go is the ONE send client. Every marketing delivery — a campaign blast,
// a drip sequence step, a calendar email — calls state.deliver, and deliver is
// the SINGLE place the per-org suppression (unsubscribe / opt-out) list is
// checked. Because there is exactly one gate, a recipient who opted out can never
// be reached by any marketing path, and adding a new send path cannot bypass the
// check: the only way to send is through deliver. Delivery itself reuses the
// platform notify rail (notify.Send) — marketing never constructs a provider.
//
// Suppression is per-org and per-channel: an address opted out of email in org A
// is still reachable by SMS, and is untouched in org B. Transactional sends (IAM
// OTP) do NOT pass through here, so an opt-out never blocks a security code.

// normAddr canonicalizes a recipient so suppression matching is case- and
// whitespace-insensitive ("A@B.com" suppresses "a@b.com ").
func normAddr(a string) string { return strings.ToLower(strings.TrimSpace(a)) }

// Suppression is one opt-out record: (org, channel, address) is the key. It is
// also the INPUT of add and remove — the same tuple names the record either way.
type Suppression struct {
	Org string `json:"-"`
	// Channel is the surface opted out of: email, sms, social, meta, google or
	// tiktok. Empty means email. Opting out of one leaves the others reachable.
	Channel string `json:"channel"`
	// Address is the recipient, normalized (lower-cased, trimmed) so an opt-out
	// cannot be slipped past on a case or whitespace difference. Required.
	Address string `json:"address"`
	// Reason is a free-text note, capped at 1024 bytes. The public one-click
	// endpoint records "one-click unsubscribe".
	Reason string `json:"reason"`
	// CreatedAt is unix seconds, server-assigned.
	CreatedAt int64 `json:"createdAt"`
}

func (s *Store) migrateSuppressions() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS marketing_suppressions (
  org         TEXT NOT NULL,
  channel     TEXT NOT NULL,
  address     TEXT NOT NULL,
  reason      TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (org, channel, address)
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("marketing migrate suppressions: %w", err)
	}
	return nil
}

// Suppress records an opt-out. Idempotent: re-suppressing the same tuple keeps
// the original record (ON CONFLICT DO NOTHING), so a double-click never errors.
func (s *Store) Suppress(ctx context.Context, sup Suppression) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO marketing_suppressions (org,channel,address,reason,created_at)
		 VALUES (?,?,?,?,?) ON CONFLICT(org,channel,address) DO NOTHING`,
		sup.Org, sup.Channel, normAddr(sup.Address), sup.Reason, sup.CreatedAt)
	if err != nil {
		return fmt.Errorf("suppress: %w", err)
	}
	return nil
}

// Unsuppress removes an opt-out (re-subscribe). Reports whether a row was removed.
func (s *Store) Unsuppress(ctx context.Context, org, channel, address string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM marketing_suppressions WHERE org=? AND channel=? AND address=?`,
		org, channel, normAddr(address))
	if err != nil {
		return false, fmt.Errorf("unsuppress: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Suppressed reports whether (org, channel, address) has opted out. This is the
// predicate the send gate consults before every delivery.
func (s *Store) Suppressed(ctx context.Context, org, channel, address string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM marketing_suppressions WHERE org=? AND channel=? AND address=?`,
		org, channel, normAddr(address)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("suppressed check: %w", err)
	}
	return true, nil
}

// ListSuppressions returns the org's opt-outs, newest first.
func (s *Store) ListSuppressions(ctx context.Context, org string, limit int) ([]Suppression, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org,channel,address,reason,created_at FROM marketing_suppressions
		 WHERE org=? ORDER BY created_at DESC LIMIT ?`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list suppressions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Suppression, 0, 16)
	for rows.Next() {
		var sup Suppression
		if err := rows.Scan(&sup.Org, &sup.Channel, &sup.Address, &sup.Reason, &sup.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan suppression: %w", err)
		}
		out = append(out, sup)
	}
	return out, rows.Err()
}

// ---- the ONE send gate ----

// sendFn is the delivery hand-off past the suppression gate. Production is the
// platform notify rail (notify.Send); tests override it to assert exactly which
// recipients reach the rail. It is the ONLY send out of deliver, so there is
// never a second sender.
var sendFn = notify.Send

// deliver is the single marketing send client. It refuses a suppressed recipient
// (skipped, not an error — a blast over a list simply omits opt-outs) and
// otherwise delivers through the notify rail. sent reports the gate decision so
// callers can record per-recipient outcomes (sent vs skipped).
func (st state) deliver(ctx context.Context, kms cloud.KMSClient, org, channel, address, subject, body string) (sent bool, err error) {
	off, err := st.store.Suppressed(ctx, org, channel, normAddr(address))
	if err != nil {
		return false, err
	}
	if off {
		return false, nil // opted out — the gate skips it
	}
	if _, err := sendFn(ctx, kms, org, channel, "", []string{address}, subject, body); err != nil {
		return false, err
	}
	return true, nil
}

// ---- public unsubscribe token (embeddable in every marketing email) ----

// unsubKeyCache holds the deployment's unsubscribe-HMAC key, loaded once from
// KMS (marketing/unsubscribe-hmac), auto-provisioned on first use. The key only
// authenticates our OWN one-click links (it binds org+channel+address), so a
// single deployment key is sufficient and simplest.
var (
	unsubKeyMu    sync.Mutex
	unsubKeyCache []byte
)

const unsubKeyRef = "marketing/unsubscribe-hmac"

// unsubKey resolves the unsubscribe-HMAC key from KMS, generating and sealing a
// fresh 32-byte key on first use. Fails closed (error) when KMS is absent, so a
// forged link can never verify against an empty key.
func unsubKey(ctx context.Context, kms cloud.KMSClient) ([]byte, error) {
	unsubKeyMu.Lock()
	defer unsubKeyMu.Unlock()
	if len(unsubKeyCache) > 0 {
		return unsubKeyCache, nil
	}
	if kms == nil {
		return nil, errors.New("marketing: KMS unavailable; unsubscribe links disabled")
	}
	if v, err := kms.GetSecret(ctx, unsubKeyRef); err == nil && len(v) >= 32 {
		unsubKeyCache = v
		return v, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := kms.PutSecret(ctx, unsubKeyRef, key); err != nil {
		return nil, fmt.Errorf("marketing: seal unsubscribe key: %w", err)
	}
	unsubKeyCache = key
	return key, nil
}

// unsubToken is the one-click-unsubscribe MAC over (org, channel, address).
func unsubToken(key []byte, org, channel, address string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(org + "\n" + channel + "\n" + normAddr(address)))
	return hex.EncodeToString(m.Sum(nil))
}

// unsubValid verifies a presented token in constant time.
func unsubValid(key []byte, org, channel, address, token string) bool {
	want := unsubToken(key, org, channel, address)
	return hmac.Equal([]byte(want), []byte(token))
}

// unsubURL builds the absolute one-click-unsubscribe link for an email footer,
// or "" when no key/domain is available (the email then simply omits it).
func unsubURL(ctx context.Context, s *cloud.Service[state], org, channel, address string) string {
	key, err := unsubKey(ctx, s.KMS)
	if err != nil || s.Domain == "" {
		return ""
	}
	tok := unsubToken(key, org, channel, address)
	q := url.Values{"org": {org}, "channel": {channel}, "address": {address}, "token": {tok}}
	return "https://" + s.Domain + "/v1/marketing/unsubscribe?" + q.Encode()
}

// ---- handlers ----

// SuppressionList is a page of opt-outs, newest first.
type SuppressionList struct {
	// Data is the page: every (channel, address) this org's send gate refuses,
	// newest opt-out first. Absence from it is not permission to mail someone —
	// it only means no opt-out was recorded on that channel.
	Data []Suppression `json:"data"`
}

// UnsubscribeInput is the signed one-click link's query: the tuple to opt out
// plus the MAC that authorizes exactly that tuple.
type UnsubscribeInput struct {
	// Org is the org the link was minted for.
	Org string `json:"org"`
	// Channel is the surface to opt out of.
	Channel string `json:"channel"`
	// Address is the recipient to opt out.
	Address string `json:"address"`
	// Token is the HMAC over (org, channel, address). It is the ONLY authority
	// here — there is no principal — so it binds the request to one tuple and
	// nothing else.
	Token string `json:"token"`
}

// Unsubscribed confirms a one-click opt-out.
type Unsubscribed struct {
	// Unsubscribed is always true here: the opt-out is idempotent, so a second
	// click on the same link confirms the same thing rather than reporting
	// nothing changed. A refused token never reaches this shape — it is a 403.
	Unsubscribed bool `json:"unsubscribed"`
	// Address is the recipient now opted out, normalized (lower-cased, trimmed)
	// to the form the send gate matches on — so it can differ in case from the
	// address the link carried.
	Address string `json:"address"`
	// Channel is the ONE surface opted out of: email, sms, social, meta, google
	// or tiktok. The other channels are untouched, and so is this address in
	// every other org.
	Channel string `json:"channel"`
}

// listSuppressions returns the org's opt-out list, newest first — everyone the
// send gate will refuse to deliver to.
//
// Example: {"limit": 100}
func (o ops) listSuppressions(ctx context.Context, in *Page) (*SuppressionList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListSuppressions(ctx, org, limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &SuppressionList{Data: rows}, nil
}

// addSuppression records an opt-out for the org (admin / self-service
// management). Address is required; channel defaults to email. It is idempotent:
// re-suppressing the same tuple keeps the original record rather than erroring.
// From here on the ONE send gate refuses that recipient on that channel.
//
// Example: {"channel": "email", "address": "person@example.com", "reason": "asked support to stop"}
func (o ops) addSuppression(ctx context.Context, in *Suppression) (*Suppression, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	channel, okCh := normChannel(in.Channel)
	if !okCh {
		return nil, zip.ErrBadRequest("channel must be one of email, sms, social, meta, google, tiktok")
	}
	addr := normAddr(in.Address)
	if addr == "" {
		return nil, zip.ErrBadRequest("address is required")
	}
	sup := Suppression{Org: org, Channel: channel, Address: addr, Reason: clip(in.Reason), CreatedAt: time.Now().Unix()}
	if err := o.s.State.store.Suppress(ctx, sup); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "suppress: %v", err)
	}
	cloud.Created(ctx)
	return &sup, nil
}

// removeSuppression re-subscribes an address on one channel and answers 204. An
// address that is not on the list reads as not found.
//
// Example: {"channel": "email", "address": "person@example.com"}
func (o ops) removeSuppression(ctx context.Context, in *Suppression) (*cloud.Unit, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	channel, okCh := normChannel(in.Channel)
	if !okCh {
		return nil, zip.ErrBadRequest("unknown channel")
	}
	removed, err := o.s.State.store.Unsuppress(ctx, org, channel, in.Address)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "unsuppress: %v", err)
	}
	if !removed {
		return nil, zip.ErrNotFound("not on the suppression list")
	}
	return nil, nil
}

// unsubscribe is the PUBLIC one-click endpoint (no principal): a recipient
// clicks the signed link in an email footer. The token binds (org, channel,
// address), so a caller can only opt OUT exactly the tuple it was minted for —
// never another address and never another org. An invalid token is refused, and
// a deployment with no KMS-sealed key refuses rather than accepting anything.
//
// Example: {"org": "acme", "channel": "email", "address": "person@example.com", "token": "9f2a…"}
// Response: {"unsubscribed": true, "address": "person@example.com", "channel": "email"}
func (o ops) unsubscribe(ctx context.Context, in *UnsubscribeInput) (*Unsubscribed, error) {
	org := strings.TrimSpace(in.Org)
	channel := strings.ToLower(strings.TrimSpace(in.Channel))
	address := normAddr(in.Address)
	token := strings.TrimSpace(in.Token)
	if org == "" || channel == "" || address == "" || token == "" {
		return nil, zip.ErrBadRequest("org, channel, address and token are required")
	}
	key, err := unsubKey(ctx, o.s.KMS)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "unsubscribe unavailable: %v", err)
	}
	if !unsubValid(key, org, channel, address, token) {
		return nil, zip.ErrForbidden("invalid unsubscribe token")
	}
	if err := o.s.State.store.Suppress(ctx, Suppression{
		Org: org, Channel: channel, Address: address, Reason: "one-click unsubscribe", CreatedAt: time.Now().Unix(),
	}); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "suppress: %v", err)
	}
	return &Unsubscribed{Unsubscribed: true, Address: address, Channel: channel}, nil
}
