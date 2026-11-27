package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// ── canned networks ───────────────────────────────────────────────────────────

// devtoPost is the DEV API's answer for an article.
func devtoPost(user, slug, body, published string) reply {
	return ok(map[string]any{"type_of": "article", "path": "/" + user + "/" + slug, "slug": slug, "body_html": body, "published_at": published, "user": map[string]any{"username": user}})
}

// mediumItem is one item of an author's feed.
func mediumItem(link, guid, pub, content string) string {
	return `<item><title><![CDATA[A title]]></title><link>` + link + `</link><guid isPermaLink="false">` + guid + `</guid>` +
		`<dc:creator><![CDATA[A Name]]></dc:creator><pubDate>` + pub + `</pubDate><content:encoded><![CDATA[` + content + `]]></content:encoded></item>`
}

// mediumFeed is an author's RSS feed: the channel's own link, the Atom links Medium
// writes after it, and the items.
func mediumFeed(handle string, items ...string) reply {
	return raw(`<?xml version="1.0" encoding="UTF-8"?><rss xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:atom="http://www.w3.org/2005/Atom" version="2.0"><channel>` +
		`<title><![CDATA[Stories by ` + handle + ` on Medium]]></title><link>https://medium.com/@` + handle + `?source=rss-1------2</link>` +
		`<atom:link href="https://medium.com/@` + handle + `/feed" rel="self" type="application/rss+xml"/><atom:link href="http://medium.superfeedr.com" rel="hub"/>` +
		strings.Join(items, "") + `</channel></rss>`)
}

// hashnodePage is a blog page for the post at canon: the markup Hashnode writes
// (a canonical link, a BlogPosting), the article, and the anchors of the page around it.
func hashnodePage(canon, name, profile, published, body string) reply {
	return hashnodeDoc(canon, `<meta property="og:type" content="article"/>`, "BlogPosting", map[string]any{"@type": "Person", "name": name, "url": profile}, published, body)
}

// hashnodeDoc is hashnodePage with the head, the type of the JSON-LD and its author as given.
func hashnodeDoc(canon, head, kind string, author any, published, body string) reply {
	ld, err := json.Marshal(map[string]any{"@context": "https://schema.org", "@type": kind, "url": canon, "author": author, "datePublished": published})
	if err != nil {
		panic(err)
	}
	return raw(`<!DOCTYPE html><html><head><link rel="canonical" href="` + canon + `"/>` + head +
		`<script type="application/ld+json">` + string(ld) + `</script></head><body><nav><a href="https://hashnode.com/">Hashnode</a> <a href="/about">About</a></nav>` +
		`<article>` + body + `</article><footer><a href="https://example.com/x">x</a></footer></body></html>`)
}

// ── Locate ────────────────────────────────────────────────────────────────────

