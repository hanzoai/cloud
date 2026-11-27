package reward

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/rule"

	"github.com/hanzoai/cloud/apps/reward/social"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// network is a fake of the networks the posts are on: the page each address reads as.
type network struct {
	mu    sync.Mutex
	pages map[string]social.Page
	down  bool
}

const withLink = "https://hanzo.ai/beta/for/zen?ref=ABCD"

func up(author string, published time.Time) social.Page {
	return social.Page{Status: 200, At: time.Now().UTC(), Public: true, Author: author, Links: []string{withLink}, Published: published}
}

func (n *network) set(url string, p social.Page) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pages[url] = p
}

func (n *network) read(_ context.Context, t social.Target) (social.Page, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.down {
		return social.Page{}, errors.New("the network is not answering")
	}
	if p, ok := n.pages[t.URL]; ok {
		p.At = time.Now().UTC()
		return p, nil
	}
	return social.Page{Status: 404, At: time.Now().UTC(), Gone: true}, nil
}

// posts wires the fakes: the networks, the member's code and the link check.
func (r *rig) posts() *network {
	n := &network{pages: map[string]social.Page{}}
	stub(r.t, &readFn, n.read)
	stub(r.t, &refFn, func(_ context.Context, p social.Page, code string) (bool, error) {
		return code == "ABCD" && len(p.Links) > 0, nil
	})
	stub(r.t, &codeFn, func(context.Context, string) (string, error) { return "ABCD", nil })
	return n
}

// weekly turns the staff-priced Weekly share rule on, as an admin would.
func (r *rig) weekly() {
	r.t.Helper()
	p, err := r.s.eng.Program(context.Background(), program)
	if err != nil {
		r.t.Fatal(err)
	}
	w := *p.Find("weekly-share")
	w.Active, w.Then = true, []rule.Action{{Type: rule.Do.Points, Points: 5}}
	if _, err := r.s.eng.SaveRule(context.Background(), program, w, "staff"); err != nil {
		r.t.Fatal(err)
	}
}

// age moves a post's week back by d and makes it due.
func (r *rig) age(id string, d time.Duration) {
	r.t.Helper()
	ctx := context.Background()
	p, err := r.s.load(ctx, id)
	if err != nil || p == nil {
		r.t.Fatalf("load %s: %v", id, err)
	}
	p.Since, p.Next = p.Since.Add(-d), time.Now().Add(-time.Minute)
	if err := r.s.save(ctx, p, false); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) post(id string) *Post {
	r.t.Helper()
	p, err := r.s.load(context.Background(), id)
	if err != nil || p == nil {
		r.t.Fatalf("load %s: %v", id, err)
	}
	return p
}

const bsky = "https://bsky.app/profile/ann.bsky.social/post/3kaaaaaa"

func TestAPostIsPendingNotPointsAndTheSameSubmissionIsOne(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		n := r.posts()
		n.set(bsky, up("ann.bsky.social", time.Time{}))
		ctx := context.Background()
		p, err := r.s.submit(ctx, "sub-ann", "acme", bsky, "ann.bsky.social")
		if err != nil {
			t.Fatal(err)
		}
		v := viewOf(p)
		if p.Status != pending || v.CountsAt == nil || v.DaysLeft != 7 || len(v.Checks) != 1 || v.Checks[0].AuthorMatch != "unknown" || v.Checks[0].RefFound == nil || !*v.Checks[0].RefFound {
			t.Fatalf("view = %+v", v)
		}
		if p.Product != "" || p.Family != "zen" {
			t.Errorf("topic = product %q, model %q, want the zen family from the link", p.Product, p.Family)
		}
		// pending is a claim record of this app: it is nowhere in the rules engine.
		if got := r.claims("sub-ann"); len(got) != 0 {
			t.Errorf("a pending post made %d claims", len(got))
		}
		again, err := r.s.submit(ctx, "sub-ann", "acme", bsky, "")
		if err != nil || again.Id() != p.Id() {
			t.Errorf("pasting it again: %v", err)
		}
		if _, err := r.s.submit(ctx, "sub-bob", "acme", bsky, ""); !errors.Is(err, errTaken) {
			t.Errorf("another member's claim of the same post: %v, want errTaken", err)
		}

	})
}

