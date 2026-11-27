package social

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const (
	bskyFeed = "https://public.api.bsky.app/xrpc/app.bsky.feed.getAuthorFeed?actor=ada.bsky.social&filter=posts_no_replies&limit=25"
	mastoAcc = "https://hachyderm.io/api/v1/accounts/lookup?acct=ada"
	mastoSts = "https://hachyderm.io/api/v1/accounts/109/statuses?exclude_reblogs=true&exclude_replies=true&limit=25"
	redSub   = "https://www.reddit.com/user/ada_1/submitted.json?raw_json=1&sort=new&limit=25"
	hnUsr    = "https://hacker-news.firebaseio.com/v0/user/ada.json"
)

// feedEntry is one entry of an author feed.
func feedEntry(handle, rkey, indexed, reason string) map[string]any {
	e := map[string]any{"post": map[string]any{
		"uri": "at://did:plc:abcdefghijklmnopqrstuvwx/app.bsky.feed.post/" + rkey, "indexedAt": indexed,
		"author": map[string]any{"handle": handle}, "record": map[string]any{"createdAt": indexed},
	}}
	if reason != "" {
		e["reason"] = map[string]any{"$type": reason}
	}
	return e
}

// recentIsReadable: every address Recent returns is one Locate makes of itself.
func recentIsReadable(t *testing.T, network string, urls []string) {
	t.Helper()
	for _, u := range urls {
		got, err := Locate(u)
		if err != nil || got.URL != u || got.Network != network || got.Review {
			t.Errorf("%s: Locate(%s) = %+v %v", network, u, got, err)
		}
	}
}

// TestRecentBlueskyListsOriginalPostsNewestFirst: no reposts, no posts of another
// author, no repeats; a pinned post sits where it was written, not at the top.
func TestRecentBlueskyListsOriginalPostsNewestFirst(t *testing.T) {
	p := stand(t)
	p.m[bskyFeed] = ok(map[string]any{"feed": []any{
		feedEntry("ada.bsky.social", "3kbbbbbbbbb22", "2026-09-01T00:00:00Z", "app.bsky.feed.defs#reasonPin"),
		feedEntry("ada.bsky.social", "3kaaaaaaaaa22", "2026-10-26T12:00:00Z", ""),
		feedEntry("eve.bsky.social", "3keeeeeeeee22", "2026-10-27T12:00:00Z", "app.bsky.feed.defs#reasonRepost"),
		feedEntry("eve.bsky.social", "3kfffffffff22", "2026-10-27T13:00:00Z", ""),
		feedEntry("ada.bsky.social", "3kccccccccc22", "2026-10-27T00:00:00Z", ""),
		feedEntry("ada.bsky.social", "3kaaaaaaaaa22", "2026-10-26T12:00:00Z", ""),
		feedEntry("ada.bsky.social", "x", "2026-10-28T00:00:00Z", ""),
	}})
	for _, handle := range []string{"ada.bsky.social", "@Ada.Bsky.Social"} {
		got, err := Recent(context.Background(), "bluesky", handle)
		want := []string{
			"https://bsky.app/profile/ada.bsky.social/post/3kccccccccc22",
			"https://bsky.app/profile/ada.bsky.social/post/3kaaaaaaaaa22",
			"https://bsky.app/profile/ada.bsky.social/post/3kbbbbbbbbb22",
		}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("Recent(%q) = %q %v, want %q", handle, got, err, want)
		}
		recentIsReadable(t, "bluesky", got)
	}
	// An account the network does not know has no posts.
	p.m[bskyFeed] = reply{Status: 400}
	if got, err := Recent(context.Background(), "bluesky", "ada.bsky.social"); err != nil || len(got) != 0 {
		t.Fatalf("an unknown account = %q %v", got, err)
	}
}

