// review.go — what a member's deploy may cost, and how long what it leaves in
// universe lives.
//
// An org admin's deploy commits to universe main; anyone else's opens a review
// branch (declare.go). Each is a build, which waits its turn in the org's line
// (dispatch.go), and a branch in universe that nothing else ever removes. So a member's deploys are
// bounded three ways, each counted from one row per review in the platform store:
//
//	memberDeploys per memberWindow   how fast one member may deploy
//	memberReviews open at once       how many unsettled branches one member holds
//	reviewTTL                        how long an unmerged branch stands
//
// A review is SETTLED — its branch deleted and its row closed — when main declares
// its tag (it was merged), when it is older than reviewTTL (it went stale, and a
// review somebody closed without merging is reaped then too), or when its branch
// never appeared (the build or the push failed after the reservation). The sweep
// runs every reviewSweep, and for one member at the moment they reach their cap.
// Only rows this file wrote are ever settled, so a branch an admin asked for, or
// one a person pushed by hand, is never deleted here.

package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/zap-proto/zip"
)

const (
	// memberDeploys bounds how many deploys one member opens in memberWindow.
	memberDeploys = 10
	memberWindow  = time.Hour
	// memberReviews bounds how many unsettled review branches one member holds.
	memberReviews = 5
	// reviewTTL is how long an unmerged review branch stands.
	reviewTTL = 7 * 24 * time.Hour
	// reviewSweep is how often every open review is settled.
	reviewSweep = time.Hour
	// reviewGrace is how long a row may stand before its branch exists: the build
	// and the push that write it follow the reservation, bounded by declareDeadline.
	reviewGrace = 2 * declareDeadline
)

var (
	errMemberRate    = fmt.Errorf("a member opens at most %d deploys an hour", memberDeploys)
	errMemberReviews = fmt.Errorf("a member holds at most %d open review branches; merge one, or wait for one to be reaped", memberReviews)
)

// review is one member's review branch in universe, as the store records it.
type review struct {
	Branch    string // deploy/<org>/<app>/<tag>, the key
	Org       string // the org directory it declares into
	User      string // the member who opened it
	App       string
	Tag       string // the image tag the branch declares
	CreatedAt int64
	ClosedAt  int64 // 0 while open
}

// reserveReview records r if its member is inside both bounds, in one
// transaction under the store's write lock (Dialect.BeginWrite): two deploys by one
// member, on this replica or another, cannot both pass the count and both insert.
func (s *Store) reserveReview(ctx context.Context, r review, now time.Time) error {
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var recent, open int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM platform_reviews WHERE org=$1 AND "user"=$2 AND created_at>=$3`,
		r.Org, r.User, now.Add(-memberWindow).Unix()).Scan(&recent); err != nil {
		return fmt.Errorf("count reviews: %w", err)
	}
	if recent >= memberDeploys {
		return errMemberRate
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM platform_reviews WHERE org=$1 AND "user"=$2 AND closed_at=0`,
		r.Org, r.User).Scan(&open); err != nil {
		return fmt.Errorf("count reviews: %w", err)
	}
	if open >= memberReviews {
		return errMemberReviews
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO platform_reviews (branch,org,"user",app,tag,created_at,closed_at) VALUES ($1,$2,$3,$4,$5,$6,0)`,
		r.Branch, r.Org, r.User, r.App, r.Tag, now.Unix()); err != nil {
		return fmt.Errorf("record review: %w", err)
	}
	return tx.Commit()
}

// releaseReview forgets a reservation whose deploy launched nothing.
func (s *Store) releaseReview(ctx context.Context, org, branch string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM platform_reviews WHERE org=$1 AND branch=$2`, org, branch)
	return err
}

// openReviews is every open review in every org — the sweep's work list.
func (s *Store) openReviews(ctx context.Context) ([]review, error) {
	return s.queryReviews(ctx, `SELECT branch,org,"user",app,tag,created_at,closed_at FROM platform_reviews WHERE closed_at=0`)
}

// memberOpenReviews is one member's open reviews in one org.
func (s *Store) memberOpenReviews(ctx context.Context, org, user string) ([]review, error) {
	return s.queryReviews(ctx, `SELECT branch,org,"user",app,tag,created_at,closed_at FROM platform_reviews WHERE org=$1 AND "user"=$2 AND closed_at=0`, org, user)
}

