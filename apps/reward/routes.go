package reward

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/ai/address"
	"github.com/hanzoai/reward/catalog"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	referralpeer "github.com/hanzoai/cloud/client/referral"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// The refusals the member's operations answer.
var (
	errNoGitHub  = zip.Errorf(http.StatusForbidden, "link your GitHub account at sign-in first")
	errBadRepo   = zip.ErrBadRequest("repo must be owner/name on GitHub")
	errNotPublic = zip.Errorf(http.StatusUnprocessableEntity, "that repository is not public on GitHub")
	errNotOwner  = zip.Errorf(http.StatusUnprocessableEntity, "that repository is not owned by the GitHub account you linked")
	errNoBadge   = zip.Errorf(http.StatusUnprocessableEntity, "that repository's README carries no Hanzo badge image")
	errSlow      = zip.Errorf(http.StatusTooManyRequests, "that was asked a moment ago; try again later")
)

// codeFn is the referral code of an org: the ?ref= its links carry. A variable so a
// test names it.
var codeFn = func(ctx context.Context, org string) (string, error) {
	out, err := referralpeer.ReferralCode(cloud.For(context.WithoutCancel(ctx), org), &client.ReferralOrg{Org: org})
	if err != nil {
		return "", err
	}
	if out == nil || strings.TrimSpace(out.Code) == "" {
		return "", errors.New("referral answered no code")
	}
	return strings.TrimSpace(out.Code), nil
}

// asked counts a member's use of an operation across the deployment.
var asked = cloud.NewWindow("reward:ops", 1, time.Hour)

// allow spends one of limit per window of the member's use of name.
func allow(name, who string, limit int, window time.Duration) bool {
	left, _ := asked.Spend([]address.Bucket{{Key: name + "|" + who, Scale: 1}}, limit, window)
	return left >= 0
}

// refusal maps what the operations return to the status a caller reads.
func refusal(err error) error {
	var he *zip.HTTPError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &he):
		return err
	case errors.Is(err, errTaken):
		return zip.Errorf(http.StatusConflict, "%v", err)
	case errors.Is(err, errNotPost):
		return zip.ErrBadRequest(err.Error())
	case errors.Is(err, errNotCounted):
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	case errors.Is(err, errBusy):
		return zip.Errorf(http.StatusServiceUnavailable, "%v; try again in a little while", err)
	}
	return zip.Errorf(http.StatusInternalServerError, "reward: %v", err)
}

// member is the caller's subject and org, or the refusal that they are nobody.
func member(ctx context.Context) (sub, org string, err error) {
	w := cloud.Who(ctx)
	if w.User == "" || w.Org == "" {
		return "", "", principal.Unauthenticated()
	}
	// The program's subject is a person's IAM id. An API key presents "<org>/<name>"
	// and an application "app:<id>": neither is the person the points are for.
	if strings.Contains(w.User, "/") || strings.HasPrefix(w.User, "app:") {
		return "", "", zip.ErrForbidden("sign in as yourself to use the rewards program: an API key or an application is not a person")
	}
	return w.User, w.Org, nil
}

// staffOnly is the refusal for a caller who is not the platform's SuperAdmin.
func staffOnly(ctx context.Context) error {
	if _, _, err := member(ctx); err != nil {
		return err
	}
	if !principal.SuperFrom(ctx) {
		return zip.ErrForbidden("reward: staff only")
	}
	return nil
}

// routes registers this app's operations beside the module's.
func (s *state) routes(app cloud.Router) {
	app.Get("/v1/reward/rules", s.rules)
	me := app.Group("/v1/reward/me")
	me.Post("/posts", s.postSubmit, zip.WithStatus(http.StatusCreated))
	me.Get("/posts", s.postList)
	me.Post("/posts/scan", s.postScan)
	me.Post("/posts/:id/recheck", s.postRecheck)
	me.Post("/github/badge", s.githubBadge)
	me.Post("/github/check", s.githubCheck)
	admin := app.Group("/v1/admin/reward")
	admin.Get("/posts", s.adminPosts)
	admin.Post("/posts/:id/recheck", s.adminRecheck)
	admin.Post("/legacy", s.adminLegacy)
	s.exposeGitHub()
	s.exposeProfiles()
	s.exposeLevels()
}