// TestRecentMastodonListsPublicStatusesWithoutBoosts: the account is looked up on
// the instance of the handle, its statuses read by that account's id.
func TestRecentMastodonListsPublicStatusesWithoutBoosts(t *testing.T) {
	p := stand(t)
	p.m[mastoAcc] = ok(map[string]any{"id": "109", "acct": "ada", "locked": false})
	p.m[mastoSts] = ok([]any{
		map[string]any{"id": "300", "visibility": "public", "reblog": nil},
		map[string]any{"id": "299", "visibility": "public", "reblog": map[string]any{"id": "5"}},
		map[string]any{"id": "298", "visibility": "private", "reblog": nil},
		map[string]any{"id": "297", "visibility": "unlisted", "reblog": nil},
		map[string]any{"id": "296", "visibility": "direct", "reblog": nil},
		map[string]any{"id": "../x", "visibility": "public", "reblog": nil},
		map[string]any{"id": "0295", "visibility": "public", "reblog": nil},
		map[string]any{"id": "294", "visibility": "public"},
	})
	for _, handle := range []string{"ada@hachyderm.io", "@Ada@Hachyderm.IO"} {
		got, err := Recent(context.Background(), "mastodon", handle)
		want := []string{"https://hachyderm.io/@ada/300", "https://hachyderm.io/@ada/297", "https://hachyderm.io/@ada/294"}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("Recent(%q) = %q %v, want %q", handle, got, err, want)
		}
		recentIsReadable(t, "mastodon", got)
	}
	// A locked account lists nothing; an unknown one has no posts.
	p.m[mastoAcc] = ok(map[string]any{"id": "109", "acct": "ada", "locked": true})
	if got, err := Recent(context.Background(), "mastodon", "ada@hachyderm.io"); err != nil || len(got) != 0 {
		t.Fatalf("a locked account = %q %v", got, err)
	}
	p.m[mastoAcc] = reply{Status: 404}
	if got, err := Recent(context.Background(), "mastodon", "ada@hachyderm.io"); err != nil || len(got) != 0 {
		t.Fatalf("an unknown account = %q %v", got, err)
	}
	// An account id that is not a number is never put into an address.
	p.m[mastoAcc] = ok(map[string]any{"id": "../../admin", "acct": "ada"})
	calls := len(p.calls)
	if got, err := Recent(context.Background(), "mastodon", "ada@hachyderm.io"); err == nil || len(got) != 0 || len(p.calls) != calls+1 {
		t.Fatalf("a hostile account id = %q %v after %v", got, err, p.calls[calls:])
	}
}

// TestRecentRedditListsSubmittedPosts: posts only (kind t3), the user's own, not
// removed, each as the address Locate makes of it.
func TestRecentRedditListsSubmittedPosts(t *testing.T) {
	p := stand(t)
	p.m[redSub] = raw(`{"data":{"children":[
		{"kind":"t3","data":{"id":"1abc2de","subreddit":"LocalLLaMA","author":"Ada_1"}},
		{"kind":"t1","data":{"id":"cmt1234","subreddit":"x1","author":"ada_1"}},
		{"kind":"t3","data":{"id":"1abc2df","subreddit":"x1","author":"ada_1","removed_by_category":"moderator"}},
		{"kind":"t3","data":{"id":"1abc2dg","subreddit":"x1","author":"eve"}},
		{"kind":"t3","data":{"id":"../x","subreddit":"x1","author":"ada_1"}},
		{"kind":"t3","data":{"id":"1abc2dh","subreddit":"a/b","author":"ada_1"}},
		{"kind":"t3","data":{"id":"1abc2di","subreddit":"x1","author":"ada_1","removed_by_category":null}}
	]}}`)
	got, err := Recent(context.Background(), "reddit", "ada_1")
	want := []string{"https://www.reddit.com/r/localllama/comments/1abc2de/", "https://www.reddit.com/r/x1/comments/1abc2di/"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("Recent = %q %v, want %q", got, err, want)
	}
	recentIsReadable(t, "reddit", got)
	p.m[redSub] = reply{Status: 404}
	if got, err := Recent(context.Background(), "reddit", "ada_1"); err != nil || len(got) != 0 {
		t.Fatalf("an unknown user = %q %v", got, err)
	}
}

