package reward

// post.go — a post on another network, proven and held a week.
//
// A member pastes the address of a post of theirs, or asks Hanzo to look through
// their recent public posts on a network. Cloud reads the post itself, from the
// network's public data (apps/reward/social), and wants three things of it: it is
// public, it was written by the member (where the network says by whom and the
// member said who they are), and it carries the member's own referral link.
//
// A post that passes is held PENDING, and pending is not points: it is a claim
// record of this app, not a claim of the rules engine, so it can never inflate a
// board. Every day of its week it is looked at again, and the first look that finds
// it gone, private or without the link marks it REMOVED, which earns nothing. A
// post still up and still carrying the link at the end of the seventh day is stated
// to the rules engine as social.post.verified, with every look it passed as the
// evidence staff read. The engine makes a claim; staff decide it.
//
// A removed post that is back up with the link restarts the week, by the member's
// asking for a re-check or by staff's. A network cloud cannot read (LinkedIn) is
// stated at once as needs_review with no checks, and staff decide it by hand.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/catalog"

	"github.com/hanzoai/cloud/apps/reward/social"
)

// postKind is the post's kind in the store, beside the engine's.
const postKind = "reward-post"

func init() { orm.Register[Post](postKind, orm.WithStringKey[Post]()) }

// A post's states.
const (
	// pending is held for its week and counts for nothing.
	pending = "pending"
	// verified stayed up and carried the link for the week, and was stated to the
	// rules engine, whose claim staff decide.
	verified = "verified"
	// removed was found gone, private, someone else's or without the link.
	removed = "removed"
	// review is on a network that cannot be read, stated for staff to decide by hand.
	review = "review"
)

const (
	// holdFor is how long a post must stay up and carry the link.
	holdFor = 7 * 24 * time.Hour
	// lookEvery is how often a pending post is looked at.
	lookEvery = 24 * time.Hour
	// historyMax bounds the looks a post keeps and states, newest last. A week of
	// daily looks is 8; the rest is room for a member's and staff's re-checks.
	historyMax = 60
)

// Post is one post a member asked to have counted, under an id of the network and
// the post: a post is claimed once, by one person, ever.
type Post struct {
	orm.Model[Post]

	Subject string `json:"subject" orm:"index"`
	Status  string `json:"status" orm:"index"`
	Network string `json:"network"`
	Native  string `json:"key"`
	URL     string `json:"url"`
	// Code is the member's referral code, the ref the post must carry.
	Code string `json:"code"`
	// Handle is who the member said they are on the network, when they said; a
	// post by another author is refused. It is a claim of theirs, not a linked
	// identity, so it can fail the author check (no) and never pass it (unknown).
	Handle  string `json:"handle,omitempty"`
	Week    string `json:"week"`
	Product string `json:"product"`
	Family  string `json:"model"`
	Note    string `json:"note,omitempty"`

	Published time.Time `json:"published,omitzero"`
	// Since is when the post's week began: when it was first found, or last found
	// back up.
	Since time.Time `json:"since"`
	// Next is when it is due its next look.
	Next time.Time `json:"next"`
	// Checks is the history of looks, oldest first.
	Checks []catalog.Check `json:"checks"`
}

// postID is the id of the post at a network under a key: the same post is the same
// row however its address is spelled.
func postID(t social.Target) string {
	sum := sha256.Sum256([]byte(t.Network + "\x00" + t.Key))
	return "post:" + hex.EncodeToString(sum[:16])
}

// weekOf is the ISO year and week of t, such as 2026-W46.
func weekOf(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

// The social package's reader, variables so a test feeds pages.
var (
	locateFn = social.Locate
	readFn   = social.Read
	refFn    = social.Ref
	recentFn = social.Recent
)

// authorMatch compares the post's author to the handle the member gave. The handle
// is a claim of theirs, not an identity IAM linked, so a match proves nothing the
// member could not type and is "unknown", never "yes": the check can refuse a post
// that is by someone else (no) and can never vouch that it is theirs. Staff read the
// author in the post's evidence and decide.
func authorMatch(author, handle string) string {
	author, handle = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(author)), "@"), strings.TrimPrefix(strings.ToLower(strings.TrimSpace(handle)), "@")
	// A Mastodon handle is user@instance and the network states the user alone: the
	// instance is the host the post was read from.
	handle, _, _ = strings.Cut(handle, "@")
	author, _, _ = strings.Cut(author, "@")
	if author != "" && handle != "" && author != handle {
		return catalog.No
	}
	return catalog.Unknown
}

// families are the model families a post may be about; any other topic is a product.
var families = []string{"zen", "enso", "kai"}