func (s *Store) queryReviews(ctx context.Context, q string, args ...any) ([]review, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []review
	for rows.Next() {
		var r review
		if err := rows.Scan(&r.Branch, &r.Org, &r.User, &r.App, &r.Tag, &r.CreatedAt, &r.ClosedAt); err != nil {
			return nil, fmt.Errorf("scan review: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// closeReviews marks the named reviews settled.
func (s *Store) closeReviews(ctx context.Context, branches []string, now time.Time) error {
	for _, b := range branches {
		if _, err := s.db.ExecContext(ctx, `UPDATE platform_reviews SET closed_at=$1 WHERE branch=$2 AND closed_at=0`, now.Unix(), b); err != nil {
			return fmt.Errorf("close review: %w", err)
		}
	}
	return nil
}

// pruneReviews forgets settled reviews the rate window no longer counts.
func (s *Store) pruneReviews(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM platform_reviews WHERE closed_at<>0 AND created_at<$1`, now.Add(-memberWindow).Unix())
	return err
}

// reserveMember holds a place for one member's review, settling their open ones
// first when they are at their cap, and answers 429 naming the bound when they
// are outside it.
func reserveMember(s *cloud.Service[state], ctx context.Context, r review) error {
	if s.State.store == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "the platform store is not open, so a member's deploy cannot be bounded; ask an org admin")
	}
	now := time.Now()
	err := s.State.store.reserveReview(ctx, r, now)
	if errors.Is(err, errMemberReviews) {
		// At the cap. A merged or abandoned review still holds its place until it
		// is settled, so settle this member's before refusing them.
		if open, lerr := s.State.store.memberOpenReviews(ctx, r.Org, r.User); lerr == nil {
			if _, serr := settleReviews(s, ctx, open, now); serr != nil {
				s.Log.Warn("settle a member's reviews", "org", r.Org, "err", serr)
			}
		}
		err = s.State.store.reserveReview(ctx, r, now)
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errMemberRate), errors.Is(err, errMemberReviews):
		return zip.Errorf(http.StatusTooManyRequests, "%v", err)
	}
	s.Log.Error("reserve a member's review", "org", r.Org, "err", err)
	return zip.Errorf(http.StatusServiceUnavailable, "could not record the review this deploy opens; nothing was built")
}

// settleReviews deletes the branches of the reviews that are done and closes
// their rows: merged (main declares the tag), stale (older than reviewTTL), or
// never written (no branch past reviewGrace). It answers how many it closed.
func settleReviews(s *cloud.Service[state], ctx context.Context, open []review, now time.Time) (int, error) {
	if len(open) == 0 {
		return 0, nil
	}
	u, err := universeGrant(ctx)
	if err != nil {
		return 0, fmt.Errorf("universe credential: %w", err)
	}
	env := pinGitEnv(u)
	gctx, cancel := context.WithTimeout(ctx, declareDeadline)
	defer cancel()
	var done []string
	err = universeClone(gctx, u, func(dir string) error {
		heads, err := reviewHeads(gctx, dir, env)
		if err != nil {
			return err
		}
		var drop []string
		for _, r := range open {
			age := now.Sub(time.Unix(r.CreatedAt, 0))
			switch {
			case !heads[r.Branch] && age > reviewGrace:
				done = append(done, r.Branch)
			case !heads[r.Branch]:
				// Still being written.
			case age > reviewTTL || merged(dir, r):
				drop = append(drop, r.Branch)
			}
		}
		if len(drop) == 0 {
			return nil
		}
		args := []string{"push", "origin", "--delete"}
		for _, b := range drop {
			args = append(args, "refs/heads/"+b)
		}
		if _, perr := runGit(gctx, dir, env, args...); perr != nil {
			// Some may have gone and some not: close what is gone.
			left, lerr := reviewHeads(gctx, dir, env)
			if lerr != nil {
				return perr
			}
			for _, b := range drop {
				if !left[b] {
					done = append(done, b)
				}
			}
			return perr
		}
		done = append(done, drop...)
		return nil
	})
	if cerr := s.State.store.closeReviews(ctx, done, now); cerr != nil {
		return 0, cerr
	}
	return len(done), err
}

// reviewHeads is every review branch universe holds, by name.
func reviewHeads(ctx context.Context, dir string, env []string) (map[string]bool, error) {
	out, err := runGit(ctx, dir, env, "ls-remote", "--heads", "origin", "refs/heads/"+declareBranchPrefix+"/*")
	if err != nil {
		return nil, fmt.Errorf("list review branches: %w", err)
	}
	heads := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			heads[strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")] = true
		}
	}
	return heads, nil
}

// merged is whether universe main already declares the review's tag.
func merged(root string, r review) bool {
	f, err := readPinFile(filepath.Join(root, filepath.FromSlash(declarePath(r.Org, r.App))))
	return err == nil && f.current == r.Tag
}

// runReviewSweeper settles every open review each reviewSweep until ctx ends.
func runReviewSweeper(s *cloud.Service[state], ctx context.Context) {
	lead := s.Coord.Lead("platform:reviews")
	t := time.NewTicker(reviewSweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			lead.Do(ctx, func(ctx context.Context) { sweepReviews(s, ctx, time.Now()) })
		}
	}
}

// sweepReviews is one pass: settle what is done, forget what no bound counts.
func sweepReviews(s *cloud.Service[state], ctx context.Context, now time.Time) {
	open, err := s.State.store.openReviews(ctx)
	if err != nil {
		s.Log.Warn("list open reviews", "err", err)
		return
	}
	if n, err := settleReviews(s, ctx, open, now); err != nil {
		s.Log.Warn("settle reviews", "closed", n, "err", err)
	} else if n > 0 {
		s.Log.Info("settled member reviews", "closed", n)
	}
	if err := s.State.store.pruneReviews(ctx, now); err != nil {
		s.Log.Warn("prune reviews", "err", err)
	}
}

// memberNotice is what a member's deploy response says about the branch it
// opened: that it deploys nothing until merged, when it is removed, and that its
// public env values are cleartext there — a branch is git history the moment it
// is pushed, and deleting it does not unpublish what it carried.
func memberNotice(public []string) string {
	n := "This deploy opened a review: the declaration is on a branch of universe and deploys nothing until it is merged. " +
		"The branch is deleted once main declares this tag, or after 7 days if it is not merged."
	if len(public) > 0 {
		n += " Its public env values (" + strings.Join(public, ", ") + ") are written in plaintext on that branch, " +
			"readable by anyone who can read universe, and deleting the branch does not unpublish them."
	}
	return n
}