// TestLocateNamesTheNewReadableNetworksPostsOnce: every spelling of a DEV,
// Medium or Hashnode post becomes one Target with one canonical address, and the
// address it is read at is one the guard fetches.
func TestLocateNamesTheNewReadableNetworksPostsOnce(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Target
		api  string
	}{
		{"https://dev.to/Ada_1/My-Post-1abc", Target{"devto", "ada_1/my-post-1abc", "https://dev.to/ada_1/my-post-1abc", false}, "https://dev.to/api/articles/ada_1/my-post-1abc"},
		{"https://dev.to/ada/my-post-1abc/?utm_source=x#comments", Target{"devto", "ada/my-post-1abc", "https://dev.to/ada/my-post-1abc", false}, "https://dev.to/api/articles/ada/my-post-1abc"},
		{"https://medium.com/@Ada/My-Great-Post-1a2b3c4d5e6f?source=rss-1------2", Target{"medium", "1a2b3c4d5e6f", "https://medium.com/@ada/my-great-post-1a2b3c4d5e6f", false}, "https://medium.com/feed/@ada"},
		{"https://medium.com/@ada.b-c/t-0123456789/", Target{"medium", "0123456789", "https://medium.com/@ada.b-c/t-0123456789", false}, "https://medium.com/feed/@ada.b-c"},
		{"https://publication.medium.com/@ada/post-a1b2c3d4e5f6", Target{"medium", "a1b2c3d4e5f6", "https://medium.com/@ada/post-a1b2c3d4e5f6", false}, "https://medium.com/feed/@ada"},
		{"https://ada.hashnode.dev/My-Post?source=x", Target{"hashnode", "ada.hashnode.dev/my-post", "https://ada.hashnode.dev/my-post", false}, "https://ada.hashnode.dev/my-post"},
		{"https://ada-1.hashnode.dev/a1/", Target{"hashnode", "ada-1.hashnode.dev/a1", "https://ada-1.hashnode.dev/a1", false}, "https://ada-1.hashnode.dev/a1"},
		{"https://hashnode.com/some-post", Target{"hashnode", "hashnode.com/some-post", "https://hashnode.com/some-post", false}, "https://hashnode.com/some-post"},
	} {
		got, api, err := plan(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if got != tc.want || api != tc.api {
			t.Errorf("%s = %+v %q, want %+v %q", tc.in, got, api, tc.want, tc.api)
		}
		if pub, err := Locate(tc.in); err != nil || pub != got {
			t.Errorf("Locate(%s) = %+v %v, want %+v", tc.in, pub, err, got)
		}
		u, err := admit(api)
		if err != nil || !fetchable(u.Hostname()) {
			t.Errorf("%s: its API address %s is not one the guard fetches: %v", tc.in, api, err)
		}
		if again, err := Locate(got.URL); err != nil || again != got {
			t.Errorf("%s: locating its canonical %s gave %+v %v", tc.in, got.URL, again, err)
		}
	}
}

// TestLocateRefusesWhatIsNotOnePostOnTheNewReadableNetworks: look-alike hosts,
// credentials, ports, other schemes, and addresses that are not one post are
// refused, and none of them is ever fetched.
func TestLocateRefusesWhatIsNotOnePostOnTheNewReadableNetworks(t *testing.T) {
	p := stand(t)
	const hex = "1a2b3c4d5e6f"
	for _, in := range []string{
		// DEV
		"https://dev.to.evil.com/a/b",
		"https://evildev.to/a/b",
		"https://www.dev.to/a/b",
		"https://api.dev.to/a/b",
		"https://dev.to@evil.com/a/b",
		"https://user:pw@dev.to/a/b",
		"https://dev.to:8443/a/b",
		"http://dev.to/a/b",
		"https://dev.to/",
		"https://dev.to/a",
		"https://dev.to/a/b/c",
		"https://dev.to/a b/c",
		"https://dev.to/../x/y",
		"https://dev.to/a/b%2Fc",
		"https://dev.to/api/articles/x",
		"https://dev.to/a.b/c",
		"https://dev.to/" + strings.Repeat("a", 31) + "/b",
		// Medium
		"https://medium.com.evil.com/@a/t-" + hex,
		"https://evilmedium.com/@a/t-" + hex,
		"https://medium.com@evil.com/@a/t-" + hex,
		"https://user:pw@medium.com/@a/t-" + hex,
		"https://medium.com:8443/@a/t-" + hex,
		"http://medium.com/@a/t-" + hex,
		"https://a.b.medium.com/@a/t-" + hex,
		"https://medium.com/@a/t-1a2b3c4d5",
		"https://medium.com/@a/t-1a2b3c4d5e6f7",
		"https://medium.com/@a/t-zzzzzzzzzzzz",
		"https://medium.com/@a/t" + hex,
		"https://medium.com/@a/" + hex,
		"https://medium.com/@a/title",
		"https://medium.com/@a/",
		"https://medium.com/@a",
		"https://medium.com/@/t-" + hex,
		"https://medium.com/@a/t-" + hex + "/responses",
		"https://medium.com/@a/b/t-" + hex,
		"https://medium.com/feed/@a",
		"https://medium.com/p/" + hex,
		"https://medium.com/blog/title-" + hex,
		"https://publication.medium.com/title-" + hex,
		// Hashnode
		"https://hashnode.dev/x",
		"https://a.b.hashnode.dev/x",
		"https://evilhashnode.dev/x",
		"https://hashnode.dev.evil.com/x",
		"https://ada.hashnode.dev.evil.com/x",
		"https://ada.hashnode.dev@evil.com/x",
		"https://user:pw@ada.hashnode.dev/x",
		"https://ada.hashnode.dev:8443/x",
		"http://ada.hashnode.dev/x",
		"https://-a.hashnode.dev/x",
		"https://ada.hashnode.com/x",
		"https://hashnode.com.evil.com/x",
		"https://ada.hashnode.dev/",
		"https://ada.hashnode.dev/a/b",
		"https://ada.hashnode.dev/rss.xml",
		"https://ada.hashnode.dev/-x",
		"https://ada.hashnode.dev/" + strings.Repeat("a", 250),
		// a blog on a domain of its own is not a network
		"https://blog.example.com/my-post",
		"https://myblog.example.com/2026/10/my-post",
		"https://ada.github.io/my-post",
		"https://ada.substack.com/p/my-post",
	} {
		got, err := Locate(in)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("Locate(%q) = %+v %v, want a refusal", in, got, err)
		}
		if got != (Target{}) {
			t.Errorf("Locate(%q) returned %+v with its refusal", in, got)
		}
	}
	if len(p.calls) != 0 {
		t.Fatalf("Locate fetched %v", p.calls)
	}
}