// topicOf is what the member's link in the post is about: the topic of a
// /beta/for/<topic> link on hanzo.ai that the post carries direct (a link behind a
// shortener names no topic here), as a product, or a model when it is a family's.
func topicOf(links []string) (product, model string) {
	for _, l := range links {
		u, err := url.Parse(l)
		if err != nil || !(u.Hostname() == "hanzo.ai" || strings.HasSuffix(u.Hostname(), ".hanzo.ai")) {
			continue
		}
		if rest, ok := strings.CutPrefix(u.Path, "/beta/for/"); ok {
			topic, _, _ := strings.Cut(rest, "/")
			if topic = strings.ToLower(strings.TrimSpace(topic)); topic != "" && len(topic) <= 64 {
				if slices.ContainsFunc(families, func(f string) bool { return strings.Contains(topic, f) }) {
					return "", topic
				}
				return topic, ""
			}
		}
	}
	return "", ""
}

// look reads the post now and runs the checks on it. up is whether it is public,
// by the member, and carries the member's link; why says what failed when it did
// not. A network that did not answer is an error, so a busy network never removes a
// post: the look is tried again.
func look(ctx context.Context, p *Post) (c catalog.Check, up bool, why string, links []string, err error) {
	page, err := readFn(ctx, social.Target{Network: p.Network, Key: p.Native, URL: p.URL})
	if err != nil {
		return c, false, "", nil, fmt.Errorf("%w: %v", errBusy, err)
	}
	if page.At.IsZero() {
		page.At = time.Now().UTC()
	}
	c = catalog.Check{FetchedAt: page.At, Status: page.Status, Public: page.Public && !page.Gone, AuthorMatch: authorMatch(page.Author, p.Handle)}
	found := false
	if c.Public {
		if found, err = refFn(ctx, page, p.Code); err != nil {
			return c, false, "", nil, fmt.Errorf("%w: %v", errBusy, err)
		}
	}
	c.RefFound = &found
	switch {
	case page.Gone:
		why = "the post is gone"
	case !c.Public:
		why = "the post is not public"
	case !found:
		why = "the post does not carry your link"
	case c.AuthorMatch == catalog.No:
		why = "the post is by someone else"
	}
	p.Published = cmpTime(p.Published, page.Published)
	return c, why == "", why, page.Links, nil
}

// earlier is the earlier of two times.
func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// cmpTime keeps the first publication time it was given.
func cmpTime(have, got time.Time) time.Time {
	if have.IsZero() {
		return got
	}
	return have
}

// trim keeps the newest historyMax looks.
func trim(c []catalog.Check) []catalog.Check {
	if len(c) > historyMax {
		return slices.Clone(c[len(c)-historyMax:])
	}
	return c
}

// state of a post in the store.
func (s *state) load(ctx context.Context, id string) (*Post, error) {
	p := orm.New[Post](s.db)
	if err := s.db.Get(ctx, s.db.NewKey(orm.Kind[Post](), id, 0, nil), p); err != nil {
		if orm.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	p.SetId(id)
	return p, nil
}

// save writes p, creating it on its first save.
func (s *state) save(ctx context.Context, p *Post, created bool) error {
	now := time.Now().UTC()
	p.UpdatedAt = now
	if created {
		p.CreatedAt = now
		ok, err := s.db.CreateIfAbsent(ctx, p.Key(), p)
		if err == nil && !ok {
			return errTaken
		}
		return err
	}
	return p.PutCtx(ctx)
}

// errTaken is a post another person claimed a moment ago.
var errTaken = errors.New("that post is already claimed")

// verifyEvent is the social.post.verified event for a post that stayed up its week,
// with every look as the evidence.
func verifyEvent(p *Post, last catalog.Check) catalog.Event {
	return catalog.Event{
		ID: "social.post.verified:" + p.Network + ":" + p.Native, Type: "social.post.verified", Subject: p.Subject, At: time.Now().UTC(),
		Fields: map[string]any{"network": p.Network, "url": p.URL, "product": p.Product, "model": p.Family, "week": p.Week},
		Evidence: &catalog.Evidence{Source: p.Network, URL: p.URL, Checks: trim(p.Checks),
			Recheck: &catalog.Recheck{At: last.FetchedAt, Result: catalog.StillUp}},
	}
}

// reviewEvent is the event for a post on a network that cannot be read: stated at
// once, for staff to decide by hand, claiming no check.
func reviewEvent(p *Post) catalog.Event {
	return catalog.Event{
		ID: "social.post.verified:" + p.Network + ":" + p.Native, Type: "social.post.verified", Subject: p.Subject, At: time.Now().UTC(),
		Fields:   map[string]any{"network": p.Network, "url": p.URL, "product": "", "model": "", "week": p.Week},
		Evidence: &catalog.Evidence{Source: p.Network, URL: p.URL, NeedsReview: true},
	}
}

// submit takes a post a member pasted and holds it, or says why it cannot be
// counted. Pasting the same post again answers the first; a post another member
// claimed is refused. who is the member's IAM subject and org the org their
// referral code belongs to.
func (s *state) submit(ctx context.Context, who, org, raw, handle string) (*Post, error) {
	if authz.IsHeldOrg(org) {
		// A held org is one the platform runs for strangers: its referral code is
		// everybody's, so a post carrying it proves nothing about this member.
		return nil, fmt.Errorf("%w: your account has no referral link of its own", errNotCounted)
	}
	t, err := locateFn(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotPost, err)
	}
	id := postID(t)
	if have, err := s.load(ctx, id); err != nil {
		return nil, err
	} else if have != nil {
		if have.Subject != who {
			return nil, errTaken
		}
		if have.Status == review {
			// stated before the row was complete once; stating it again is a no-op.
			if _, err := s.emit(ctx, reviewEvent(have)); err != nil {
				return nil, err
			}
		}
		return have, nil
	}
	p := orm.New[Post](s.db)
	p.SetId(id)
	now := time.Now().UTC()
	p.Subject, p.Network, p.Native, p.URL, p.Handle = who, t.Network, t.Key, t.URL, strings.TrimSpace(handle)
	p.Since, p.Next, p.Week = now, now.Add(lookEvery), weekOf(now)
	if t.Review {
		p.Status = review
		// The row first: the one who creates it is the one who may state it.
		if err := s.save(ctx, p, true); err != nil {
			return nil, err
		}
		if _, err := s.emit(ctx, reviewEvent(p)); err != nil {
			return nil, err
		}
		return p, nil
	}
	if p.Code, err = codeFn(ctx, org); err != nil {
		return nil, fmt.Errorf("%w: %v", errBusy, err)
	}
	c, up, why, links, err := look(ctx, p)
	if err != nil {
		return nil, err
	}
	if !up {
		return nil, fmt.Errorf("%w: %s", errNotCounted, why)
	}
	p.Status, p.Checks = pending, []catalog.Check{c}
	p.Product, p.Family = topicOf(links)
	if !p.Published.IsZero() {
		p.Week = weekOf(p.Published)
	}
	p.Next = earlier(now.Add(lookEvery), p.Since.Add(holdFor))
	return p, s.save(ctx, p, true)
}

