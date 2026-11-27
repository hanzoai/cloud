package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// ── canned networks ───────────────────────────────────────────────────────────

// ok is a 200 answer with the JSON of v.
func ok(v any) reply {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return reply{Status: http.StatusOK, Body: b}
}

// raw is a 200 answer with body as it is.
func raw(body string) reply { return reply{Status: http.StatusOK, Body: []byte(body)} }

// xPost is X's oEmbed answer for a post whose words are text, with links in the
// words and the permalink in the footer, as X writes them.
func xPost(user, id, text string, links ...string) reply {
	h := `<blockquote class="twitter-tweet"><p lang="en" dir="ltr">` + text
	for _, l := range links {
		h += ` <a href="` + l + `">` + strings.TrimPrefix(l, "https://") + `</a>`
	}
	h += `</p>&mdash; Name (@` + user + `) <a href="https://twitter.com/` + user + `/status/` + id + `?ref_src=twsrc%5Etfw">October 26, 2026</a></blockquote>`
	return ok(map[string]any{"url": "https://twitter.com/" + user + "/status/" + id, "author_url": "https://twitter.com/" + user, "html": h})
}

// bskyPost is the AppView's answer for a post: indexed at indexed, written (by its
// author's account) at created, with links in its facets.
func bskyPost(handle, rkey, text, indexed, created string, uris ...string) reply {
	facets := []map[string]any{}
	for _, u := range uris {
		facets = append(facets, map[string]any{"features": []map[string]any{{"$type": "app.bsky.richtext.facet#link", "uri": u}}})
	}
	post := map[string]any{
		"uri": "at://did:plc:abcdefghijklmnopqrstuvwx/app.bsky.feed.post/" + rkey, "author": map[string]any{"handle": handle},
		"record": map[string]any{"text": text, "createdAt": created, "facets": facets},
	}
	if indexed != "" {
		post["indexedAt"] = indexed
	}
	return ok(map[string]any{"thread": map[string]any{"$type": "app.bsky.feed.defs#threadViewPost", "post": post}})
}

// redditPost is a subreddit post's .json.
func redditPost(id, author, title, self, link string, created float64, extra string) reply {
	return raw(fmt.Sprintf(`[{"data":{"children":[{"kind":"t3","data":{"id":%q,"author":%q,"title":%q,"selftext":%q,"url":%q,"is_self":%t,"created_utc":%v%s}}]}}]`,
		id, author, title, self, link, link == "", created, extra))
}

// mastodonStatus is an instance's answer for a status.
func mastodonStatus(id, acct, visibility, content, created string, locked bool, card string) reply {
	s := map[string]any{"id": id, "visibility": visibility, "content": content, "created_at": created, "account": map[string]any{"acct": acct, "locked": locked}}
	if card != "" {
		s["card"] = map[string]any{"url": card}
	}
	return ok(s)
}

// serve answers the API address of the post at u with r.
func (p *pages) serve(t *testing.T, u string, r reply) Target {
	t.Helper()
	tg, api, err := plan(u)
	if err != nil || api == "" {
		t.Fatalf("plan %s: %q %v", u, api, err)
	}
	p.m[api] = r
	return tg
}

var (
	published = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	written   = time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC)
)

// same reports whether got says what want says in everything but the fetch's own
// status and time.
func same(got, want Page) bool {
	return got.Public == want.Public && got.Gone == want.Gone && got.Author == want.Author &&
		slices.Equal(got.Links, want.Links) && got.Published.Equal(want.Published)
}