// TestAMediumPublicationAddressIsRefusedWithAWayOut: a publication's address names
// no author, so there is no feed to read; the refusal says what to paste.
func TestAMediumPublicationAddressIsRefusedWithAWayOut(t *testing.T) {
	p := stand(t)
	if _, err := Locate("https://medium.com/blog/title-1a2b3c4d5e6f"); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "publication") {
		t.Errorf("a Medium publication address: %v, want a refusal that says it is not supported", err)
	}
	if len(p.calls) != 0 {
		t.Fatalf("fetched %v", p.calls)
	}
}

// TestTwoSpellingsOfOneNewPostAreOneKey: case, a trailing slash, a query and the
// Medium publication host do not make a second claim.
func TestTwoSpellingsOfOneNewPostAreOneKey(t *testing.T) {
	for _, group := range [][]string{
		{"https://dev.to/ada/post-1abc", "https://dev.to/ADA/Post-1ABC/", "https://dev.to/ada/post-1abc?x=1#y"},
		{"https://medium.com/@ada/t-1a2b3c4d5e6f", "https://medium.com/@ADA/T-1A2B3C4D5E6F/", "https://pub.medium.com/@ada/t-1a2b3c4d5e6f?source=x"},
		{"https://ada.hashnode.dev/post", "https://ada.hashnode.dev/POST/", "https://ada.hashnode.dev/post?x=1"},
	} {
		first, err := Locate(group[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range group[1:] {
			got, err := Locate(in)
			if err != nil || got.Key != first.Key || got.Network != first.Network {
				t.Errorf("%s = %+v %v, want the key %q of %s", in, got, err, first.Key, group[0])
			}
		}
	}
	// The same slug on another author or blog is another post.
	a, _ := Locate("https://dev.to/ada/post-1abc")
	b, _ := Locate("https://dev.to/eve/post-1abc")
	c, _ := Locate("https://ada.hashnode.dev/post")
	d, _ := Locate("https://eve.hashnode.dev/post")
	if a.Key == b.Key || c.Key == d.Key {
		t.Fatalf("keys collide across authors: %q %q, %q %q", a.Key, b.Key, c.Key, d.Key)
	}
}

// ── Read ──────────────────────────────────────────────────────────────────────

// TestReadStatesWhatTheNewReadableNetworksSay: for DEV, Medium and Hashnode, a good post that carries the link, one without it, a deleted one, a
// private one, and an answer that is not the post, each from the network's own answer.
func TestReadStatesWhatTheNewReadableNetworksSay(t *testing.T) {
	const ref = "https://hanzo.ai/beta?ref=ADA1"
	const dv = "https://dev.to/ada/my-post-1abc"
	const md = "https://medium.com/@ada/my-post-1a2b3c4d5e6f"
	const hn = "https://ada.hashnode.dev/my-post"
	type tc struct {
		name string
		url  string
		rep  reply
		want Page
		fail bool
	}
	const mdPub = "Mon, 26 Oct 2026 12:00:00 GMT"
	for _, c := range []tc{
		// DEV
		{"devto, an article with the link", dv, devtoPost("ada", "my-post-1abc", `<p>Try <a href="https://hanzo.ai/beta?ref=ADA1">Hanzo</a> and <a href="https://example.com/x">more</a></p>`, "2026-10-26T12:00:00Z"),
			Page{Public: true, Author: "ada", Links: []string{ref}, Published: published}, false},
		{"devto, a short link in the body", dv, devtoPost("ada", "my-post-1abc", `<p><a href="https://t.co/q">q</a></p>`, "2026-10-26T12:00:00Z"),
			Page{Public: true, Author: "ada", Links: []string{"https://t.co/q"}, Published: published}, false},
		{"devto, an article without the link", dv, devtoPost("ada", "my-post-1abc", `<p><a href="https://example.com/x">x</a></p>`, ""), Page{Public: true, Author: "ada"}, false},
		{"devto, deleted", dv, reply{Status: 404}, Page{Gone: true}, false},
		{"devto, an answer for another article", dv, devtoPost("ada", "other-1abc", "x", ""), Page{}, true},
		{"devto, an answer for another author", dv, devtoPost("eve", "my-post-1abc", "x", ""), Page{}, true},
		{"devto, an article whose path says another author", dv, ok(map[string]any{"path": "/ada/my-post-1abc", "user": map[string]any{"username": "eve"}}), Page{}, true},
		{"devto, not json", dv, raw("<html>"), Page{}, true},
		// Medium
		{"medium, a story in the feed with the link", md,
			mediumFeed("Ada",
				mediumItem("https://medium.com/@ada/older-post-ffffffffffff?source=rss-1------2", "https://medium.com/p/ffffffffffff", "Sun, 25 Oct 2026 12:00:00 GMT", `<p><a href="https://example.com/old">old</a></p>`),
				mediumItem("https://medium.com/@ada/my-post-1a2b3c4d5e6f?source=rss-1------2", "https://medium.com/p/1a2b3c4d5e6f", mdPub, `<h3>T</h3><p>Try <a href="https://hanzo.ai/beta?ref=ADA1">Hanzo</a> <a href="https://example.com/x">x</a></p>`)),
			Page{Public: true, Author: "ada", Links: []string{ref}, Published: published}, false},
		{"medium, a story of a publication is found by its id", md,
			mediumFeed("ada", mediumItem("https://medium.com/some-publication/my-post-1a2b3c4d5e6f?source=rss-1------2", "https://medium.com/p/1a2b3c4d5e6f", mdPub, `<a href="https://t.co/q">q</a>`)),
			Page{Public: true, Author: "ada", Links: []string{"https://t.co/q"}, Published: published}, false},
		{"medium, a story found by its guid", md,
			mediumFeed("ada", mediumItem("https://medium.com/redirect?x=1", "https://medium.com/p/1a2b3c4d5e6f", mdPub, "x")),
			Page{Public: true, Author: "ada", Published: published}, false},
		{"medium, a story without the link", md,
			mediumFeed("ada", mediumItem("https://medium.com/@ada/my-post-1a2b3c4d5e6f", "https://medium.com/p/1a2b3c4d5e6f", mdPub, `<a href="https://example.com/x">x</a>`)),
			Page{Public: true, Author: "ada", Published: published}, false},
		{"medium, a story older than the feed reaches is gone", md,
			mediumFeed("ada", mediumItem("https://medium.com/@ada/newer-ffffffffffff", "https://medium.com/p/ffffffffffff", mdPub, "x")), Page{Gone: true}, false},
		{"medium, a feed without this story's id has not got it", md,
			mediumFeed("ada", mediumItem("https://medium.com/@eve/my-post-1a2b3c4d5e60", "https://medium.com/p/1a2b3c4d5e60", mdPub, "x")), Page{Gone: true}, false},
		{"medium, an author with no stories", md, mediumFeed("ada"), Page{Gone: true}, false},
		{"medium, no such author", md, reply{Status: 404}, Page{Gone: true}, false},
		{"medium, a feed that is not allowed", md, reply{Status: 403}, Page{}, false},
		{"medium, a feed that is not XML", md, raw("<html><body>Just a moment...</body></html>"), Page{}, true},
		{"medium, a feed that names no author", md, raw(`<rss><channel><link>https://medium.com/</link></channel></rss>`), Page{}, true},
		// Hashnode
		{"hashnode, a post with the link", hn,
			hashnodePage(hn, "Ada Lovelace", "https://hashnode.com/@Ada", "2026-10-26T12:00:00.000Z", `<p>Try <a href="https://hanzo.ai/beta?ref=ADA1">Hanzo</a></p>`),
			Page{Public: true, Author: "ada", Links: []string{ref}, Published: published}, false},
		{"hashnode, the author meta is the author when the markup gives no profile", hn,
			hashnodeDoc(hn, `<meta name="author" content="Ada L"/>`, "BlogPosting", map[string]any{"name": "Ada Lovelace"}, "2026-10-26T12:00:00Z", "x"),
			Page{Public: true, Author: "ada l", Published: published}, false},
		{"hashnode, the author's name is the author when nothing else is given", hn,
			hashnodeDoc(hn, "", "BlogPosting", []map[string]any{{"name": "Ada Lovelace"}}, "2026-10-26T12:00:00Z", "x"),
			Page{Public: true, Author: "ada lovelace", Published: published}, false},
		{"hashnode, a post without the link", hn, hashnodePage(hn, "Ada", "", "", `<a href="https://example.com/y">y</a>`), Page{Public: true, Author: "ada"}, false},
		{"hashnode, deleted", hn, reply{Status: 404}, Page{Gone: true}, false},
		{"hashnode, not for everyone", hn, reply{Status: 403}, Page{}, false},
		{"hashnode, a page whose canonical link is another post", hn, hashnodePage("https://ada.hashnode.dev/other", "Ada", "", "", "x"), Page{}, true},
		{"hashnode, a page that is not a post", hn, hashnodeDoc(hn, "", "WebSite", map[string]any{"name": "Ada"}, "", "x"), Page{}, true},
		{"hashnode, a page with no canonical link", hn, raw(`<html><head></head><body><a href="` + ref + `">x</a></body></html>`), Page{}, true},
		{"hashnode, hashnode.com", "https://hashnode.com/my-post", hashnodePage("https://hashnode.com/my-post/", "Ada", "https://hashnode.com/@ada", "", "x"), Page{Public: true, Author: "ada"}, false},
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
			if got.Status != c.rep.Status || got.At.Before(before.UTC().Add(-time.Second)) || time.Since(got.At) > time.Minute {
				t.Fatalf("status %d at %s, want status %d just now", got.Status, got.At, c.rep.Status)
			}
			if len(p.calls) != 1 {
				t.Fatalf("fetched %v, want one fetch", p.calls)
			}
		})
	}
}