func TestAPostThatIsNotPublicNotTheMembersOrWithoutTheLinkIsNotHeld(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		n := r.posts()
		ctx := context.Background()
		// gone
		if _, err := r.s.submit(ctx, "sub-ann", "acme", bsky, ""); !errors.Is(err, errNotCounted) {
			t.Errorf("gone: %v", err)
		}
		// private
		n.set(bsky, social.Page{Status: 200, At: time.Now(), Public: false})
		if _, err := r.s.submit(ctx, "sub-ann", "acme", bsky, ""); !errors.Is(err, errNotCounted) {
			t.Errorf("private: %v", err)
		}
		// no link
		n.set(bsky, social.Page{Status: 200, At: time.Now(), Public: true, Author: "ann.bsky.social"})
		if _, err := r.s.submit(ctx, "sub-ann", "acme", bsky, ""); !errors.Is(err, errNotCounted) {
			t.Errorf("no link: %v", err)
		}
		// someone else's, by the handle the member gave
		n.set(bsky, up("mallory.bsky.social", time.Time{}))
		if _, err := r.s.submit(ctx, "sub-ann", "acme", bsky, "ann.bsky.social"); !errors.Is(err, errNotCounted) {
			t.Errorf("someone else's: %v", err)
		}
		// the network is down: not a refusal of the post, a try again
		n.down = true
		if _, err := r.s.submit(ctx, "sub-ann", "acme", bsky, ""); !errors.Is(err, errBusy) {
			t.Errorf("down: %v", err)
		}
		if rows, _ := r.s.postsOf(ctx, "sub-ann"); len(rows) != 0 {
			t.Errorf("a post that could not be held was stored: %d", len(rows))
		}

	})
}

func TestADailyCheckThatFindsRemovalMidWeekRemovesItAndItEarnsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		n := r.posts()
		r.weekly()
		n.set(bsky, up("ann.bsky.social", time.Time{}))
		ctx := context.Background()
		p, err := r.s.submit(ctx, "sub-ann", "acme", bsky, "")
		if err != nil {
			t.Fatal(err)
		}
		// day 3: still up, the look is added to the history
		r.age(p.Id(), 3*24*time.Hour)
		r.s.sweepPosts(ctx)
		if got := r.post(p.Id()); got.Status != pending || len(got.Checks) != 2 {
			t.Fatalf("day 3: %s with %d checks", got.Status, len(got.Checks))
		}
		// day 4: the post is deleted
		n.mu.Lock()
		delete(n.pages, bsky)
		n.mu.Unlock()
		r.age(p.Id(), 24*time.Hour)
		r.s.sweepPosts(ctx)
		got := r.post(p.Id())
		if got.Status != removed || got.Note != "the post is gone" || len(got.Checks) != 3 || got.Checks[2].Public {
			t.Fatalf("day 4: %s %q %+v", got.Status, got.Note, got.Checks)
		}
		// and it never reaches the engine, however late the sweep runs again
		r.age(p.Id(), 5*24*time.Hour)
		r.s.sweepPosts(ctx)
		if len(r.claims("sub-ann")) != 0 {
			t.Errorf("a removed post earned %d claims", len(r.claims("sub-ann")))
		}
		if r.post(p.Id()).Status != removed {
			t.Error("a removed post moved on its own")
		}

	})
}

