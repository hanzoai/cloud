// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// planlink_store.go — the plan link: which account's plan an org borrows.
//
// A plan is a subscription in ONE org's books, held by one subject. A link says an org
// reads its plan from another account's recorded subscriptions instead of its own; it
// names no tier and moves no money. It is platform-level data (the org's tier read asks
// by org, an unlink asks by source), so it is a system store: one database for every
// org, a schema on hanzo-sql or a SQLite file, in the dialect both engines share.
//
// Two uniques are the product rule, enforced by the database and not by a read before a
// write: an org borrows at most ONE plan (org is the key), and a plan is lent to at most
// ONE org (source is unique). Three replicas linking at once land one after the other on
// those keys.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
)

// linkSubsystem names the store: the second half of its key, and its entry in
// manifest.SQLStores.
const linkSubsystem = "planlinks"

// planLink is one link. From is account.Account's own form, so a person's wallet and an
// org's account are told apart by the type and never by guessing at a slash.
type planLink struct {
	// Org is the org that borrows the plan.
	Org string
	// From is the account whose recorded subscriptions the plan is read from.
	From account.Account
	// By is the IAM row ("<owner>/<name>") of the person who linked it.
	By string
	// At is when it was linked.
	At time.Time
}

// What a link is refused for: the two uniques.
var (
	errOrgLinked    = errors.New("plan links: the org already borrows a plan")
	errSourceLinked = errors.New("plan links: the plan is already lent to an org")
)

// linkStore is the plan-link database.
type linkStore struct{ db *sql.DB }

// openLinks opens the store under dir, the commerce data directory, as every store of
// this deployment is opened (locus.Use).
func openLinks(lg luxlog.Logger, dir string) (*linkStore, error) {
	s, err := locus.Use(context.Background(), lg, namespace.System(), linkSubsystem, dir, func(db *sql.DB) (*linkStore, error) {
		s := &linkStore{db: db}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS plan_links (
  org       TEXT PRIMARY KEY,
  source    TEXT NOT NULL UNIQUE,
  linked_by TEXT NOT NULL,
  linked_at BIGINT NOT NULL
)`)
			return err
		}); err != nil {
			return nil, fmt.Errorf("migrate: %w", err)
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open plan links: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *linkStore) Close() error { return s.db.Close() }

// planLinks is the store this process opened at boot. It is a handle, never the truth: a
// replica that restarts, or a sibling, reads the same rows.
var planLinks atomic.Pointer[linkStore]

// links is the store, or the 503 a process that never opened it answers with. A tier that
// cannot be read is an error and never Free, so an absent store is not an absent link.
func links() (*linkStore, error) {
	if s := planLinks.Load(); s != nil {
		return s, nil
	}
	return nil, zip.Errorf(503, "plan links: the store is not open in this process")
}

// foldOrg is an org as the store keys it, the way account folds one.
func foldOrg(org string) string { return strings.ToLower(strings.TrimSpace(org)) }

// Put records l, or says which unique it broke: errOrgLinked when the org already
// borrows a plan, errSourceLinked when the plan is already lent. The constraints decide;
// the reads after a refusal only name which one, since the engines spell the violation
// differently.
func (s *linkStore) Put(ctx context.Context, l planLink) error {
	org := foldOrg(l.Org)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO plan_links (org, source, linked_by, linked_at) VALUES ($1,$2,$3,$4)`,
		org, l.From.String(), l.By, l.At.Unix())
	if err == nil {
		return nil
	}
	if _, ok, qerr := s.ByOrg(ctx, org); qerr == nil && ok {
		return errOrgLinked
	}
	if _, ok, qerr := s.BySource(ctx, l.From); qerr == nil && ok {
		return errSourceLinked
	}
	return fmt.Errorf("plan links: link %s: %w", org, err)
}

// ByOrg is the plan org borrows.
func (s *linkStore) ByOrg(ctx context.Context, org string) (planLink, bool, error) {
	return s.one(ctx, `SELECT org, source, linked_by, linked_at FROM plan_links WHERE org = $1`, foldOrg(org))
}

// BySource is the link an account's plan is lent through.
func (s *linkStore) BySource(ctx context.Context, from account.Account) (planLink, bool, error) {
	return s.one(ctx, `SELECT org, source, linked_by, linked_at FROM plan_links WHERE source = $1`, from.String())
}

func (s *linkStore) one(ctx context.Context, query string, arg string) (planLink, bool, error) {
	var org, source, by string
	var at int64
	err := s.db.QueryRowContext(ctx, query, arg).Scan(&org, &source, &by, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return planLink{}, false, nil
	}
	if err != nil {
		return planLink{}, false, fmt.Errorf("plan links: read: %w", err)
	}
	from := account.Parse(source)
	if from.Zero() {
		return planLink{}, false, fmt.Errorf("plan links: %s names an account that does not parse: %q", org, source)
	}
	return planLink{Org: org, From: from, By: by, At: time.Unix(at, 0).UTC()}, true, nil
}

// Drop ends exactly the link l, as it was read: a link another act replaced since — the
// org unlinked and borrowed another plan, the plan lent to another org — is not l and is
// left alone. It says whether l was there to end.
func (s *linkStore) Drop(ctx context.Context, l planLink) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM plan_links WHERE org = $1 AND source = $2`,
		foldOrg(l.Org), l.From.String())
	if err != nil {
		return false, fmt.Errorf("plan links: unlink: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("plan links: unlink: %w", err)
	}
	return n > 0, nil
}