// TestReadFetchesEachNewNetworkAtItsOwnEndpoint: what is asked for, per network.
func TestReadFetchesEachNewNetworkAtItsOwnEndpoint(t *testing.T) {
	for url, want := range map[string]string{
		"https://dev.to/Ada/My-Post-1abc":               "https://dev.to/api/articles/ada/my-post-1abc",
		"https://medium.com/@Ada/T-1a2b3c4d5e6f?x=1":    "https://medium.com/feed/@ada",
		"https://pub.medium.com/@ada/t-1a2b3c4d5e6f":    "https://medium.com/feed/@ada",
		"https://ada.hashnode.dev/post?utm_source=feed": "https://ada.hashnode.dev/post",
	} {
		p := stand(t)
		p.m[want] = reply{Status: 404}
		tg, err := Locate(url)
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		if _, err := Read(context.Background(), tg); err != nil || !slices.Equal(p.calls, []string{want}) {
			t.Errorf("%s fetched %v %v, want only %s", url, p.calls, err, want)
		}
	}
}

// TestLongFormLinksKeepOnlyThoseAReferralCanBe: an article holds more links than the
// reader keeps, so the links of no use to Ref are dropped and cannot push the
// member's link out past the twelfth.
func TestLongFormLinksKeepOnlyThoseAReferralCanBe(t *testing.T) {
	var body string
	for i := range 30 {
		body += fmt.Sprintf(`<a href="https://example.com/%d">%d</a> `, i, i)
	}
	body += `<a href="https://hanzo.ai/beta?ref=ADA1">Hanzo</a>`
	for name, tc := range map[string]struct {
		url string
		rep reply
	}{
		"devto":    {"https://dev.to/ada/my-post-1abc", devtoPost("ada", "my-post-1abc", body, "")},
		"medium":   {"https://medium.com/@ada/my-post-1a2b3c4d5e6f", mediumFeed("ada", mediumItem("https://medium.com/@ada/my-post-1a2b3c4d5e6f", "https://medium.com/p/1a2b3c4d5e6f", "Mon, 26 Oct 2026 12:00:00 GMT", body))},
		"hashnode": {"https://ada.hashnode.dev/my-post", hashnodePage("https://ada.hashnode.dev/my-post", "Ada", "", "", body)},
	} {
		p := stand(t)
		tg := p.serve(t, tc.url, tc.rep)
		got, err := Read(context.Background(), tg)
		if err != nil || !slices.Equal(got.Links, []string{"https://hanzo.ai/beta?ref=ADA1"}) {
			t.Errorf("%s: links = %q %v, want only the member's", name, got.Links, err)
			continue
		}
		if found, err := Ref(context.Background(), got, "ADA1"); err != nil || !found {
			t.Errorf("%s: Ref = %v %v", name, found, err)
		}
	}
}