func TestARecheckOfARemovedPostThatIsBackUpRestartsTheWeek(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		n := r.posts()
		n.set(bsky, up("ann.bsky.social", time.Time{}))
		ctx := context.Background()
		p, _ := r.s.submit(ctx, "sub-ann", "acme", bsky, "")
		n.mu.Lock()
		delete(n.pages, bsky)
		n.mu.Unlock()
		r.age(p.Id(), 4*24*time.Hour)
		r.s.sweepPosts(ctx)
		if r.post(p.Id()).Status != removed {
			t.Fatal("not removed")
		}
		// still gone: a recheck records the look and stays removed
		v, err := r.s.recheck(ctx, p.Id(), "sub-ann", false)
		if err != nil || v.Status != removed || len(v.Checks) != 3 {
			t.Fatalf("recheck while gone: %+v %v", v, err)
		}
		// back up with the link: pending again, the week starts over, the history is kept whole
		n.set(bsky, up("ann.bsky.social", time.Time{}))
		v, err = r.s.recheck(ctx, p.Id(), "sub-ann", false)
		if err != nil || v.Status != pending || len(v.Checks) != 4 || v.DaysLeft != 7 {
			t.Fatalf("recheck once back: %+v %v", v, err)
		}
		if got := r.post(p.Id()); time.Since(got.Since) > time.Minute || got.Note == "" {
			t.Errorf("the week did not restart: since %v", got.Since)
		}
		// someone else's post is not theirs to look at; staff may
		if _, err := r.s.recheck(ctx, p.Id(), "sub-bob", false); err == nil {
			t.Error("a member re-checked another member's post")
		}
		if v, err = r.s.recheck(ctx, p.Id(), "staff", true); err != nil || len(v.Checks) != 5 {
			t.Errorf("staff recheck: %+v %v", v, err)
		}

	})
}

func TestAPostStillUpAtTheEndOfItsWeekIsStatedWithEveryLookAsEvidence(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		n := r.posts()
		r.weekly()
		n.set(bsky, up("ann.bsky.social", time.Time{}))
		ctx := context.Background()
		p, _ := r.s.submit(ctx, "sub-ann", "acme", bsky, "ann.bsky.social")
		for day := 1; day <= 6; day++ {
			r.age(p.Id(), 24*time.Hour)
			r.s.sweepPosts(ctx)
		}
		if got := r.post(p.Id()); got.Status != pending || len(got.Checks) != 7 || len(r.claims("sub-ann")) != 0 {
			t.Fatalf("day 6: %s, %d checks, %d claims", got.Status, len(got.Checks), len(r.claims("sub-ann")))
		}
		r.age(p.Id(), 24*time.Hour)
		r.s.sweepPosts(ctx)
		if got := r.post(p.Id()); got.Status != verified {
			t.Fatalf("day 7: %s", got.Status)
		}
		cs := r.claimsOf("sub-ann", "social.post.verified")
		if len(cs) != 1 || cs[0].Rule != "weekly-share" {
			t.Fatalf("claims = %+v", cs)
		}
		ev := cs[0].Evidence
		if ev == nil || ev.Source != "bluesky" || ev.URL != "https://bsky.app/profile/ann.bsky.social/post/3kaaaaaa" || ev.NeedsReview || len(ev.Checks) != 8 {
			t.Fatalf("evidence = %+v", ev)
		}
		if ev.Recheck == nil || ev.Recheck.Result != "ok" || ev.Recheck.At.IsZero() {
			t.Errorf("recheck = %+v", ev.Recheck)
		}
		for i, c := range ev.Checks {
			if c.Status != 200 || !c.Public || c.RefFound == nil || !*c.RefFound || c.AuthorMatch != "unknown" {
				t.Errorf("check %d = %+v", i, c)
			}
		}
		// a later sweep claims nothing more
		r.s.sweepPosts(ctx)
		if len(r.claimsOf("sub-ann", "social.post.verified")) != 1 {
			t.Error("a second claim")
		}

	})
}

func TestTheWeekKeyAllowsOneAwardPerNetworkPerWeek(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		n := r.posts()
		r.weekly()
		ctx := context.Background()
		w46 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) // 2026-W46
		w47 := w46.Add(7 * 24 * time.Hour)
		if weekOf(w46) != "2026-W46" || weekOf(w47) != "2026-W47" {
			t.Fatalf("weeks = %s %s", weekOf(w46), weekOf(w47))
		}
		posts := []struct {
			url, who string
			at       time.Time
		}{
			{"https://bsky.app/profile/ann.bsky.social/post/3kabc001", "bluesky1", w46},
			{"https://bsky.app/profile/ann.bsky.social/post/3kabc002", "bluesky2", w46.Add(48 * time.Hour)},
			{"https://bsky.app/profile/ann.bsky.social/post/3kabc003", "bluesky3", w47},
			{"https://news.ycombinator.com/item?id=4242", "hn", w46},
		}
		var ids []string
		for _, p := range posts {
			n.set(p.url, up("ann", p.at))
			x, err := r.s.submit(ctx, "sub-ann", "acme", p.url, "")
			if err != nil {
				t.Fatalf("%s: %v", p.who, err)
			}
			ids = append(ids, x.Id())
			r.age(x.Id(), 8*24*time.Hour)
		}
		r.s.sweepPosts(ctx)
		for _, id := range ids {
			if r.post(id).Status != verified {
				t.Fatalf("%s: %s", id, r.post(id).Status)
			}
		}
		// four posts, four events; the second bluesky post in W46 is withheld by the week key.
		got := map[string]bool{}
		for _, c := range r.claimsOf("sub-ann", "social.post.verified") {
			got[c.Slot] = true
		}
		if len(got) != 3 || !got["bluesky+2026-W46"] || !got["bluesky+2026-W47"] || !got["hn+2026-W46"] {
			t.Errorf("claims by network+week = %v", got)
		}

	})
}