// TestRecentHackerNewsListsStoriesAndComments: the user's submitted ids, newest
// first, each looked up; jobs, polls, deleted and dead items are left out, and no
// more than 25 are looked at.
func TestRecentHackerNewsListsStoriesAndComments(t *testing.T) {
	p := stand(t)
	ids := []int64{900, 899, 898, 897, 896, 895}
	p.m[hnUsr] = ok(map[string]any{"id": "ada", "submitted": ids})
	item := func(id int64, v map[string]any) {
		v["id"] = id
		p.m[fmt.Sprintf("https://hacker-news.firebaseio.com/v0/item/%d.json", id)] = ok(v)
	}
	item(900, map[string]any{"by": "ada", "type": "story"})
	item(899, map[string]any{"by": "ada", "type": "comment"})
	item(898, map[string]any{"by": "ada", "type": "story", "dead": true})
	item(897, map[string]any{"by": "ada", "type": "comment", "deleted": true})
	item(896, map[string]any{"by": "ada", "type": "job"})
	p.m["https://hacker-news.firebaseio.com/v0/item/895.json"] = raw("null")
	got, err := Recent(context.Background(), "hn", "ada")
	want := []string{"https://news.ycombinator.com/item?id=900", "https://news.ycombinator.com/item?id=899"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("Recent = %q %v, want %q", got, err, want)
	}
	recentIsReadable(t, "hn", got)

	// Thirty ids submitted: 25 are looked at, so no more than 25 come back.
	p = stand(t)
	var many []int64
	for i := range 30 {
		id := int64(1000 - i)
		many = append(many, id)
		p.m[fmt.Sprintf("https://hacker-news.firebaseio.com/v0/item/%d.json", id)] = ok(map[string]any{"id": id, "by": "ada", "type": "story"})
	}
	p.m[hnUsr] = ok(map[string]any{"submitted": many})
	got, err = Recent(context.Background(), "hn", "ada")
	if err != nil || len(got) != 25 || got[0] != "https://news.ycombinator.com/item?id=1000" || len(p.calls) != 26 {
		t.Fatalf("thirty submitted = %d urls %v after %d fetches", len(got), err, len(p.calls))
	}
	// An unknown user is null; an item that fails to load is an error to retry.
	p = stand(t)
	p.m[hnUsr] = raw("null")
	if got, err := Recent(context.Background(), "hn", "ada"); err != nil || len(got) != 0 {
		t.Fatalf("an unknown user = %q %v", got, err)
	}
	p.m[hnUsr] = ok(map[string]any{"submitted": []int64{5}})
	p.err["https://hacker-news.firebaseio.com/v0/item/5.json"] = errors.New("connection reset")
	if got, err := Recent(context.Background(), "hn", "ada"); err == nil || len(got) != 0 {
		t.Fatalf("an item that failed = %q %v", got, err)
	}
}

// TestRecentListsAtMost25: a network that answers more is cut at 25.
func TestRecentListsAtMost25(t *testing.T) {
	p := stand(t)
	var feed, sts, kids []any
	for i := range 40 {
		feed = append(feed, feedEntry("ada.bsky.social", fmt.Sprintf("3k%011d", i), fmt.Sprintf("2026-10-%02dT00:00:00Z", 1+i%28), ""))
		sts = append(sts, map[string]any{"id": fmt.Sprint(1000 + i), "visibility": "public"})
		kids = append(kids, map[string]any{"kind": "t3", "data": map[string]any{"id": fmt.Sprintf("a%04d", i), "subreddit": "x1", "author": "ada_1"}})
	}
	p.m[bskyFeed] = ok(map[string]any{"feed": feed})
	p.m[mastoAcc] = ok(map[string]any{"id": "109"})
	p.m[mastoSts] = ok(sts)
	p.m[redSub] = ok(map[string]any{"data": map[string]any{"children": kids}})
	for network, handle := range map[string]string{"bluesky": "ada.bsky.social", "mastodon": "ada@hachyderm.io", "reddit": "ada_1"} {
		got, err := Recent(context.Background(), network, handle)
		if err != nil || len(got) != 25 {
			t.Errorf("%s = %d urls %v, want 25", network, len(got), err)
		}
	}
}

// TestRecentSaysWhenTheNetworkCouldNotBeRead: a transport failure or a status
// that says nothing about the account is an error to try again later, never an
// empty list.
func TestRecentSaysWhenTheNetworkCouldNotBeRead(t *testing.T) {
	for network, tc := range map[string]struct{ handle, first string }{
		"bluesky":  {"ada.bsky.social", bskyFeed},
		"mastodon": {"ada@hachyderm.io", mastoAcc},
		"reddit":   {"ada_1", redSub},
		"hn":       {"ada", hnUsr},
		"devto":    {"ada_1", devtoList},
	} {
		for _, status := range []int{429, 500, 503, 403, 401} {
			p := stand(t)
			p.m[tc.first] = reply{Status: status}
			if got, err := Recent(context.Background(), network, tc.handle); err == nil || errors.Is(err, ErrRefused) {
				t.Errorf("%s with %d = %q %v, want an error to retry", network, status, got, err)
			}
		}
		p := stand(t)
		p.err[tc.first] = errors.New("tls: handshake failure")
		if got, err := Recent(context.Background(), network, tc.handle); err == nil || errors.Is(err, ErrRefused) {
			t.Errorf("%s, a transport failure = %q %v", network, got, err)
		}
		delete(p.err, tc.first)
		p.m[tc.first] = raw("<html>")
		if got, err := Recent(context.Background(), network, tc.handle); err == nil {
			t.Errorf("%s, an answer that is not JSON = %q %v", network, got, err)
		}
	}
}