// TestReadMapsEveryStatusTheSameWayOnTheNewNetworks: missing is Gone, forbidden is
// not public, and a status that says nothing about the post is an error to try
// again later, as on every other network.
func TestReadMapsEveryStatusTheSameWayOnTheNewNetworks(t *testing.T) {
	for _, u := range []string{
		"https://dev.to/ada/my-post-1abc",
		"https://medium.com/@ada/my-post-1a2b3c4d5e6f",
		"https://ada.hashnode.dev/my-post",
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
		p := stand(t)
		tg := p.serve(t, u, reply{})
		_, api, _ := plan(u)
		p.err[api] = errors.New("tls: handshake failure")
		if got, err := Read(context.Background(), tg); err == nil || errors.Is(err, ErrRefused) {
			t.Errorf("%s, a transport failure = %+v %v", u, got, err)
		}
	}
}

// ── Ref ───────────────────────────────────────────────────────────────────────

// TestADevtoArticleThatLinksThroughTCoCarriesTheMembersRef: Read, then Ref, on a DEV
// article with a short link and a direct one, end to end through the canned network.
func TestADevtoArticleThatLinksThroughTCoCarriesTheMembersRef(t *testing.T) {
	p := stand(t)
	tg := p.serve(t, "https://dev.to/ada/my-post-1abc", devtoPost("ada", "my-post-1abc",
		`<p>I build with <a href="https://t.co/q">Hanzo</a>, and <a href="https://app.hanzo.ai/x?ref=DIRECT">here</a>.</p>`, "2026-10-26T12:00:00Z"))
	p.m["https://t.co/q"] = reply{Status: 301, Location: "https://hanzo.ai/beta?ref=ADA1"}
	page, err := Read(context.Background(), tg)
	if err != nil || !page.Public || page.Author != "ada" || !page.Published.Equal(published) {
		t.Fatalf("read = %+v %v", page, err)
	}
	for code, want := range map[string]bool{"ADA1": true, "ada1": true, "DIRECT": true, "EVE1": false} {
		if got, err := Ref(context.Background(), page, code); err != nil || got != want {
			t.Errorf("Ref(%s) = %v %v, want %v", code, got, err, want)
		}
	}
	// The same article, edited to point the short link at a look-alike site.
	p.m["https://t.co/q"] = reply{Status: 301, Location: "https://hanzo.ai.evil.com/beta?ref=ADA1"}
	if got, err := Ref(context.Background(), page, "ADA1"); err != nil || got {
		t.Fatalf("a look-alike host = %v %v", got, err)
	}
}

