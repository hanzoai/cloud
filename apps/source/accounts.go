package source

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

const (
	maxAccounts = 50
	maxLabel    = 64
	maxValue    = 4096
	kmsEnv      = "default"
	kmsWait     = 10 * time.Second
)

var errAccountCap = errors.New("source: too many provider accounts")

// A key account is a reference and nothing else: the row holds where KMS keeps the
// value and a fingerprint, never the value. The fingerprint is the first 8 hex digits
// of sha256 of the value, so a person can tell two keys apart without the key.

type account struct {
	ID          string
	Subject     string
	Provider    string
	Label       string
	KeyRef      string
	Fingerprint string
	CreatedAt   int64
}

func (s *Store) putAccount(ctx context.Context, owner string, a account) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_accounts WHERE subject=$1`, a.Subject).Scan(&n); err != nil {
		return fmt.Errorf("count: %w", err)
	}
	if n >= maxAccounts {
		return errAccountCap
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO provider_accounts (owner, subject, id, provider, label, key_ref, fingerprint, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		owner, a.Subject, a.ID, a.Provider, a.Label, a.KeyRef, a.Fingerprint, a.CreatedAt); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	return tx.Commit()
}

func (s *Store) accounts(ctx context.Context, subject string) ([]account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, subject, provider, label, key_ref, fingerprint, created_at FROM provider_accounts WHERE subject=$1 ORDER BY created_at, id`, subject)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}
	defer rows.Close()
	var out []account
	for rows.Next() {
		var a account
		if err := rows.Scan(&a.ID, &a.Subject, &a.Provider, &a.Label, &a.KeyRef, &a.Fingerprint, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("accounts: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) account(ctx context.Context, subject, id string) (account, error) {
	var a account
	err := s.db.QueryRowContext(ctx,
		`SELECT id, subject, provider, label, key_ref, fingerprint, created_at FROM provider_accounts WHERE subject=$1 AND id=$2`, subject, id).
		Scan(&a.ID, &a.Subject, &a.Provider, &a.Label, &a.KeyRef, &a.Fingerprint, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return account{}, errNotFound
	}
	if err != nil {
		return account{}, fmt.Errorf("account: %w", err)
	}
	return a, nil
}

func (s *Store) dropAccount(ctx context.Context, subject, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM provider_accounts WHERE subject=$1 AND id=$2`, subject, id)
	return err
}

// keyPath is where KMS keeps an account's value: the org, the person (by a digest
// of their subject, which may hold characters a path cannot), the provider and the
// account id. The one function both the seal and the resolve go through, so they
// cannot name two places.
func keyPath(org, subject, provider, id string) (string, error) {
	slug := namespace.Sanitize(org)
	if slug == "" {
		return "", fmt.Errorf("source: %q is not an org a key can be kept for", org)
	}
	h := sha256.Sum256([]byte(subject))
	return "orgs/" + slug + "/users/" + hex.EncodeToString(h[:16]) + "/providers/" + provider + "/" + id, nil
}

func kmsCtx(ctx context.Context, org string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(plane.For(ctx, org), kmsWait)
}

// Account is one provider account: a key the person sealed into KMS, or a login a
// source reported. No field carries a value.
type Account struct {
	// ID is the account id. A login's is `<source id>:<instance>`.
	ID string `json:"id"`
	// Provider is the provider's catalog name, for example `anthropic`.
	Provider string `json:"provider"`
	// Kind is `key` or `login`.
	Kind string `json:"kind"`
	// Label is the name the person gave a key, or the instance of a login.
	Label string `json:"label"`
	// Fingerprint is the first 8 hex digits of sha256 of a key's value. Keys only.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Created is when the key was sealed, or the login's source was linked, RFC 3339 UTC.
	Created string `json:"created"`
	// Classes are where it may run: `key`, `desktop` and `local`, from the provider
	// policy. A browser source's logins are `local` only.
	Classes []string `json:"classes"`
	// SourceID is the source that reported a login. Logins only.
	SourceID string `json:"sourceId,omitempty"`
	// Instance is the login's instance id on that source. Logins only.
	Instance string `json:"instance,omitempty"`
	// State is a login's `ready`, `needs_login` or `limited`. Logins only.
	State string `json:"state,omitempty"`
	// Plan is a login's plan label. Logins only.
	Plan string `json:"plan,omitempty"`
	// Shared is whether the person offered this login to their other surfaces. Logins only.
	Shared *bool `json:"shared,omitempty"`
}

// AccountList is the caller's provider accounts.
type AccountList struct {
	// Accounts are the caller's keys, then the logins their sources report.
	Accounts []Account `json:"accounts"`
}

// AccountNew is a key to keep.
type AccountNew struct {
	// Provider is the provider the key is for, for example `anthropic`. It must be one
	// that takes a key.
	Provider string `json:"provider"`
	// Kind is `key`. A login cannot be created here: it appears when a source reports it.
	Kind string `json:"kind"`
	// Label names the key, up to 64 characters. Required.
	Label string `json:"label"`
	// Value is the key. It is sealed into KMS and appears in no response, log or audit.
	Value string `json:"value"`
}

// AccountRef addresses one provider account.
type AccountRef struct {
	// ID is the account, from the path.
	ID string `json:"id"`
}

// AccountDeleted acknowledges a delete.
type AccountDeleted struct {
	// ID is the account that was removed.
	ID string `json:"id"`
	// Deleted is true.
	Deleted bool `json:"deleted"`
}

func (o ops) accountView(a account) Account {
	return Account{ID: a.ID, Provider: a.Provider, Kind: "key", Label: a.Label, Fingerprint: a.Fingerprint,
		Created: stamp(a.CreatedAt), Classes: classes(a.Provider, "key", "")}
}

// ListAccounts returns the signed-in person's own provider accounts with the route
// class each may run under: the keys they sealed into KMS, and the logins their
// linked sources report. A key shows a label and fingerprint, never its value. A
// login is read-only here and disappears when its source stops reporting it.
func (o ops) accounts(ctx context.Context, _ *cloud.Unit) (*AccountList, error) {
	sto, _, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := sto.accounts(ctx, subject)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	srcs, err := sto.List(ctx, subject)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	out := &AccountList{Accounts: []Account{}}
	for _, k := range keys {
		out.Accounts = append(out.Accounts, o.accountView(k))
	}
	for _, r := range srcs {
		for _, p := range r.Providers {
			if p.Kind != "login" {
				continue
			}
			sh := p.Shared
			out.Accounts = append(out.Accounts, Account{ID: r.ID + ":" + p.Instance, Provider: p.Provider, Kind: "login",
				Label: p.Instance, Created: stamp(r.LinkedAt), Classes: classes(p.Provider, "login", r.Kind),
				SourceID: r.ID, Instance: p.Instance, State: p.State, Plan: p.Plan, Shared: &sh})
		}
	}
	return out, nil
}

// CreateAccount seals a provider API key into KMS and records a reference to it.
// KMS keeps the value at kms://orgs/<org>/users/<person>/providers/<provider>/<id>;
// the row keeps that path, the label and a fingerprint, and nothing else. The value
// is in no response, log, trace or audit field, and not even the owner can read it
// back: the gateway resolves it at call time. Only `kind: "key"` is accepted, for a
// provider that takes a key.
//
// Example: {"provider": "anthropic", "kind": "key", "label": "work", "value": "sk-ant-…"}
func (o ops) createAccount(ctx context.Context, in *AccountNew) (*Account, error) {
	sto, org, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	if in.Kind != "key" {
		return nil, zip.ErrBadRequest("only a key can be created here: a login appears when a source reports it")
	}
	if !keyProviders[in.Provider] {
		return nil, zip.ErrBadRequest("this provider takes no API key")
	}
	label := strings.TrimSpace(in.Label)
	if label == "" || len(label) > maxLabel {
		return nil, zip.ErrBadRequest("label is required, up to 64 characters")
	}
	if in.Value == "" || len(in.Value) > maxValue || strings.IndexFunc(in.Value, unicode.IsControl) >= 0 {
		return nil, zip.ErrBadRequest("value is required, up to 4096 characters, no control characters")
	}
	if o.s.State.kms == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "no KMS is configured, so a key cannot be kept")
	}
	id := mint.ID("acct")
	path, err := keyPath(org, subject, in.Provider, id)
	if err != nil {
		return nil, zip.ErrBadRequest("this org cannot keep a key")
	}
	buf := []byte(in.Value)
	sum := sha256.Sum256(buf)
	kctx, cancel := kmsCtx(ctx, org)
	err = o.s.State.kms.PutSecret(kctx, path+"@"+kmsEnv, buf)
	cancel()
	clear(buf)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "KMS did not keep the key")
	}
	a := account{ID: id, Subject: subject, Provider: in.Provider, Label: label, KeyRef: "kms://" + path,
		Fingerprint: hex.EncodeToString(sum[:])[:8], CreatedAt: o.s.State.now().Unix()}
	if err := sto.putAccount(ctx, org, a); err != nil {
		o.forget(ctx, org, path)
		if errors.Is(err, errAccountCap) {
			return nil, zip.Errorf(http.StatusConflict, "at most %d provider accounts: delete one first", maxAccounts)
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	o.record(ctx, "provider.account.create", id)
	v := o.accountView(a)
	return &v, nil
}

// forget asks KMS to drop path; what it was not holding is already forgotten.
func (o ops) forget(ctx context.Context, org, path string) error {
	kctx, cancel := kmsCtx(ctx, org)
	defer cancel()
	if err := o.s.State.kms.DeleteSecret(kctx, path+"@"+kmsEnv); err != nil && !errors.Is(err, secret.ErrNone) {
		return err
	}
	return nil
}

// DeleteAccount removes one of the signed-in person's own provider keys. KMS is
// asked to delete the secret first and the row goes only after it has; if KMS
// cannot, the answer is 502 and the account stays. Another person's id is 404. A
// login cannot be deleted here: it goes when the person logs out on its source.
//
// Example: {"id": "acct_0123456789abcdef0123456789abcdef"}
func (o ops) deleteAccount(ctx context.Context, in *AccountRef) (*AccountDeleted, error) {
	sto, org, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	if strings.Contains(in.ID, ":") {
		return nil, zip.ErrBadRequest("a login is removed by logging out on its source")
	}
	a, err := sto.account(ctx, subject, in.ID)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("account not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	if o.s.State.kms == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "no KMS is configured, so a key cannot be removed")
	}
	if err := o.forget(ctx, org, strings.TrimPrefix(a.KeyRef, "kms://")); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "KMS did not forget the key, so the account stays")
	}
	if err := sto.dropAccount(ctx, subject, a.ID); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	o.record(ctx, "provider.account.delete", a.ID)
	return &AccountDeleted{ID: a.ID, Deleted: true}, nil
}