// TestRecentRefusesTheReviewedNetworksAndUnknownOnes: X, LinkedIn, TikTok, YouTube,
// Threads, Instagram and Facebook are confirmed by a maintainer, Medium and
// Hashnode have no list, so each is an error and nothing is fetched.
func TestRecentRefusesTheReviewedNetworksAndUnknownOnes(t *testing.T) {
	p := stand(t)
	for _, network := range []string{"x", "linkedin", "facebook", "twitter", "hackernews", "", "BLUESKY", "tiktok", "medium", "hashnode", "youtube", "threads", "instagram", "dev.to", "DEVTO"} {
		got, err := Recent(context.Background(), network, "ada")
		if !errors.Is(err, ErrRefused) || got != nil {
			t.Errorf("Recent(%q) = %q %v, want a refusal", network, got, err)
		}
	}
	if len(p.calls) != 0 {
		t.Fatalf("fetched %v", p.calls)
	}
}

// TestRecentRefusesAHandleThatCouldReshapeTheAddress: the handle goes into a URL
// path or query, so anything that is not the strict shape of a handle on that
// network is refused before a single fetch.
func TestRecentRefusesAHandleThatCouldReshapeTheAddress(t *testing.T) {
	p := stand(t)
	for network, bad := range map[string][]string{
		"bluesky": {
			"", "ada", "@", "ada.bsky.social/../x", "ada.bsky.social?actor=eve.bsky.social", "ada.bsky.social&limit=1", "../../x",
			"ada bsky.social", "ada.bsky.social#", "did:plc:abcdefghijklmnopqrstuvwx", "-ada.bsky.social", "ada-.bsky.social", "ada..bsky.social",
			".ada.bsky.social", "ada.bsky.social.", "ada.bsky.social\n", "ａda.bsky.social", "ada.bsky.social%2f", "ada@bsky.social", "a.b/c.d",
			strings.Repeat("a", 64) + ".bsky.social", strings.Repeat("a.", 130) + "social",
		},
		"mastodon": {
			"", "ada", "@ada", "ada@", "@@ada@hachyderm.io", "ada@hachyderm.io@hachyderm.io", "ada@evil.example", "ada@sub.hachyderm.io", "ada@hachyderm.io.evil.com",
			"ada@hachyderm.io/../x", "ada@hachyderm.io?x=1", "ada@hachyderm.io:8443", "ada@hachyderm.io#", "a/b@hachyderm.io", "../x@hachyderm.io", "..@hachyderm.io",
			"ada.@hachyderm.io", ".ada@hachyderm.io", "ada@127.0.0.1", "ada@localhost", "ada@hachyderm.io\n", "ada%40hachyderm.io", strings.Repeat("a", 65) + "@hachyderm.io",
			"ada&x=1@hachyderm.io", "ada?x=1@hachyderm.io",
		},
		"reddit": {
			"", "a", "ab", "a/b", "../x", "ada.json", "a b c", "u/ada", "/u/ada", strings.Repeat("a", 21), "ada?x=1", "ada%2f..", "ada\n", "ada#", "ada&limit=1",
			"..", "ada/../x", "ａda", "ada;x",
		},
		"hn": {
			"", "a", strings.Repeat("a", 16), "a/b", "a.b", "../x", "ada?x", "ada%00", "ada\n", "ada.json", "a b", "ada#", "ａda",
		},
		"devto": {
			"", "a/b", "a.b", "ada-1", "../x", "ada?x", "ada&per_page=1", "ada%00", "ada\n", "ada#", "ａda", "@ada", strings.Repeat("a", 31), "a b",
		},
	} {
		for _, h := range bad {
			got, err := Recent(context.Background(), network, h)
			if !errors.Is(err, ErrRefused) || got != nil {
				t.Errorf("Recent(%s, %q) = %q %v, want a refusal", network, h, got, err)
			}
		}
	}
	if len(p.calls) != 0 {
		t.Fatalf("a refused handle was fetched: %v", p.calls)
	}
	// The shapes that are handles are not refused (they reach the fetch).
	for network, good := range map[string]string{"bluesky": "ada.bsky.social", "mastodon": "ada@hachyderm.io", "reddit": "ada_1", "hn": "ada-1", "devto": "ada_1"} {
		if _, err := Recent(context.Background(), network, good); errors.Is(err, ErrRefused) {
			t.Errorf("Recent(%s, %q) was refused: %v", network, good, err)
		}
	}
}