// ── posts ─────────────────────────────────────────────────────────────────────

// postIn is a post a member pastes.
type postIn struct {
	// URL is the address of the post, on X, Bluesky, Reddit, Hacker News, Mastodon
	// or LinkedIn.
	URL string `json:"url"`
	// Handle is who you are on that network, so the post's author can be matched.
	// Optional; without it the author match is unknown.
	Handle string `json:"handle,omitempty"`
}

// postRef names a post.
type postRef struct {
	ID string `json:"id"`
}

// postView is a post as its owner reads it.
type postView struct {
	ID      string `json:"id"`
	Network string `json:"network"`
	URL     string `json:"url"`
	// Status is pending (held for its week, counts for nothing), verified (it
	// stayed up and was put to staff), removed (it earns nothing) or review (staff
	// decide it by hand).
	Status string `json:"status"`
	// Note says why a post was removed.
	Note string `json:"note,omitempty"`
	// Week is the ISO week the post was published in; a post in a later week can
	// earn again.
	Week string `json:"week"`
	// Since is when the post's week began; CountsAt when it counts, and DaysLeft the
	// days until then. Both are absent unless the post is pending.
	Since    time.Time  `json:"since"`
	CountsAt *time.Time `json:"countsAt,omitempty"`
	DaysLeft int        `json:"daysLeft,omitempty"`
	// Checks is every look at the post, oldest first.
	Checks []catalog.Check `json:"checks"`
}

// postList is a member's posts.
type postList struct {
	Posts []postView `json:"posts"`
	// Pending is how many posts are held and count for nothing yet.
	Pending int `json:"pending"`
}

func viewOf(p *Post) postView {
	v := postView{ID: p.Id(), Network: p.Network, URL: p.URL, Status: p.Status, Note: p.Note, Week: p.Week, Since: p.Since, Checks: p.Checks}
	if v.Checks == nil {
		v.Checks = []catalog.Check{}
	}
	if p.Status == pending {
		at := p.Since.Add(holdFor)
		v.CountsAt = &at
		v.DaysLeft = max(1, int(time.Until(at).Hours()/24+0.999))
	}
	return v
}

// Takes the address of a post of yours, reads it from its network, and holds it for
// a week if it is public, is yours, and carries your own referral link. A held post
// is not points: it is looked at every day, and one found gone, private or without
// your link is removed and earns nothing. One still up after seven days is put to
// staff, who decide it. Pasting a post again answers the first.
//
// Example: {"url": "https://bsky.app/profile/you.bsky.social/post/3k..."}
func (s *state) postSubmit(ctx context.Context, in *postIn) (*postView, error) {
	sub, org, err := member(ctx)
	if err != nil {
		return nil, err
	}
	if !allow("post", sub, 20, time.Hour) {
		return nil, errSlow
	}
	p, err := s.submit(ctx, sub, org, in.URL, in.Handle)
	if err != nil {
		return nil, refusal(err)
	}
	v := viewOf(p)
	return &v, nil
}

