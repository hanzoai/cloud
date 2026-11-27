package social

import (
	"errors"
	"strings"
	"testing"
)

// TestLocateNamesEachNetworksPostOnce: every spelling of a post's address on a
// network becomes one Target, with one Key and one canonical URL, and the API
// address it is read at is one the guard fetches.
func TestLocateNamesEachNetworksPostOnce(t *testing.T) {
	for _, tc := range []struct {
		in       string
		want     Target
		readable bool
	}{
		{"https://x.com/ada/status/123?s=20", Target{"x", "123", "https://x.com/ada/status/123", false}, true},
		{"https://twitter.com/Ada_1/statuses/123/photo/1", Target{"x", "123", "https://x.com/ada_1/status/123", false}, true},
		{"https://mobile.twitter.com/ada/status/9", Target{"x", "9", "https://x.com/ada/status/9", false}, true},
		{"https://www.x.com/ada/status/9/", Target{"x", "9", "https://x.com/ada/status/9", false}, true},
		{"https://bsky.app/profile/ada.bsky.social/post/3kabc123xyz", Target{"bluesky", "ada.bsky.social/3kabc123xyz", "https://bsky.app/profile/ada.bsky.social/post/3kabc123xyz", false}, true},
		{"https://bsky.app/profile/Ada.Bsky.Social/post/3kabc123xyz/", Target{"bluesky", "ada.bsky.social/3kabc123xyz", "https://bsky.app/profile/ada.bsky.social/post/3kabc123xyz", false}, true},
		{"https://bsky.app/profile/did:plc:abcdefghijklmnopqrstuvwx/post/3kabc123xyz", Target{"bluesky", "did:plc:abcdefghijklmnopqrstuvwx/3kabc123xyz", "https://bsky.app/profile/did:plc:abcdefghijklmnopqrstuvwx/post/3kabc123xyz", false}, true},
		{"https://old.reddit.com/r/LocalLLaMA/comments/1abc2de/my_post_title/", Target{"reddit", "1abc2de", "https://www.reddit.com/r/localllama/comments/1abc2de/", false}, true},
		{"https://reddit.com/r/ab/comments/1ABC2DE", Target{"reddit", "1abc2de", "https://www.reddit.com/r/ab/comments/1abc2de/", false}, true},
		{"https://news.ycombinator.com/item?id=41000000", Target{"hn", "41000000", "https://news.ycombinator.com/item?id=41000000", false}, true},
		{"https://hachyderm.io/@ada/112233445566", Target{"mastodon", "hachyderm.io/112233445566", "https://hachyderm.io/@ada/112233445566", false}, true},
		{"https://mastodon.social/@Ada@Fosstodon.org/998877", Target{"mastodon", "mastodon.social/998877", "https://mastodon.social/@ada@fosstodon.org/998877", false}, true},
		{"https://fosstodon.org/users/ada/statuses/55", Target{"mastodon", "fosstodon.org/55", "https://fosstodon.org/@ada/55", false}, true},
		{"https://www.linkedin.com/posts/ada_x-activity-7123456789012345678-AbCd?utm=1", Target{"linkedin", "7123456789012345678", "https://www.linkedin.com/posts/ada_x-activity-7123456789012345678-AbCd", true}, false},
		{"https://linkedin.com/feed/update/urn:li:activity:7123456789012345678/", Target{"linkedin", "7123456789012345678", "https://www.linkedin.com/feed/update/urn:li:activity:7123456789012345678/", true}, false},
		{"  https://x.com/ada/status/123  ", Target{"x", "123", "https://x.com/ada/status/123", false}, true},
	} {
		got, api, err := plan(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %+v, want %+v", tc.in, got, tc.want)
		}
		if pub, err := Locate(tc.in); err != nil || pub != got {
			t.Errorf("Locate(%s) = %+v %v, want %+v", tc.in, pub, err, got)
		}
		if (api != "") != tc.readable {
			t.Errorf("%s: api %q, readable %v", tc.in, api, tc.readable)
		}
		if api != "" {
			u, err := admit(api)
			if err != nil || !fetchable(u.Hostname()) {
				t.Errorf("%s: its API address %s is not one the guard fetches: %v", tc.in, api, err)
			}
		}
		// The canonical address is a fixed point: locating it gives the same Target.
		if again, err := Locate(got.URL); err != nil || again != got {
			t.Errorf("%s: locating its canonical %s gave %+v %v", tc.in, got.URL, again, err)
		}
	}
}

// TestLinkedInIsLocatedForReviewAndNeverFetched: LinkedIn is the network the
// reader cannot read, so its Target says Review and names no API address.
func TestLinkedInIsLocatedForReviewAndNeverFetched(t *testing.T) {
	for _, in := range []string{
		"https://www.linkedin.com/posts/ada_x-activity-7123456789012345678-AbCd",
		"https://www.linkedin.com/feed/update/urn:li:share:7123456789012345678",
		"https://www.linkedin.com/feed/update/urn:li:ugcPost:7123456789012345678/",
	} {
		got, api, err := plan(in)
		if err != nil || !got.Review || got.Network != "linkedin" || got.Key != "7123456789012345678" || api != "" {
			t.Errorf("%s = %+v %q %v", in, got, api, err)
		}
	}
	// With no id in the address, the key is a hash of the path, the same every time.
	a, _ := Locate("https://www.linkedin.com/posts/ada_hello-world-AbCd")
	b, _ := Locate("https://linkedin.com/posts/ada_hello-world-AbCd?utm_source=x")
	c, _ := Locate("https://linkedin.com/posts/ada_hello-world-ZzZz")
	if a.Key == "" || a.Key != b.Key || a.Key == c.Key || !a.Review {
		t.Errorf("hashed keys: %+v %+v %+v", a, b, c)
	}
	for _, in := range []string{
		"https://www.linkedin.com/in/ada",
		"https://www.linkedin.com/company/hanzo",
		"https://www.linkedin.com/posts/",
		"https://www.linkedin.com/posts/a/b/c",
		"https://www.linkedin.com/feed/",
	} {
		if got, err := Locate(in); !errors.Is(err, ErrRefused) {
			t.Errorf("Locate(%q) = %+v %v, want a refusal", in, got, err)
		}
	}
}