// TestAHashnodePageCarriesTheMembersRefOnlyOnHanzoAI: the anchors of the whole page
// are read, so a link that is the member's ref but on another site, or on hanzo.ai
// with another member's code, does not count.
func TestAHashnodePageCarriesTheMembersRefOnlyOnHanzoAI(t *testing.T) {
	for body, want := range map[string]bool{
		`<a href="https://hanzo.ai/beta?ref=ADA1">x</a>`:          true,
		`<a href="https://app.hanzo.ai/?ref=ada1">x</a>`:          true,
		`<a href="https://hanzo.ai/beta?ref=EVE1">x</a>`:          false,
		`<a href="https://example.com/beta?ref=ADA1">x</a>`:       false,
		`<a href="https://hanzo.ai.evil.com/beta?ref=ADA1">x</a>`: false,
		`no link`: false,
	} {
		p := stand(t)
		tg := p.serve(t, "https://ada.hashnode.dev/my-post", hashnodePage("https://ada.hashnode.dev/my-post", "Ada", "", "", body))
		page, err := Read(context.Background(), tg)
		if err != nil || !page.Public {
			t.Fatalf("read = %+v %v", page, err)
		}
		if got, err := Ref(context.Background(), page, "ADA1"); err != nil || got != want {
			t.Errorf("%s: Ref = %v %v, want %v", body, got, err, want)
		}
	}
}