// errNotPost is an address that is not a post on a network cloud knows.
var errNotPost = errors.New("that is not the address of a post on a supported network")

// errNotCounted is a post that cannot be counted as it stands.
var errNotCounted = errors.New("that post cannot be counted")

// step looks at p now, appends the look to its history and moves it on: removed if
// it fails, verified and stated if it passed its week, else held. A removed post
// back up restarts its week. It saves p, except when the network did not answer,
// which leaves p as it was to be looked at again.
func (s *state) step(ctx context.Context, p *Post) error {
	c, up, why, _, err := look(ctx, p)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	p.Checks = trim(append(p.Checks, c))
	switch {
	case p.Status == verified:
		// Stated already; the look is only the record of how it stands now.
	case !up:
		p.Status, p.Note = removed, why
	case p.Status == removed:
		p.Status, p.Note, p.Since = pending, "back up; the week starts again", now
		p.Next = earlier(now.Add(lookEvery), p.Since.Add(holdFor))
	case !now.Before(p.Since.Add(holdFor)):
		if _, err := s.emit(ctx, verifyEvent(p, c)); err != nil {
			return err
		}
		p.Status, p.Note = verified, ""
	default:
		p.Next = earlier(now.Add(lookEvery), p.Since.Add(holdFor))
	}
	return s.save(ctx, p, false)
}

// due is the pending posts whose next look has come.
func (s *state) due(ctx context.Context, now time.Time) ([]*Post, error) {
	rows, err := orm.TypedQuery[Post](s.db).Filter("Status=", pending).GetAll(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(rows, func(p *Post) bool { return p.Next.After(now) }), nil
}

// lookBudget bounds the looks of one pass of the daily loop.
const lookBudget = 5 * time.Minute

// sweepPosts looks at every pending post that is due. A post whose network did
// not answer is looked at on the next pass; one failing never stops the rest.
func (s *state) sweepPosts(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, lookBudget)
	defer cancel()
	rows, err := s.due(ctx, time.Now().UTC())
	if err != nil {
		s.base.Log.Error("reward: the pending posts could not be read", "err", err)
		return
	}
	var held, gone, stated int
	for _, p := range rows {
		if ctx.Err() != nil {
			break
		}
		if err := s.step(ctx, p); err != nil {
			s.base.Log.Warn("reward: a post could not be looked at; it is tried again", "post", p.URL, "err", err)
			continue
		}
		switch p.Status {
		case removed:
			gone++
		case verified:
			stated++
		default:
			held++
		}
	}
	if len(rows) > 0 {
		s.base.Log.Info("reward: pending posts looked at", "due", len(rows), "held", held, "removed", gone, "stated", stated)
	}
}

// postsOf is a member's posts, newest first.
func (s *state) postsOf(ctx context.Context, who string) ([]*Post, error) {
	rows, err := orm.TypedQuery[Post](s.db).Filter("Subject=", who).GetAll(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b *Post) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return rows, nil
}

// allPosts is every post in the store.
func (s *state) allPosts(ctx context.Context) ([]*Post, error) {
	return orm.TypedQuery[Post](s.db).GetAll(ctx)
}

// errRefusedNetwork is the network a scan cannot read.
var errRefusedNetwork = social.ErrRefused
