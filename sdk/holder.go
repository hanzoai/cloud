// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/account"
)

// Holder is the account whose plan a call by person ("<home org>/<name>") in org may
// spend when the payer org's books hold no plan of their own: the person's own
// account, when person owns org. A key made in an org someone owns — a project's —
// spends its owner's plan; a member of an org someone else owns never lends theirs to
// it, nor borrows the owner's. Zero when person is not named, is the payer already,
// or does not own org; an ownership nobody can read is not ownership.
func Holder(ctx context.Context, person, org string) account.Account {
	if strings.TrimSpace(org) == "" {
		return account.Account{}
	}
	if linked := LinkedHolder(ctx, org); !linked.Zero() {
		return linked
	}
	if !strings.Contains(person, "/") {
		return account.Account{}
	}
	acct := account.PayerOf("", person)
	if acct.Zero() || acct.Org() == strings.ToLower(strings.TrimSpace(org)) {
		return account.Account{} // acting at home: their own books are the payer's
	}
	if owner, err := Owns(ctx, person, org); err != nil || !owner {
		return account.Account{}
	}
	return acct
}

// Owns reports whether person holds an owner membership in org, asked of IAM over the
// plane and kept for ownerTTL. No IAM in this deployment owns nothing.
func Owns(ctx context.Context, person, org string) (bool, error) {
	k := org + "\x00" + person
	owners.mu.Lock()
	o, ok := owners.m[k]
	owners.mu.Unlock()
	if ok && time.Since(o.at) < ownerTTL {
		return o.owner, nil
	}
	ask := peersOf().OwnerOf
	if ask == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, tierCallTimeout)
	defer cancel()
	owner, err := ask(ctx, person, org)
	switch {
	case errors.Is(err, ErrNoPeer):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("owner: %w", err)
	}
	owners.mu.Lock()
	if len(owners.m) >= tierMax {
		owners.m = map[string]ownerAnswer{}
	}
	owners.m[k] = ownerAnswer{owner: owner, at: time.Now()}
	owners.mu.Unlock()
	return owner, nil
}

// ownerTTL is how long an ownership answer is kept.
var ownerTTL = time.Minute

type ownerAnswer struct {
	owner bool
	at    time.Time
}

var owners = struct {
	mu sync.Mutex
	m  map[string]ownerAnswer
}{m: map[string]ownerAnswer{}}
