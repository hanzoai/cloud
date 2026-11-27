package sandbox

// quota.go — how many sandboxes one paying account holds running at once.
//
// A paid lease is admitted by its payer's balance (prepaid.go), and beside it by a
// count: how many of the account's paid sandboxes hold a pod. The account is the
// org the lease is taken in, whoever pays for it — a SuperAdmin leasing inside an
// org counts against that org, though the SuperAdmin's own wallet pays — or, in
// the signup org, where each person is their own customer, the person paying. A
// free lease is not counted; the free lane's allowance and its hour bound it.
//
// HOW MANY IS STAFF'S TO SET, per org or per plan, in admin.hanzo.ai: the limit
// sandbox.running, which commerce resolves org, then plan, then the registry's
// default (client/limit.go). Commerce is asked and the answer kept for quotaTTL.
// When commerce cannot answer, the last answer holds, else the default: the quota
// never refuses a lease because the question could not be asked. The balance read
// beside it refuses on that already, and a quota is a bound, not a gate on money.
//
// The count is the store's, not a process's: a restart forgets nothing, and every
// door to a pod (a lease, a resume) asks the same question of the same rows.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// counted is the account a paid lease in org counts against: the org, whoever
// pays, or in the signup org the person paying.
func counted(org string, payer account.Account) string {
	if org == account.SignupOrg {
		return payer.Subject()
	}
	return org
}

// quotaTTL is how long one answer from commerce is used before it is asked again.
const quotaTTL = 15 * time.Second

// quotaKept is how many accounts' answers are kept before the stale ones go.
const quotaKept = 10_000

// quotas is the last answer commerce gave for each account. The zero value is
// ready to use.
type quotas struct {
	mu sync.Mutex
	m  map[string]quotaAt
}

// quotaAt is one answer and when it was given.
type quotaAt struct {
	n  int64
	at time.Time
}

// askQuota is how this package asks commerce for one account's sandbox.running.
// A var so a test can answer without commerce.
var askQuota = func(ctx context.Context, org, acct string) (int64, error) {
	s, err := commercepeer.BillingLimits(cloud.For(ctx, org), &client.LimitsIn{Subject: acct})
	if err != nil {
		return 0, err
	}
	if s == nil {
		return 0, errors.New("commerce answered nothing")
	}
	for _, v := range s.Values {
		if v.Key == client.LimitSandboxRunning {
			return v.Value, nil
		}
	}
	return 0, fmt.Errorf("commerce answered no %s", client.LimitSandboxRunning)
}

// of is how many paid sandboxes acct may hold running at once in org; -1 for no
// bound. Asked of commerce once per quotaTTL; when commerce cannot answer, the
// last answer, else the default.
func (q *quotas) of(ctx context.Context, log luxlog.Logger, org, acct string) int64 {
	key := org + "\x00" + acct
	now := time.Now()
	q.mu.Lock()
	last, ok := q.m[key]
	q.mu.Unlock()
	if ok && now.Sub(last.at) < quotaTTL {
		return last.n
	}
	n, err := askQuota(ctx, org, acct)
	if err != nil {
		if !ok {
			l, _ := client.LimitOf(client.LimitSandboxRunning)
			last.n = l.Default
		}
		if log != nil {
			log.Warn("sandbox quota: commerce did not answer; holding the last known or default",
				"org", org, "account", acct, "quota", last.n, "err", err)
		}
		return last.n
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.m == nil {
		q.m = map[string]quotaAt{}
	}
	if len(q.m) >= quotaKept {
		for k, v := range q.m {
			if now.Sub(v.at) >= quotaTTL {
				delete(q.m, k)
			}
		}
	}
	q.m[key] = quotaAt{n: n, at: now}
	return n
}

// within refuses a paid lease that would take its account past its quota. A
// lease with no payer is the free lane's, and its allowance is what bounds it.
func within(s *Service, ctx context.Context, st *Store, org string, payer account.Account) error {
	if payer.Zero() {
		return nil
	}
	acct := counted(org, payer)
	q := s.State.quotas.of(ctx, s.Log, org, acct)
	if q < 0 {
		return nil
	}
	n, err := st.LivePaid(ctx, org, acct)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "count: %v", err)
	}
	if n >= q {
		return zip.Errorf(http.StatusTooManyRequests,
			"%s has %d sandboxes running, its quota of %d; end one, or ask support for a higher quota", acct, n, q)
	}
	return nil
}

// quotaOf is acct's quota in org and how much of it is in use.
func quotaOf(s *Service, ctx context.Context, org, acct string) (*client.Quota, error) {
	st, err := storeFor(s, org)
	if err != nil {
		return nil, err
	}
	n, err := st.LivePaid(ctx, org, acct)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count: %v", err)
	}
	return &client.Quota{Account: acct, Running: n, Quota: s.State.quotas.of(ctx, s.Log, org, acct)}, nil
}

// Answers the caller's sandbox quota: the account it belongs to — the org, or a
// person in the signup org — how many of that account's paid sandboxes are
// running, and how many may run at once (-1 for no bound). Staff set the quota per
// org or per plan; where they set none it is 100.
//
// Response: {"account": "acme", "running": 3, "quota": 100}
func (o ops) quota(ctx context.Context, _ *struct{}) (*client.Quota, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	payer := cloud.PayerOf(ctx).Wallet
	if payer.Zero() {
		return nil, zip.Errorf(http.StatusPaymentRequired, "this caller names no wallet, so it holds no quota")
	}
	return quotaOf(o.s, ctx, org, counted(org, payer))
}

// planeQuota answers how many of the caller org's paid sandboxes run and how many
// it may hold at once. The signup org holds no quota of its own: each person in
// it holds theirs, so it is refused.
func planeQuota(ctx context.Context, _ *cloud.Unit) (*client.Quota, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	if org == account.SignupOrg {
		return nil, zip.ErrBadRequest("quota: each person in the signup org holds their own")
	}
	return quotaOf(s, ctx, org, org)
}