// Lists your posts, newest first, each with its status and every look at it. Pending
// posts are not points; the count of them is beside the list.
func (s *state) postList(ctx context.Context, _ *cloud.Unit) (*postList, error) {
	sub, _, err := member(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.postsOf(ctx, sub)
	if err != nil {
		return nil, refusal(err)
	}
	out := &postList{Posts: make([]postView, 0, len(rows))}
	for _, p := range rows {
		out.Posts = append(out.Posts, viewOf(p))
		if p.Status == pending {
			out.Pending++
		}
	}
	return out, nil
}

// recheck looks at a post now and runs the same checks as the daily look. who is
// the caller; staff may look at any post, a member at their own pending or removed
// ones, and a post on a network that cannot be read at none.
func (s *state) recheck(ctx context.Context, id, who string, isStaff bool) (*postView, error) {
	p, err := s.load(ctx, id)
	if err != nil {
		return nil, refusal(err)
	}
	if p == nil || (!isStaff && p.Subject != who) {
		return nil, zip.ErrNotFound("no such post")
	}
	if p.Status == review {
		return nil, zip.ErrBadRequest("that post is on a network Hanzo cannot read; staff decide it by hand")
	}
	if !isStaff && p.Status != pending && p.Status != removed {
		return nil, zip.ErrBadRequest("only a pending or removed post is looked at again")
	}
	if !isStaff && !allow("recheck", who, 10, time.Hour) {
		return nil, errSlow
	}
	if err := s.step(ctx, p); err != nil {
		return nil, refusal(err)
	}
	v := viewOf(p)
	return &v, nil
}

// Looks at one of your pending or removed posts now, with the same checks as the
// daily look, and adds the look to its history. A removed post that is back up and
// carries your link is held again and its week starts over. Asked at most ten times
// an hour.
//
// Example: {"id": "post:9f8c1d2e"}
func (s *state) postRecheck(ctx context.Context, in *postRef) (*postView, error) {
	sub, _, err := member(ctx)
	if err != nil {
		return nil, err
	}
	return s.recheck(ctx, strings.TrimSpace(in.ID), sub, false)
}

// scanIn asks Hanzo to look through a member's recent public posts on a network.
type scanIn struct {
	// Network is bluesky, mastodon, reddit, hn or devto.
	Network string `json:"network"`
	// Handle is your handle there; on Mastodon user@instance.
	Handle string `json:"handle"`
}

// scanOut is what a scan found.
type scanOut struct {
	// Looked is how many of the recent posts were read, Held how many carried your
	// link and are now held, and Skipped how many did not.
	Looked  int        `json:"looked"`
	Held    int        `json:"held"`
	Skipped int        `json:"skipped"`
	Posts   []postView `json:"posts"`
}

// scanPosts is the most recent posts one scan reads.
const scanPosts = 10

// Reads your recent public posts on Bluesky, Mastodon, Reddit, Hacker News or DEV by the
// handle you give, and holds each that carries your own referral link, as pasting
// it would. Every other network is pasted as an address: X, Medium and Hashnode are read when
// pasted, and LinkedIn, TikTok, YouTube, Threads, Instagram and Facebook cannot be read
// without your sign-in there, so staff decide those by hand. Asked at most three times an hour.
//
// Example: {"network": "bluesky", "handle": "you.bsky.social"}
func (s *state) postScan(ctx context.Context, in *scanIn) (*scanOut, error) {
	sub, org, err := member(ctx)
	if err != nil {
		return nil, err
	}
	if !allow("scan", sub, 3, time.Hour) {
		return nil, errSlow
	}
	urls, err := recentFn(ctx, strings.ToLower(strings.TrimSpace(in.Network)), strings.TrimSpace(in.Handle))
	if err != nil {
		if errors.Is(err, errRefusedNetwork) {
			return nil, zip.ErrBadRequest("Hanzo reads Bluesky, Mastodon, Reddit, Hacker News and DEV posts by handle; paste the address of any other post")
		}
		return nil, refusal(errors.Join(errBusy, err))
	}
	out := &scanOut{Posts: []postView{}}
	for _, u := range urls[:min(len(urls), scanPosts)] {
		out.Looked++
		p, err := s.submit(ctx, sub, org, u, in.Handle)
		switch {
		case err == nil:
			out.Held++
			out.Posts = append(out.Posts, viewOf(p))
		case errors.Is(err, errBusy):
			return out, refusal(err)
		default:
			out.Skipped++
		}
	}
	return out, nil
}

// ── GitHub ────────────────────────────────────────────────────────────────────

// Verifies against GitHub's API that a project of yours carries a Hanzo badge image
// in its README, and puts one claim to staff for each kind it carries: your live
// profile badge, your rank badge, or the Built with Hanzo badge. The project must be
// public, not a fork, and owned by the GitHub account you linked at sign-in. Each kind
// is counted once for the project, so asking again claims nothing twice. Asked at most
// ten times an hour.
//
// Example: {"repo": "you/your-project"}
func (s *state) githubBadge(ctx context.Context, in *badgeIn) (*badgeOut, error) {
	sub, _, err := member(ctx)
	if err != nil {
		return nil, err
	}
	if !allow("badge", sub, 10, time.Hour) {
		return nil, errSlow
	}
	out, err := s.badge(ctx, sub, in.Repo)
	return out, refusalGitHub(err)
}

// Reads the stars, follows and forks of the GitHub account you linked at sign-in,
// from GitHub's API, and puts to staff each that is a repository or account of ours,
// once each. Asked at most twice an hour.
//
// Example: {}
func (s *state) githubCheck(ctx context.Context, _ *cloud.Unit) (*checkOut, error) {
	sub, _, err := member(ctx)
	if err != nil {
		return nil, err
	}
	if !allow("github", sub, 2, time.Hour) {
		return nil, errSlow
	}
	out, err := s.mine(ctx, sub)
	return out, refusalGitHub(err)
}

// refusalGitHub is refusal for the operations that read GitHub.
func refusalGitHub(err error) error {
	if errors.Is(err, errBusy) {
		return zip.Errorf(http.StatusServiceUnavailable, "GitHub is not answering right now; try again in a little while")
	}
	return refusal(err)
}

// ── staff ─────────────────────────────────────────────────────────────────────

// adminPostsIn filters the posts staff list.
type adminPostsIn struct {
	// Status is pending, verified, removed or review; empty is every post.
	Status string `json:"status,omitempty" url:"status"`
}

// Lists the posts of every member, newest first, optionally of one status, so staff
// can find one to look at again. SuperAdmin only.
func (s *state) adminPosts(ctx context.Context, in *adminPostsIn) (*postList, error) {
	if err := staffOnly(ctx); err != nil {
		return nil, err
	}
	all, err := s.all(ctx)
	if err != nil {
		return nil, refusal(err)
	}
	out := &postList{Posts: []postView{}}
	for _, p := range all {
		if in.Status != "" && p.Status != in.Status {
			continue
		}
		out.Posts = append(out.Posts, viewOf(p))
		if p.Status == pending {
			out.Pending++
		}
	}
	return out, nil
}

// all is every post, newest first.
func (s *state) all(ctx context.Context) ([]*Post, error) {
	rows, err := s.allPosts(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b *Post) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return rows, nil
}

// Looks at any member's post now, with the same checks as the daily look, and adds
// the look to its history. A removed post that is back up and carries the member's
// link is held again and its week starts over. SuperAdmin only.
//
// Example: {"id": "post:9f8c1d2e"}
func (s *state) adminRecheck(ctx context.Context, in *postRef) (*postView, error) {
	if err := staffOnly(ctx); err != nil {
		return nil, err
	}
	return s.recheck(ctx, strings.TrimSpace(in.ID), cloud.Who(ctx).User, true)
}

// Writes what members earned before the program from the platform's own records,
// once: tokens at 100,000 per point, referred signups, referred orgs with spend,
// verified stars and follows, and bounty awards, each by the one score every board
// shares. The points count toward lifetime points, levels and rank and are never
// redeemable. It is idempotent per person and source, and the trail names who ran
// it. SuperAdmin only.
func (s *state) adminLegacy(ctx context.Context, _ *cloud.Unit) (*legacyOut, error) {
	if err := staffOnly(ctx); err != nil {
		return nil, err
	}
	who := cloud.Who(ctx)
	by := who.Email
	if by == "" {
		by = who.Org + "/" + who.Name
	}
	out, err := s.legacy(ctx, by)
	if err != nil {
		return out, refusal(err)
	}
	return out, nil
}
