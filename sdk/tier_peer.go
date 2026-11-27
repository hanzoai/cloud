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

// tierCallTimeout bounds the ask. Every reader treats a tier it could not read as
// unknown, so waiting longer than this buys nothing.
const tierCallTimeout = 5 * time.Second

// Tier is the plan tier commerce derives for subject inside org — free, starter,
// pro or enterprise — asked of the process that owns the subscription store over
// the plane. When this process serves commerce the op runs in-process; otherwise
// it crosses the socket. It is the one tier read: the free-call allowance, the
// rolling spend cap and the ai module's tier gates all call it.
//
// An answer is kept for tierTTL and served while it is refreshed in the background
// after that, so a call waits on commerce only for a pair it has never asked about;
// a plan change reaches the gates within a refresh.
//
// "" with a nil error means no plan can be named: an empty subject, or a
// deployment that runs no commerce. Every other failure is returned, and each
// reader decides which way it fails.
func Tier(ctx context.Context, subject, org string) (string, error) {
	return asked(ctx, "tier", subject, org, askTier)
}

// Plan is the catalog plan commerce serves subject as inside org ("unlimited",
// "pro-3x", ...), asked and kept as Tier is. "" with a nil error means none.
func Plan(ctx context.Context, subject, org string) (string, error) {
	return asked(ctx, "plan", subject, org, askPlan)
}

// LinkedHolder returns the account whose paid subscription org borrows through a
// Shared Plan link, or the zero Account when org uses its own books or holds none.
func LinkedHolder(ctx context.Context, org string) account.Account {
	org = tierOrg(org)
	if org == "" || strings.EqualFold(org, account.SignupOrg) {
		return account.Account{}
	}
	raw, err := asked(ctx, "holder", org, org, askPlanHolder)
	if err != nil || raw == "" {
		return account.Account{}
	}
	return account.Parse(raw)
}

// InvalidateTier drops cached tier, plan and holder answers so a plan link or
// unlink takes effect on the very next read in this process.
func InvalidateTier(org string) {
	org = strings.ToLower(tierOrg(org))
	tiers.mu.Lock()
	if org == "" {
		tiers.m = map[string]tierAnswer{}
	} else {
		for k := range tiers.m {
			if strings.Contains(strings.ToLower(k), "\x00"+org+"\x00") {
				delete(tiers.m, k)
			}
		}
	}
	tiers.mu.Unlock()
}

// tierOrg extracts the commerce organization namespace from org. Callers such as
// hanzoai/ai's RateLimiter pass a composite payer subject ("hanzo/<user>") as
// both subject and org for users in the signup org; commerce routes by org
// slug, so forwarding the composite string makes every lookup miss and fall
// back to the free tier.
func tierOrg(org string) string {
	org = strings.TrimSpace(org)
	if base, _, ok := strings.Cut(org, "/"); ok && base != "" {
		return base
	}
	return org
}

// asked is the cache Tier and Plan share.
func asked(ctx context.Context, kind, subject, org string, ask func(ctx context.Context, subject, org string) (string, error)) (string, error) {
	if strings.TrimSpace(subject) == "" {
		return "", nil
	}
	org = tierOrg(org)
	k := kind + "\x00" + org + "\x00" + subject
	tiers.mu.Lock()
	t, ok := tiers.m[k]
	stale := ok && time.Since(t.at) > tierTTL
	if stale && !t.asking {
		t.asking = true
		tiers.m[k] = t
		go refreshTier(k, subject, org, ask)
	}
	tiers.mu.Unlock()
	if ok {
		return t.name, nil
	}
	name, err := ask(ctx, subject, org)
	if err == nil {
		keepTier(k, name)
	}
	return name, err
}

// tierTTL is how long a tier answer is served before it is asked again.
var tierTTL = 30 * time.Second

type tierAnswer struct {
	name   string
	at     time.Time
	asking bool
}

var tiers = struct {
	mu sync.Mutex
	m  map[string]tierAnswer
}{m: map[string]tierAnswer{}}

// tierMax bounds the pairs kept; past it the map starts again.
const tierMax = 1 << 16

func keepTier(k, name string) {
	tiers.mu.Lock()
	if len(tiers.m) >= tierMax {
		tiers.m = map[string]tierAnswer{}
	}
	tiers.m[k] = tierAnswer{name: name, at: time.Now()}
	tiers.mu.Unlock()
}

func refreshTier(k, subject, org string, ask func(ctx context.Context, subject, org string) (string, error)) {
	name, err := ask(context.Background(), subject, org)
	if err == nil {
		keepTier(k, name)
		return
	}
	tiers.mu.Lock()
	if t, ok := tiers.m[k]; ok {
		t.asking = false
		tiers.m[k] = t
	}
	tiers.mu.Unlock()
}

func askTier(ctx context.Context, subject, org string) (string, error) {
	ask := peersOf().TierOf
	if ask == nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(For(ctx, org), tierCallTimeout)
	defer cancel()
	name, err := ask(ctx, subject)
	switch {
	case errors.Is(err, ErrNoPeer):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("tier: %w", err)
	}
	return strings.TrimSpace(name), nil
}

func askPlan(ctx context.Context, subject, org string) (string, error) {
	ask := peersOf().PlanOf
	if ask == nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(For(ctx, org), tierCallTimeout)
	defer cancel()
	name, err := ask(ctx, subject)
	switch {
	case errors.Is(err, ErrNoPeer):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("plan: %w", err)
	}
	return strings.TrimSpace(name), nil
}

func askPlanHolder(ctx context.Context, subject, org string) (string, error) {
	ask := peersOf().PlanHolderOf
	if ask == nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(For(ctx, org), tierCallTimeout)
	defer cancel()
	holder, err := ask(ctx, subject)
	switch {
	case errors.Is(err, ErrNoPeer):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("plan holder: %w", err)
	}
	return strings.TrimSpace(holder), nil
}