func TestLinkedInIsStatedAtOnceForReviewClaimingNoChecks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.posts()
		r.weekly()
		ctx := context.Background()
		p, err := r.s.submit(ctx, "sub-ann", "acme", "https://www.linkedin.com/posts/ann-example_hello-activity-7000000000000000000-AbCd", "")
		if err != nil || p.Status != review {
			t.Fatalf("submit: %+v %v", p, err)
		}
		cs := r.claimsOf("sub-ann", "social.post.verified")
		if len(cs) != 1 || cs[0].Evidence == nil || !cs[0].Evidence.NeedsReview || len(cs[0].Evidence.Checks) != 0 || cs[0].Evidence.Source != "linkedin" {
			t.Fatalf("claims = %+v", cs)
		}
		if _, err := r.s.recheck(ctx, p.Id(), "sub-ann", false); err == nil {
			t.Error("a post that cannot be read was re-checked")
		}

	})
}

func TestThePostRoutesWorkOverHTTPAndTheRecheckIsRateLimited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		n := r.posts()
		n.set(bsky, up("ann.bsky.social", time.Time{}))
		h := memberHeaders("sub-http", "acme")
		code, body := r.do(http.MethodPost, "/v1/reward/me/posts", h, map[string]any{"url": bsky})
		if code != http.StatusCreated {
			t.Fatalf("submit = %d %s", code, body)
		}
		var v postView
		if err := json.Unmarshal(body, &v); err != nil || v.Status != pending || v.ID == "" {
			t.Fatalf("view = %+v %v", v, err)
		}
		if code, _ = r.do(http.MethodPost, "/v1/reward/me/posts", h, map[string]any{"url": "https://example.com/x"}); code != http.StatusBadRequest {
			t.Errorf("not a post = %d", code)
		}
		if code, body = r.do(http.MethodGet, "/v1/reward/me/posts", h, nil); code != http.StatusOK {
			t.Fatalf("list = %d %s", code, body)
		}
		var list postList
		if err := json.Unmarshal(body, &list); err != nil || len(list.Posts) != 1 || list.Pending != 1 {
			t.Errorf("list = %+v %v", list, err)
		}
		// ten re-checks an hour, then the eleventh is asked to wait
		var last int
		for range 11 {
			last, _ = r.do(http.MethodPost, "/v1/reward/me/posts/"+v.ID+"/recheck", h, map[string]any{})
		}
		if last != http.StatusTooManyRequests {
			t.Errorf("the eleventh re-check = %d, want 429", last)
		}
		// staff are not limited, and see any member's post
		if code, body = r.do(http.MethodPost, "/v1/admin/reward/posts/"+v.ID+"/recheck", platformHeaders, map[string]any{}); code != http.StatusOK {
			t.Errorf("staff re-check = %d %s", code, body)
		}
		// someone else is told there is no such post
		if code, _ = r.do(http.MethodPost, "/v1/reward/me/posts/"+v.ID+"/recheck", memberHeaders("sub-other", "acme"), map[string]any{}); code != http.StatusNotFound {
			t.Errorf("another member's re-check = %d, want 404", code)
		}

	})
}