// ── Recent ────────────────────────────────────────────────────────────────────

const devtoList = "https://dev.to/api/articles?username=ada_1&per_page=25"

// TestRecentDevtoListsTheUsersOwnArticlesNewestFirst: each as the address Locate
// makes of it; an article by another author (an organization's) and a repeat are
// left out.
func TestRecentDevtoListsTheUsersOwnArticlesNewestFirst(t *testing.T) {
	p := stand(t)
	article := func(path, user string) map[string]any {
		return map[string]any{"path": path, "user": map[string]any{"username": user}}
	}
	p.m[devtoList] = ok([]any{
		article("/Ada_1/Newest-1abc", "ada_1"),
		article("/ada_1/older-2def", "Ada_1"),
		article("/someorg/their-post-3ghi", "eve"),
		article("/ada_1/older-2def", "ada_1"),
		article("/ada_1/../x", "ada_1"),
		article("/ada_1/a/b", "ada_1"),
	})
	got, err := Recent(context.Background(), "devto", "ada_1")
	want := []string{"https://dev.to/ada_1/newest-1abc", "https://dev.to/ada_1/older-2def"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("Recent = %q %v, want %q", got, err, want)
	}
	recentIsReadable(t, "devto", got)
	p.m[devtoList] = ok([]any{})
	if got, err := Recent(context.Background(), "devto", "ada_1"); err != nil || len(got) != 0 {
		t.Fatalf("a user with no articles = %q %v", got, err)
	}
	p.m[devtoList] = reply{Status: 404}
	if got, err := Recent(context.Background(), "devto", "ada_1"); err != nil || len(got) != 0 {
		t.Fatalf("an unknown user = %q %v", got, err)
	}
	var many []any
	for i := range 40 {
		many = append(many, article(fmt.Sprintf("/ada_1/post-%d", i), "ada_1"))
	}
	p.m[devtoList] = ok(many)
	if got, err := Recent(context.Background(), "devto", "ada_1"); err != nil || len(got) != 25 {
		t.Fatalf("forty articles = %d urls %v, want 25", len(got), err)
	}
}