// TestReadStatesWhatEachNetworkSays: for every readable network, a good post, a
// deleted one and a private one, from the network's own answer.
func TestReadStatesWhatEachNetworkSays(t *testing.T) {
	const ref = "https://hanzo.ai/beta?ref=ADA1"
	type tc struct {
		name string
		url  string
		rep  reply
		want Page
		fail bool
	}
	const bsky = "https://bsky.app/profile/ada.bsky.social/post/3kabc123xyz"
	const red = "https://www.reddit.com/r/x1/comments/1abc2de/"
	const hn = "https://news.ycombinator.com/item?id=41"
	const mast = "https://hachyderm.io/@ada/77"
	const x = "https://x.com/ada/status/10"
	for _, c := range []tc{
		// X
		{"x, a public post", x, xPost("Ada", "10", "Hello Hanzo", "https://t.co/q"), Page{Public: true, Author: "ada", Links: []string{"https://t.co/q"}}, false},
		{"x, a time in its HTML", x, raw(`{"url":"https://twitter.com/ada/status/10","author_url":"https://twitter.com/Ada","html":"<blockquote><p>hi</p><time datetime=\"2026-10-26T12:00:00Z\">Oct</time></blockquote>"}`), Page{Public: true, Author: "ada", Published: published}, false},
		{"x, deleted", x, reply{Status: 404}, Page{Gone: true}, false},
		{"x, removed", x, reply{Status: 410}, Page{Gone: true}, false},
		{"x, protected", x, reply{Status: 403}, Page{}, false},
		{"x, an answer for another post", x, xPost("ada", "11", "x"), Page{}, true},
		{"x, an answer for a longer id", "https://x.com/ada/status/123", xPost("ada", "1234", "x"), Page{}, true},
		{"x, not json", x, raw("<html>"), Page{}, true},
		{"x, empty html", x, raw(`{"url":"https://twitter.com/ada/status/10","html":""}`), Page{}, true},
		// Bluesky
		{"bluesky, a public post", bsky, bskyPost("Ada.bsky.social", "3kabc123xyz", "Hello Hanzo hanzo.ai/beta?ref=ADA1", "2026-10-26T12:00:00.000Z", "2026-10-20T08:30:00.000Z", ref),
			Page{Public: true, Author: "ada.bsky.social", Links: []string{ref}, Published: published}, false},
		{"bluesky, the index time wins over a back-dated one", bsky, bskyPost("ada.bsky.social", "3kabc123xyz", "x", "2026-10-26T12:00:00Z", "2020-01-01T00:00:00Z"),
			Page{Public: true, Author: "ada.bsky.social", Published: published}, false},
		{"bluesky, no index time falls back to the author's", bsky, bskyPost("ada.bsky.social", "3kabc123xyz", "x", "", "2026-10-20T08:30:00Z"),
			Page{Public: true, Author: "ada.bsky.social", Published: written}, false},
		{"bluesky, a card and a quoted media card are links", bsky,
			raw(`{"thread":{"$type":"app.bsky.feed.defs#threadViewPost","post":{"uri":"at://did:plc:a/app.bsky.feed.post/3kabc123xyz","author":{"handle":"ada.bsky.social"},"record":{"text":"x","embed":{"external":{"uri":"https://t.co/r"}}},"embed":{"media":{"external":{"uri":"https://bit.ly/m"}}}}}}`),
			Page{Public: true, Author: "ada.bsky.social", Links: []string{"https://t.co/r", "https://bit.ly/m"}}, false},
		{"bluesky, not found", bsky, raw(`{"thread":{"$type":"app.bsky.feed.defs#notFoundPost","uri":"x"}}`), Page{Gone: true}, false},
		{"bluesky, the author does not exist", bsky, reply{Status: 400}, Page{Gone: true}, false},
		{"bluesky, blocked", bsky, raw(`{"thread":{"$type":"app.bsky.feed.defs#blockedPost"}}`), Page{}, false},
		{"bluesky, hidden from signed-out readers", bsky,
			raw(`{"thread":{"$type":"app.bsky.feed.defs#threadViewPost","post":{"uri":"at://d/app.bsky.feed.post/3kabc123xyz","author":{"handle":"ada.bsky.social","labels":[{"val":"!no-unauthenticated"}]},"record":{"text":"x https://hanzo.ai/?ref=ADA1"}}}}`),
			Page{}, false},
		{"bluesky, an answer for another post", bsky, bskyPost("ada.bsky.social", "3kzzzzzzzzz", "x", "", ""), Page{}, true},
		{"bluesky, an answer for another author", bsky, bskyPost("eve.bsky.social", "3kabc123xyz", "x", "", ""), Page{}, true},
		{"bluesky, no post in the thread", bsky, raw(`{"thread":{"$type":"app.bsky.feed.defs#threadViewPost"}}`), Page{}, true},
		// Reddit
		{"reddit, a text post", red, redditPost("1abc2de", "Ada", "Title", "body https://hanzo.ai/?ref=ADA1", "", 1792000000, ""),
			Page{Public: true, Author: "ada", Links: []string{"https://hanzo.ai/?ref=ADA1"}, Published: time.Unix(1792000000, 0).UTC()}, false},
		{"reddit, a link post", red, redditPost("1abc2de", "ada", "Title", "", ref, 1792000000.0, ""),
			Page{Public: true, Author: "ada", Links: []string{ref}, Published: time.Unix(1792000000, 0).UTC()}, false},
		{"reddit, removed by a moderator", red, redditPost("1abc2de", "ada", "T", "", "", 1792000000, `,"removed_by_category":"moderator"`), Page{Gone: true}, false},
		{"reddit, deleted by its author", red, redditPost("1abc2de", "[deleted]", "T", "[deleted]", "", 1792000000, ""), Page{Gone: true}, false},
		{"reddit, removed text", red, redditPost("1abc2de", "ada", "T", "[removed]", "", 1792000000, ""), Page{Gone: true}, false},
		{"reddit, a private subreddit", red, redditPost("1abc2de", "ada", "T", "x", "", 1792000000, `,"subreddit_type":"private"`), Page{}, false},
		{"reddit, a banned subreddit", red, reply{Status: 404}, Page{Gone: true}, false},
		{"reddit, a quarantined or private subreddit", red, reply{Status: 403}, Page{}, false},
		{"reddit, an answer for another post", red, redditPost("1zzzzzz", "ada", "T", "x", "", 1792000000, ""), Page{}, true},
		{"reddit, no listing", red, raw(`[]`), Page{}, true},
		// Hacker News
		{"hn, a story", hn, raw(`{"id":41,"by":"Ada","type":"story","time":1792000000,"title":"Show HN: x","url":"https://hanzo.ai/beta?ref=ADA1"}`),
			Page{Public: true, Author: "ada", Links: []string{ref}, Published: time.Unix(1792000000, 0).UTC()}, false},
		{"hn, a comment with a link in its html", hn, raw(`{"id":41,"by":"a","time":1792000000,"text":"see &lt;a href=&quot;x&quot;&gt; <a href=\"https://hanzo.ai/beta?ref=ADA1\">here</a>"}`),
			Page{Public: true, Author: "a", Links: []string{ref}, Published: time.Unix(1792000000, 0).UTC()}, false},
		{"hn, a missing item", hn, raw(`null`), Page{Gone: true}, false},
		{"hn, a dead item", hn, raw(`{"id":41,"dead":true}`), Page{Gone: true}, false},
		{"hn, a deleted item", hn, raw(`{"id":41,"deleted":true}`), Page{Gone: true}, false},
		{"hn, an answer for another item", hn, raw(`{"id":42,"by":"a"}`), Page{}, true},
		// Mastodon
		{"mastodon, a public status", mast, mastodonStatus("77", "Ada", "public", `<p>Hello <a href="https://hanzo.ai/beta?ref=ADA1">hanzo.ai/beta</a></p>`, "2026-10-26T12:00:00.000Z", false, ""),
			Page{Public: true, Author: "ada", Links: []string{ref, "https://hanzo.ai/beta"}, Published: published}, false},
		{"mastodon, an unlisted status with a card", mast, mastodonStatus("77", "ada", "unlisted", "hi", "2026-10-26T12:00:00Z", false, ref),
			Page{Public: true, Author: "ada", Links: []string{ref}, Published: published}, false},
		{"mastodon, a remote account", mast, mastodonStatus("77", "ada@fosstodon.org", "public", "hi", "2026-10-26T12:00:00Z", false, ""),
			Page{Public: true, Author: "ada@fosstodon.org", Published: published}, false},
		{"mastodon, deleted", mast, reply{Status: 404}, Page{Gone: true}, false},
		{"mastodon, a locked account", mast, mastodonStatus("77", "ada", "public", "hi "+ref, "2026-10-26T12:00:00Z", true, ""), Page{}, false},
		{"mastodon, followers only", mast, mastodonStatus("77", "ada", "private", "hi", "2026-10-26T12:00:00Z", false, ""), Page{}, false},
		{"mastodon, direct", mast, mastodonStatus("77", "ada", "direct", "hi", "2026-10-26T12:00:00Z", false, ""), Page{}, false},
		{"mastodon, an authorized-fetch instance", mast, reply{Status: 401}, Page{}, false},
		{"mastodon, an answer for another status", mast, mastodonStatus("78", "ada", "public", "hi", "2026-10-26T12:00:00Z", false, ""), Page{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := stand(t)
			tg := p.serve(t, c.url, c.rep)
			before := time.Now()
			got, err := Read(context.Background(), tg)
			if c.fail {
				if err == nil {
					t.Fatalf("read = %+v, want an error", got)
				}
				if errors.Is(err, ErrRefused) {
					t.Fatalf("an answer that is not the post is not a permanent refusal: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !same(got, c.want) {
				t.Fatalf("read = %+v, want %+v", got, c.want)
			}
			if got.Status != c.rep.Status || got.At.Before(before.UTC().Add(-time.Second)) || time.Since(got.At) > time.Minute || got.At.Location() != time.UTC {
				t.Fatalf("status %d at %s, want status %d just now in UTC", got.Status, got.At, c.rep.Status)
			}
			if len(p.calls) != 1 {
				t.Fatalf("fetched %v, want one fetch", p.calls)
			}
		})
	}
}

// TestReadMapsEveryStatusTheSameWay: what a network's HTTP status means does not
// depend on the network. Missing is Gone, forbidden is not public, and a status
// that says nothing about the post is an error to try again later.
func TestReadMapsEveryStatusTheSameWay(t *testing.T) {
	for _, u := range []string{
		"https://x.com/ada/status/10",
		"https://bsky.app/profile/ada.bsky.social/post/3kabc123xyz",
		"https://www.reddit.com/r/x1/comments/1abc2de/",
		"https://news.ycombinator.com/item?id=41",
		"https://hachyderm.io/@ada/77",
	} {
		for status, want := range map[int]string{404: "gone", 410: "gone", 400: "gone", 401: "private", 403: "private", 429: "error", 500: "error", 502: "error", 503: "error", 301: "error", 204: "error"} {
			p := stand(t)
			tg := p.serve(t, u, reply{Status: status})
			got, err := Read(context.Background(), tg)
			var have string
			switch {
			case err != nil:
				have = "error"
			case got.Gone && !got.Public:
				have = "gone"
			case !got.Gone && !got.Public:
				have = "private"
			default:
				have = "public"
			}
			if have != want || got.Status != status && err == nil {
				t.Errorf("%s with %d = %q %+v %v, want %q", u, status, have, got, err, want)
			}
		}
		// A transport failure is an error, and not a statement about the post.
		p := stand(t)
		tg := p.serve(t, u, reply{})
		_, api, _ := plan(u)
		p.err[api] = errors.New("tls: handshake failure")
		if got, err := Read(context.Background(), tg); err == nil || errors.Is(err, ErrRefused) {
			t.Errorf("%s, a transport failure = %+v %v", u, got, err)
		}
	}
}

// TestReadNeverFetchesATargetItDidNotMakeOrCannotRead: a review network and a
// hand-built Target are refused with nothing fetched.
func TestReadNeverFetchesATargetItDidNotMakeOrCannotRead(t *testing.T) {
	p := stand(t)
	li, err := Locate("https://www.linkedin.com/posts/ada_x-activity-7123456789012345678-AbCd")
	if err != nil {
		t.Fatal(err)
	}
	x, _ := Locate("https://x.com/ada/status/10")
	for name, tg := range map[string]Target{
		"linkedin":         li,
		"a review flag":    {Network: "x", Key: "10", URL: "https://x.com/ada/status/10", Review: true},
		"a zero target":    {},
		"metadata":         {Network: "x", Key: "10", URL: "https://169.254.169.254/latest/meta-data/"},
		"an unknown host":  {Network: "x", Key: "10", URL: "https://evil.com/ada/status/10"},
		"another key":      {Network: x.Network, Key: "11", URL: x.URL},
		"another network":  {Network: "reddit", Key: x.Key, URL: x.URL},
		"a facebook post":  {Network: "facebook", Key: "1", URL: "https://www.facebook.com/a/posts/b"},
		"a non-canonical":  {Network: "x", Key: "10", URL: "https://twitter.com/ada/status/10"},
		"a trailing space": {Network: "x", Key: "10", URL: "https://x.com/ada/status/10 "},
	} {
		if got, err := Read(context.Background(), tg); !errors.Is(err, ErrRefused) || !same(got, Page{}) {
			t.Errorf("%s: Read = %+v %v, want a refusal", name, got, err)
		}
	}
	if len(p.calls) != 0 {
		t.Fatalf("fetched %v", p.calls)
	}
}

// TestReadKeepsAtMostTwelveLinks.
func TestReadKeepsAtMostTwelveLinks(t *testing.T) {
	var uris []string
	for i := range 20 {
		uris = append(uris, fmt.Sprintf("https://example.com/%d", i))
	}
	p := stand(t)
	tg := p.serve(t, "https://bsky.app/profile/ada.bsky.social/post/3kabc123xyz", bskyPost("ada.bsky.social", "3kabc123xyz", "x", "", "", uris...))
	got, err := Read(context.Background(), tg)
	if err != nil || len(got.Links) != 12 || got.Links[0] != "https://example.com/0" || got.Links[11] != "https://example.com/11" {
		t.Fatalf("links = %v %v", got.Links, err)
	}
}

// TestLinksAreFoundWhereEachNetworkPutsThem: hrefs, written-out URLs and
// hanzo.ai addresses without a scheme, https only, host lower-cased, trailing
// punctuation dropped, no repeats.
func TestLinksAreFoundWhereEachNetworkPutsThem(t *testing.T) {
	got := linksIn(
		[]string{`<p>see <a href="https://T.co/Abc">t.co</a> and <a href="mailto:a@b.c">mail</a> <a href="javascript:x">js</a> <a href="//evil.com/x">rel</a></p>`},
		[]string{"go hanzo.ai/beta?ref=Q now, or https://hanzo.ai/beta?ref=Q.", "http://example.com/plain (and http://example.com/plain).", "evilhanzo.ai/x hanzo.ai.evil.com/x"},
	)
	want := []string{"https://t.co/Abc", "https://hanzo.ai/beta?ref=Q", "https://example.com/plain"}
	if !slices.Equal(got, want) {
		t.Fatalf("links = %q, want %q", got, want)
	}
}

// ── Ref ───────────────────────────────────────────────────────────────────────

// TestRefFollowsAShortLinkToTheMembersCode: a post whose link is a t.co link that
// goes to hanzo.ai with the member's ref carries it, whatever the case of the
// code; one that goes anywhere else, or to another code, does not.
func TestRefFollowsAShortLinkToTheMembersCode(t *testing.T) {
	p := stand(t)
	p.m["https://t.co/q"] = reply{Status: 301, Location: "https://hanzo.ai/beta?ref=ABC"}
	p.m["https://bit.ly/x"] = reply{Status: 302, Location: "https://t.co/q"}
	p.m["https://t.co/other"] = reply{Status: 301, Location: "https://hanzo.ai/beta?ref=ZZZ"}
	p.m["https://t.co/evil"] = reply{Status: 301, Location: "https://hanzo.ai.evil.com/beta?ref=ABC"}
	p.m["https://t.co/away"] = reply{Status: 301, Location: "https://example.com/?ref=ABC"}
	p.m["https://t.co/meta"] = reply{Status: 200, Body: []byte(`<META http-equiv="refresh" content="0;URL=https://app.hanzo.ai/x?ref=ABC">`)}
	for _, tc := range []struct {
		name string
		link string
		code string
		want bool
	}{
		{"t.co to hanzo.ai", "https://t.co/q", "ABC", true},
		{"the code in another case", "https://t.co/q", "abc", true},
		{"two short links", "https://bit.ly/x", "ABC", true},
		{"a meta refresh to a subdomain", "https://t.co/meta", "ABC", true},
		{"another member's code", "https://t.co/other", "ABC", false},
		{"a link to hanzo.ai.evil.com", "https://t.co/evil", "ABC", false},
		{"a link to another site that carries the code", "https://t.co/away", "ABC", false},
		{"no code is no match", "https://t.co/q", "", false},
		{"a blank code is no match", "https://t.co/q", "  ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Ref(context.Background(), Page{Links: []string{tc.link}}, tc.code)
			if err != nil || got != tc.want {
				t.Fatalf("Ref = %v %v, want %v", got, err, tc.want)
			}
		})
	}
}

// TestRefOnlyCountsALinkOnHanzoAI: the link must be https on hanzo.ai or one of
// its subdomains, with the parameter ref equal to the code. Look-alike hosts,
// credentials, ports, other schemes and the code in another parameter do not
// count, and none of them is fetched.
func TestRefOnlyCountsALinkOnHanzoAI(t *testing.T) {
	p := stand(t)
	for link, want := range map[string]bool{
		"https://hanzo.ai/beta?ref=ABC":                  true,
		"https://hanzo.ai?ref=ABC":                       true,
		"https://www.hanzo.ai/beta?ref=ABC":              true,
		"https://app.hanzo.ai/x?utm=1&ref=ABC":           true,
		"https://hanzo.ai/beta?ref=abc":                  true,
		"https://hanzo.ai/beta?REF=Abc":                  true,
		"https://hanzo.ai/beta?ref=ABC&ref=ZZZ":          true,
		"https://hanzo.ai/beta?ref=ZZZ&ref=ABC":          true,
		"https://hanzo.ai:443/beta?ref=ABC":              true,
		"https://hanzo.ai/beta?ref=ZZZ":                  false,
		"https://hanzo.ai/beta?ref=ABCD":                 false,
		"https://hanzo.ai/beta?ref=ABC%20":               false,
		"https://hanzo.ai/beta?ref=":                     false,
		"https://hanzo.ai/beta?xref=ABC":                 false,
		"https://hanzo.ai/beta?utm_ref=ABC":              false,
		"https://hanzo.ai/beta":                          false,
		"https://hanzo.ai/beta#ref=ABC":                  false,
		"https://hanzo.ai/ref=ABC":                       false,
		"https://hanzo.ai.evil.com/beta?ref=ABC":         false,
		"https://evilhanzo.ai/beta?ref=ABC":              false,
		"https://nothanzo.ai/beta?ref=ABC":               false,
		"https://hanzo.ai@evil.com/beta?ref=ABC":         false,
		"https://evil.com@hanzo.ai/beta?ref=ABC":         false,
		"https://user:pw@hanzo.ai/beta?ref=ABC":          false,
		"https://evil.com/hanzo.ai?ref=ABC":              false,
		"https://evil.com/?u=hanzo.ai&ref=ABC":           false,
		"https://hanzo.ai:8443/beta?ref=ABC":             false,
		"http://hanzo.ai/beta?ref=ABC":                   false,
		"ftp://hanzo.ai/beta?ref=ABC":                    false,
		"//hanzo.ai/beta?ref=ABC":                        false,
		"hanzo.ai/beta?ref=ABC":                          false,
		"https://HANZO.ai/beta?ref=ABC":                  false,
		"https://hanzo.ai./beta?ref=ABC":                 false,
		"https://127.0.0.1/beta?ref=ABC":                 false,
		"https://[::1]/beta?ref=ABC":                     false,
		"https://xn--hanzo-9ua.ai/beta?ref=ABC":          false,
		"https://hanzo.ai\\.evil.com/beta?ref=ABC":       false,
		"https://example.com/":                           false,
		"":                                               false,
		"not a link":                                     false,
		"https://hanzo.ai/beta?ref=ABC\x00":              false,
		"https://hanzo.ai.t.co/beta?ref=ABC":             false,
		"https://t.co.hanzo.ai.evil.com/beta?ref=ABC":    false,
		"https://hanzo.ai%2eevil.com/beta?ref=ABC":       false,
		"https://hanzo.ai%00.evil.com/beta?ref=ABC":      false,
		"https://hanzo.ai․evil.com/beta?ref=ABC":         false,
		"https://hanzo.ai/beta?ref=ABC#https://evil.com": true,
	} {
		got, err := Ref(context.Background(), Page{Links: []string{link}}, "ABC")
		if err != nil || got != want {
			t.Errorf("Ref(%q) = %v %v, want %v", link, got, err, want)
		}
	}
	if len(p.calls) != 0 {
		t.Fatalf("a link that is not a listed shortener was fetched: %v", p.calls)
	}
}

// TestRefLooksAtTwelveLinks: the thirteenth link of a page is not looked at.
func TestRefLooksAtTwelveLinks(t *testing.T) {
	stand(t)
	var links []string
	for i := range 11 {
		links = append(links, fmt.Sprintf("https://example.com/%d", i))
	}
	mine := "https://hanzo.ai/beta?ref=ABC"
	if got, err := Ref(context.Background(), Page{Links: append(slices.Clone(links), mine)}, "ABC"); err != nil || !got {
		t.Fatalf("the twelfth link: %v %v", got, err)
	}
	if got, err := Ref(context.Background(), Page{Links: append(slices.Clone(links), "https://example.com/11", mine)}, "ABC"); err != nil || got {
		t.Fatalf("the thirteenth link: %v %v", got, err)
	}
}

// TestRefSaysWhenAShortLinkCouldNotBeFetched: a short link that fails, or that
// its shortener refuses to answer now (429, 5xx), is an error to try again later
// and never a "no"; one the guard refuses is a "no".
func TestRefSaysWhenAShortLinkCouldNotBeFetched(t *testing.T) {
	p := stand(t)
	p.err["https://t.co/down"] = errors.New("connection reset")
	p.err["https://t.co/refused"] = fmt.Errorf("%w: nope", ErrRefused)
	p.m["https://t.co/slow"] = reply{Status: 429}
	p.m["https://t.co/broken"] = reply{Status: 503}
	page := Page{Links: []string{"https://t.co/down", "https://hanzo.ai/beta?ref=ABC"}}
	if got, err := Ref(context.Background(), page, "ABC"); err == nil || got {
		t.Fatalf("a short link that failed = %v %v, want an error", got, err)
	}
	if got, err := Ref(context.Background(), Page{Links: []string{"https://t.co/refused", "https://hanzo.ai/beta?ref=ABC"}}, "ABC"); err != nil || !got {
		t.Fatalf("a short link that was refused = %v %v, want the next link to count", got, err)
	}
	for _, link := range []string{"https://t.co/slow", "https://t.co/broken"} {
		if got, err := Ref(context.Background(), Page{Links: []string{link, "https://hanzo.ai/beta?ref=ABC"}}, "ABC"); err == nil || got {
			t.Fatalf("%s answered 429 or 500 = %v %v, want an error", link, got, err)
		}
	}
}

// TestAPostThatLinksThroughTCoCarriesTheMembersRef: Read, then Ref, on an X post,
// end to end through the canned network.
func TestAPostThatLinksThroughTCoCarriesTheMembersRef(t *testing.T) {
	p := stand(t)
	tg := p.serve(t, "https://x.com/ada/status/10", xPost("ada", "10", "I am building with Hanzo", "https://t.co/q"))
	p.m["https://t.co/q"] = reply{Status: 301, Location: "https://hanzo.ai/beta?ref=ADA1"}
	page, err := Read(context.Background(), tg)
	if err != nil || !page.Public || page.Author != "ada" {
		t.Fatalf("read = %+v %v", page, err)
	}
	for code, want := range map[string]bool{"ADA1": true, "ada1": true, "EVE1": false} {
		if got, err := Ref(context.Background(), page, code); err != nil || got != want {
			t.Errorf("Ref(%s) = %v %v, want %v", code, got, err, want)
		}
	}
	// The same post, edited to point at a look-alike site, does not.
	p.m["https://t.co/q"] = reply{Status: 301, Location: "https://hanzo.ai.evil.com/beta?ref=ADA1"}
	if got, err := Ref(context.Background(), page, "ADA1"); err != nil || got {
		t.Fatalf("a look-alike host = %v %v", got, err)
	}
}