func TestAScanHoldsTheRecentPostsThatCarryTheMembersLink(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		n := r.posts()
		a, b, c := "https://bsky.app/profile/scan.bsky.social/post/3kabcaaa", "https://bsky.app/profile/scan.bsky.social/post/3kabcbbb", "https://bsky.app/profile/scan.bsky.social/post/3kabcccc"
		n.set(a, up("scan.bsky.social", time.Time{}))
		n.set(b, social.Page{Status: 200, At: time.Now(), Public: true, Author: "scan.bsky.social"})
		n.set(c, up("scan.bsky.social", time.Time{}))
		stub(t, &recentFn, func(_ context.Context, network, handle string) ([]string, error) {
			if network == "x" {
				return nil, social.ErrRefused
			}
			return []string{a, b, c}, nil
		})
		// The scan limit counts per member across the process, and this body runs once
		// per engine: each run is a member of its own.
		h := memberHeaders("sub-scan-"+strings.ReplaceAll(t.Name(), "/", "-"), "acme")
		code, body := r.do(http.MethodPost, "/v1/reward/me/posts/scan", h, map[string]any{"network": "bluesky", "handle": "scan.bsky.social"})
		var out scanOut
		if err := json.Unmarshal(body, &out); code != http.StatusOK || err != nil || out.Looked != 3 || out.Held != 2 || out.Skipped != 1 {
			t.Fatalf("scan = %d %s", code, body)
		}
		if code, _ = r.do(http.MethodPost, "/v1/reward/me/posts/scan", h, map[string]any{"network": "x", "handle": "scan"}); code != http.StatusBadRequest {
			t.Errorf("scan of X = %d, want 400: X cannot be read by handle", code)
		}

	})
}

func TestAMemberOfAHeldOrgHasNoLinkOfTheirOwnSoNoPostIsHeld(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		n := r.posts()
		n.set(bsky, up("ann.bsky.social", time.Time{}))
		if _, err := r.s.submit(context.Background(), "sub-staff", "admin", bsky, ""); !errors.Is(err, errNotCounted) {
			t.Errorf("a post from a held org: %v, want errNotCounted", err)
		}
		if rows, _ := r.s.postsOf(context.Background(), "sub-staff"); len(rows) != 0 {
			t.Errorf("held: %d", len(rows))
		}

	})
}

func TestAnAPIKeyOrAnApplicationIsNotAPersonToHoldAPost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		n := r.posts()
		n.set(bsky, up("ann.bsky.social", time.Time{}))
		for _, who := range []string{"acme/ann", "app:build-bot"} {
			code, body := r.do(http.MethodPost, "/v1/reward/me/posts", memberHeaders(who, "acme"), map[string]any{"url": bsky})
			if code != http.StatusForbidden {
				t.Errorf("%s held a post: %d %s", who, code, body)
			}
		}

	})
}

func TestAReviewPostIsStatedAgainWhenItsOwnerPastesItAgainAndNeverForAnotherMember(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.posts()
		ctx := context.Background()
		const url = "https://www.linkedin.com/posts/ann-example_hello-activity-7000000000000000001-AbCd"
		p, err := r.s.submit(ctx, "sub-ann", "acme", url, "")
		if err != nil || p.Status != review {
			t.Fatalf("submit: %v", err)
		}
		if _, err := r.s.submit(ctx, "sub-bob", "acme", url, ""); !errors.Is(err, errTaken) {
			t.Errorf("another member's claim of the same post: %v", err)
		}
		again, err := r.s.submit(ctx, "sub-ann", "acme", url, "")
		if err != nil || again.Id() != p.Id() {
			t.Errorf("pasting it again: %v", err)
		}
		if n := len(r.claimsOf("sub-ann", "social.post.verified")); n != 0 && n != 1 {
			t.Errorf("claims = %d", n)
		}

	})
}

func TestAMastodonHandleMatchesTheUserTheNetworkStates(t *testing.T) {
	for handle, want := range map[string]string{
		"alice@mastodon.social": catalog.Unknown, "@alice": catalog.Unknown, "bob@mastodon.social": catalog.No, "": catalog.Unknown,
	} {
		if got := authorMatch("alice", handle); got != want {
			t.Errorf("handle %q = %s, want %s", handle, got, want)
		}
	}
}