// TestLocateRefusesWhatIsNotOnePost: unknown hosts, look-alike hosts,
// credentials, IPs, ports, other schemes and addresses that are not one post are
// all refused, and none of them is ever fetched.
func TestLocateRefusesWhatIsNotOnePost(t *testing.T) {
	p := stand(t)
	for _, in := range []string{
		"",
		"nope",
		"http://x.com/a/status/1",
		"//x.com/a/status/1",
		"https://x.com/a",
		"https://x.com/a/status/",
		"https://x.com/a/status/abc",
		"https://x.com/a/status/0123",
		"https://x.com/waytoolongusernamehere/status/1",
		"https://x.com@evil.com/a/status/1",
		"https://twitter.com@evil.com/a/status/1",
		"https://user:pw@x.com/a/status/1",
		"https://x.com.evil.com/a/status/1",
		"https://evilx.com/a/status/1",
		"https://evil.com/x.com/a/status/1",
		"https://notbsky.app/profile/a.b/post/3kabc123xyz",
		"https://bsky.app.evil.com/profile/a.b/post/3kabc123xyz",
		"https://evil.com/a/status/1",
		"https://evil.com/item?id=1",
		"https://evil.com/@ada/1",
		"https://evil.com/profile/a.b/post/3kabc123xyz",
		"https://127.0.0.1/a/status/1",
		"https://[::1]/a/status/1",
		"https://169.254.169.254/latest/meta-data/",
		"https://x.com:8443/a/status/1",
		"https://x.com./a/status/1",
		"https://X.com/a/status/1",
		"https://ｘ.com/a/status/1",
		"https://x.com/a/status/1\x00",
		"https://bsky.app/profile/a/post/x",
		"https://bsky.app/profile/a.b/post/3kabc123xyz/extra",
		"https://bsky.app/profile/../post/3kabc123xyz",
		"https://bsky.app/profile/a..b/post/3kabc123xyz",
		"https://bsky.app/profile/-a.b/post/3kabc123xyz",
		"https://bsky.app/profile/did:plc:short/post/3kabc123xyz",
		"https://www.reddit.com/r/ab/comments/1abc2de/t/c1/",
		"https://www.reddit.com/user/ada/",
		"https://redd.it/1abc2de",
		"https://news.ycombinator.com/item?id=abc",
		"https://news.ycombinator.com/item?id=041",
		"https://news.ycombinator.com/item?id=",
		"https://news.ycombinator.com/user?id=ada",
		"https://news.ycombinator.com.evil.com/item?id=1",
		"https://mastodon.evil.example/@ada/1",
		"https://evilmastodon.social/@ada/1",
		"https://sub.mastodon.social/@ada/1",
		"https://mastodon.social/@ada",
		"https://mastodon.social/@../1",
		"https://mastodon.social/@ada/0123",
		"https://mastodon.social/@ada@evil/1",
		"https://mastodon.social/@ada@../1",
		"https://www.facebook.com/ada/posts/pfbid0abc",
		"https://facebook.com/ada/posts/pfbid0abc",
		"https://t.co/abc",
		"https://hanzo.ai/beta?ref=ADA1",
		"https://example.com/",
		"https://x.com/a/status/1" + strings.Repeat("0", 2100),
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

// TestTwoSpellingsOfOnePostAreOneKey: leading zeros, letter case and the
// twitter.com spelling do not make a second claim of the same post.
func TestTwoSpellingsOfOnePostAreOneKey(t *testing.T) {
	for _, group := range [][]string{
		{"https://x.com/ada/status/123", "https://twitter.com/ADA/status/123", "https://mobile.twitter.com/Ada/statuses/123/", "https://x.com/someone_else/status/123?s=1"},
		{"https://bsky.app/profile/ada.bsky.social/post/3kabc123xyz", "https://bsky.app/profile/ADA.bsky.social/post/3kabc123xyz/"},
		{"https://www.reddit.com/r/ab/comments/1abc2de/", "https://old.reddit.com/r/AB/comments/1ABC2DE/title/", "https://np.reddit.com/r/ab/comments/1abc2de"},
		{"https://news.ycombinator.com/item?id=41", "https://news.ycombinator.com/item?id=41&p=2"},
		{"https://hachyderm.io/@ada/77", "https://hachyderm.io/@Ada/77/", "https://hachyderm.io/users/ada/statuses/77"},
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
	// A post id is only unique within its author on Bluesky and its instance on
	// Mastodon: the same id on another author or instance is another post.
	a, _ := Locate("https://bsky.app/profile/ada.bsky.social/post/3kabc123xyz")
	b, _ := Locate("https://bsky.app/profile/eve.bsky.social/post/3kabc123xyz")
	m, _ := Locate("https://hachyderm.io/@ada/77")
	n, _ := Locate("https://fosstodon.org/@ada/77")
	if a.Key == b.Key || m.Key == n.Key {
		t.Fatalf("keys collide across authors: %q %q, %q %q", a.Key, b.Key, m.Key, n.Key)
	}
}
