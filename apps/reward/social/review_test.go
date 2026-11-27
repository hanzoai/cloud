package social

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestReviewNetworksAreLocatedForReviewAndNeverFetched: TikTok, YouTube, Threads,
// Instagram and Facebook are named once, by the network's own id for the post,
// marked Review, and given no API address, as LinkedIn is.
func TestReviewNetworksAreLocatedForReviewAndNeverFetched(t *testing.T) {
	const yt = "dQw4w9WgXcQ"
	const tt = "7123456789012345678"
	for _, tc := range []struct {
		in   string
		want Target
	}{
		{"https://www.tiktok.com/@Ada.B/video/" + tt + "?lang=en&is_copy_url=1", Target{"tiktok", tt, "https://www.tiktok.com/@ada.b/video/" + tt, true}},
		{"https://tiktok.com/@ada.b/video/" + tt + "/", Target{"tiktok", tt, "https://www.tiktok.com/@ada.b/video/" + tt, true}},
		{"https://m.tiktok.com/@ada.b/video/" + tt, Target{"tiktok", tt, "https://www.tiktok.com/@ada.b/video/" + tt, true}},
		{"https://www.tiktok.com/@ada/video/9?url=https://evil.com/", Target{"tiktok", "9", "https://www.tiktok.com/@ada/video/9", true}},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10s&list=PL1", Target{"youtube", yt, "https://www.youtube.com/watch?v=" + yt, true}},
		{"https://youtube.com/watch?v=dQw4w9WgXcQ", Target{"youtube", yt, "https://www.youtube.com/watch?v=" + yt, true}},
		{"https://m.youtube.com/watch?feature=share&v=dQw4w9WgXcQ", Target{"youtube", yt, "https://www.youtube.com/watch?v=" + yt, true}},
		{"https://youtu.be/dQw4w9WgXcQ?si=abc", Target{"youtube", yt, "https://www.youtube.com/watch?v=" + yt, true}},
		{"https://youtu.be/dQw4w9WgXcQ/", Target{"youtube", yt, "https://www.youtube.com/watch?v=" + yt, true}},
		{"https://www.youtube.com/shorts/dQw4w9WgXcQ", Target{"youtube", yt, "https://www.youtube.com/shorts/" + yt, true}},
		{"https://threads.net/@Ada.B/post/C8x7Yz_pQ1a?xmt=1", Target{"threads", "C8x7Yz_pQ1a", "https://www.threads.com/@ada.b/post/C8x7Yz_pQ1a", true}},
		{"https://www.threads.com/@ada/post/C8x7Yz_pQ1a/", Target{"threads", "C8x7Yz_pQ1a", "https://www.threads.com/@ada/post/C8x7Yz_pQ1a", true}},
		{"https://www.instagram.com/p/CxYz1AbCdEf/?igsh=abc", Target{"instagram", "CxYz1AbCdEf", "https://www.instagram.com/p/CxYz1AbCdEf/", true}},
		{"https://instagram.com/reel/CxYz1AbCdEf", Target{"instagram", "CxYz1AbCdEf", "https://www.instagram.com/reel/CxYz1AbCdEf/", true}},
		{"https://www.instagram.com/reels/CxYz1AbCdEf/", Target{"instagram", "CxYz1AbCdEf", "https://www.instagram.com/reel/CxYz1AbCdEf/", true}},
		{"https://www.instagram.com/tv/CxYz1AbCdEf/", Target{"instagram", "CxYz1AbCdEf", "https://www.instagram.com/tv/CxYz1AbCdEf/", true}},
		{"https://www.instagram.com/ada.b/p/CxYz1AbCdEf/", Target{"instagram", "CxYz1AbCdEf", "https://www.instagram.com/p/CxYz1AbCdEf/", true}},
		{"https://www.facebook.com/Ada.Page/posts/pfbid0AbCdEfGhIjKlMn?ref=x", Target{"facebook", "pfbid0AbCdEfGhIjKlMn", "https://www.facebook.com/ada.page/posts/pfbid0AbCdEfGhIjKlMn", true}},
		{"https://m.facebook.com/ada/posts/10161234567890123/", Target{"facebook", "10161234567890123", "https://www.facebook.com/ada/posts/10161234567890123", true}},
		{"https://web.facebook.com/ada/videos/123456789012345", Target{"facebook", "123456789012345", "https://www.facebook.com/ada/videos/123456789012345", true}},
		{"https://www.facebook.com/permalink.php?story_fbid=10161234567890123&id=100012345678901", Target{"facebook", "10161234567890123", "https://www.facebook.com/permalink.php?story_fbid=10161234567890123&id=100012345678901", true}},
		{"https://facebook.com/watch/?v=123456789012345&ref=x", Target{"facebook", "123456789012345", "https://www.facebook.com/watch/?v=123456789012345", true}},
		{"https://www.facebook.com/watch?v=123456789012345", Target{"facebook", "123456789012345", "https://www.facebook.com/watch/?v=123456789012345", true}},
		{"https://www.facebook.com/reel/123456789012345", Target{"facebook", "123456789012345", "https://www.facebook.com/reel/123456789012345", true}},
		{"https://www.facebook.com/share/p/1AbCdEfGhI/", Target{"facebook", "share/1AbCdEfGhI", "https://www.facebook.com/share/p/1AbCdEfGhI/", true}},
		{"https://www.facebook.com/share/v/1AbCdEfGhI", Target{"facebook", "share/1AbCdEfGhI", "https://www.facebook.com/share/v/1AbCdEfGhI/", true}},
		{"https://fb.watch/AbC_d-EfGh/", Target{"facebook", "fb.watch/AbC_d-EfGh", "https://fb.watch/AbC_d-EfGh/", true}},
	} {
		got, api, err := plan(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if got != tc.want || api != "" {
			t.Errorf("%s = %+v %q, want %+v and no API address", tc.in, got, api, tc.want)
		}
		if pub, err := Locate(tc.in); err != nil || pub != got {
			t.Errorf("Locate(%s) = %+v %v, want %+v", tc.in, pub, err, got)
		}
		if again, err := Locate(got.URL); err != nil || again != got {
			t.Errorf("%s: locating its canonical %s gave %+v %v", tc.in, got.URL, again, err)
		}
	}
}

// TestTwoSpellingsOfOneTikTokVideoAreOneKey: the hosts, the case of the account, a
// trailing slash and a query do not make a second claim of the video, and the
// account the address names is not part of its key.
func TestTwoSpellingsOfOneTikTokVideoAreOneKey(t *testing.T) {
	group := []string{"https://www.tiktok.com/@ada/video/77", "https://tiktok.com/@ADA/video/77/", "https://m.tiktok.com/@ada/video/77?lang=en", "https://www.tiktok.com/@someone/video/77"}
	first, err := Locate(group[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range group[1:] {
		if got, err := Locate(in); err != nil || got.Key != first.Key || got.Network != first.Network {
			t.Errorf("%s = %+v %v, want the key %q of %s", in, got, err, first.Key, group[0])
		}
	}
}

// TestReviewIdsKeepTheirCaseAndTheHostDoesNot: an id the network spells with
// capitals is a different id in another case; two spellings of one address are one key.
func TestReviewIdsKeepTheirCaseAndTheHostDoesNot(t *testing.T) {
	a, _ := Locate("https://www.youtube.com/watch?v=aBcDeFgHiJk")
	b, _ := Locate("https://www.youtube.com/watch?v=ABCDEFGHIJK")
	c, _ := Locate("https://youtu.be/aBcDeFgHiJk")
	d, _ := Locate("https://www.instagram.com/p/AbCdE12345/")
	e, _ := Locate("https://www.instagram.com/p/abcde12345/")
	if a.Key != "aBcDeFgHiJk" || a.Key == b.Key || a.Key != c.Key || d.Key != "AbCdE12345" || d.Key == e.Key {
		t.Fatalf("keys = %q %q %q %q %q", a.Key, b.Key, c.Key, d.Key, e.Key)
	}
	for _, in := range []string{"https://WWW.youtube.com/watch?v=aBcDeFgHiJk", "https://Instagram.com/p/AbCdE12345/", "https://YOUTU.BE/aBcDeFgHiJk"} {
		if got, err := Locate(in); !errors.Is(err, ErrRefused) {
			t.Errorf("Locate(%q) = %+v %v, want a refusal of an upper-case host", in, got, err)
		}
	}
}

// TestReviewNetworksRefuseLookAlikesAndWhatIsNotOnePost: look-alike hosts,
// credentials, ports, other schemes, query-only tricks and addresses that are not
// one post are refused, and none is fetched. A blog on its own domain is not a
// network.
func TestReviewNetworksRefuseLookAlikesAndWhatIsNotOnePost(t *testing.T) {
	p := stand(t)
	for _, in := range []string{
		// TikTok
		"https://tiktok.com.evil.com/@a/video/1",
		"https://www.tiktok.com.evil.com/@a/video/1",
		"https://evil.com/www.tiktok.com/@a/video/1",
		"https://eviltiktok.com/@a/video/1",
		"https://www.tiktok.com@evil.com/@a/video/1",
		"https://user:pw@www.tiktok.com/@a/video/1",
		"https://www.tiktok.com:8443/@a/video/1",
		"http://www.tiktok.com/@a/video/1",
		"https://WWW.tiktok.com/@a/video/1",
		"https://www.tiktok.com/@a/video/0123",
		"https://www.tiktok.com/@a/video/abc",
		"https://www.tiktok.com/@a/video/",
		"https://www.tiktok.com/@a",
		"https://www.tiktok.com/@a/photo/123",
		"https://www.tiktok.com/@a/video/123/extra",
		"https://www.tiktok.com/@waytoolongusernamethatisnotone/video/1",
		"https://www.tiktok.com/oembed?url=https%3A%2F%2Fwww.tiktok.com%2F%40a%2Fvideo%2F1",
		"https://www.tiktok.com/@a%2Fb/video/1",
		"https://www.tiktok.com/@a/video/1" + strings.Repeat("0", 20),
		// YouTube
		"https://youtube.com.evil.com/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com.evil.com/watch?v=dQw4w9WgXcQ",
		"https://evil.com/www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://evilyoutube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be.evil.com/dQw4w9WgXcQ",
		"https://youtube.com@evil.com/watch?v=dQw4w9WgXcQ",
		"https://user:pw@www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com:8443/watch?v=dQw4w9WgXcQ",
		"http://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com/watch",
		"https://www.youtube.com/watch?v=",
		"https://www.youtube.com/watch?v=short",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&v=AAAAAAAAAAA",
		"https://www.youtube.com/watch?x=dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXc%2F",
		"https://www.youtube.com/?u=https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com/watch/dQw4w9WgXcQ",
		"https://www.youtube.com/@ada",
		"https://www.youtube.com/playlist?list=PL1",
		"https://www.youtube.com/shorts/short",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ/extra",
		"https://youtu.be/",
		"https://youtu.be/short",
		"https://youtu.be/dQw4w9WgXcQ/extra",
		// Threads
		"https://threads.net.evil.com/@ada/post/C8x7Yz_pQ1a",
		"https://evilthreads.com/@ada/post/C8x7Yz_pQ1a",
		"https://threads.com@evil.com/@ada/post/C8x7Yz_pQ1a",
		"https://threads.net:8443/@ada/post/C8x7Yz_pQ1a",
		"https://threads.net/@ada",
		"https://threads.net/@ada/post/",
		"https://threads.net/@ada/post/x",
		"https://threads.net/ada/post/C8x7Yz_pQ1a",
		"https://threads.net/@ada/post/C8x7Yz_pQ1a/media",
		"https://threads.net/@ada/replies",
		// Instagram
		"https://instagram.com.evil.com/p/CxYz1AbCdEf/",
		"https://evilinstagram.com/p/CxYz1AbCdEf/",
		"https://instagram.com@evil.com/p/CxYz1AbCdEf/",
		"https://user:pw@www.instagram.com/p/CxYz1AbCdEf/",
		"https://www.instagram.com:8443/p/CxYz1AbCdEf/",
		"https://www.instagram.com/p/",
		"https://www.instagram.com/p/x/",
		"https://www.instagram.com/p/CxYz1AbCdEf/comments/",
		"https://www.instagram.com/ada/",
		"https://www.instagram.com/stories/ada/123456789/",
		"https://www.instagram.com/explore/tags/hanzo/",
		"https://www.instagram.com/?u=/p/CxYz1AbCdEf/",
		"https://www.instagram.com/a/b/c/p/CxYz1AbCdEf/",
		// Facebook
		"https://facebook.com.evil.com/ada/posts/10161234567890123",
		"https://www.facebook.com.evil.com/ada/posts/10161234567890123",
		"https://evilfacebook.com/ada/posts/10161234567890123",
		"https://facebook.com@evil.com/ada/posts/10161234567890123",
		"https://user:pw@www.facebook.com/ada/posts/10161234567890123",
		"https://www.facebook.com:8443/ada/posts/10161234567890123",
		"https://fb.watch.evil.com/AbCdEfGhI/",
		"https://fb.watch@evil.com/AbCdEfGhI/",
		"https://fb.watch/",
		"https://fb.watch/x",
		"https://fb.watch/AbCdEfGhI/extra",
		"https://www.facebook.com/ada",
		"https://www.facebook.com/ada/posts/",
		"https://www.facebook.com/ada/posts/abc",
		"https://www.facebook.com/ada/posts/pfbid0abc",
		"https://www.facebook.com/ada/posts/10161234567890123/extra",
		"https://www.facebook.com/ada/photos/123456789012345",
		"https://www.facebook.com/permalink.php",
		"https://www.facebook.com/permalink.php?story_fbid=10161234567890123",
		"https://www.facebook.com/permalink.php?story_fbid=abc&id=100012345678901",
		"https://www.facebook.com/permalink.php?story_fbid=10161234567890123&id=ada",
		"https://www.facebook.com/permalink.php?story_fbid=10161234567890123&story_fbid=10161234567890124&id=100012345678901",
		"https://www.facebook.com/watch/?v=abc",
		"https://www.facebook.com/watch/",
		"https://www.facebook.com/reel/abc",
		"https://www.facebook.com/share/x/1AbCdEfGhI/",
		"https://www.facebook.com/share/p/",
		"https://www.facebook.com/login",
		"https://www.facebook.com/?u=/ada/posts/10161234567890123",
		// not a network at all
		"https://blog.example.com/my-post",
		"https://myblog.example.com/2026/10/my-post",
		"https://ada.substack.com/p/my-post",
		"https://ada.wordpress.com/2026/10/my-post/",
		"https://example.com/watch?v=dQw4w9WgXcQ",
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

// TestTikTokShortLinksAreRefusedWithAWayOut: a short link names its video only by a
// redirect, which is not followed; the refusal says to paste the full address.
func TestTikTokShortLinksAreRefusedWithAWayOut(t *testing.T) {
	p := stand(t)
	for _, in := range []string{"https://vm.tiktok.com/ZMabc123/", "https://vt.tiktok.com/ZSabc123/", "https://www.tiktok.com/t/ZTabc123/", "https://tiktok.com/t/ZTabc123"} {
		got, err := Locate(in)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "paste the full video address") || got != (Target{}) {
			t.Errorf("Locate(%q) = %+v %v, want a refusal that asks for the full video address", in, got, err)
		}
	}
	if len(p.calls) != 0 {
		t.Fatalf("a short link was fetched: %v", p.calls)
	}
}

// TestReadNeverFetchesAReviewNetworkOrATargetBuiltByHand: a located TikTok, YouTube,
// Threads, Instagram or Facebook post is refused with nothing fetched, and so is
// one built by hand without the Review mark.
func TestReadNeverFetchesAReviewNetworkOrATargetBuiltByHand(t *testing.T) {
	p := stand(t)
	for _, in := range []string{
		"https://www.tiktok.com/@ada/video/7123456789012345678",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://www.threads.com/@ada/post/C8x7Yz_pQ1a",
		"https://www.instagram.com/p/CxYz1AbCdEf/",
		"https://www.facebook.com/ada/posts/10161234567890123",
		"https://fb.watch/AbCdEfGhI/",
	} {
		tg, err := Locate(in)
		if err != nil || !tg.Review {
			t.Fatalf("Locate(%s) = %+v %v", in, tg, err)
		}
		if got, err := Read(context.Background(), tg); !errors.Is(err, ErrRefused) || !same(got, Page{}) {
			t.Errorf("%s: Read = %+v %v, want a refusal", in, got, err)
		}
		hand := tg
		hand.Review = false
		if got, err := Read(context.Background(), hand); !errors.Is(err, ErrRefused) || !same(got, Page{}) {
			t.Errorf("%s without the mark: Read = %+v %v, want a refusal", in, got, err)
		}
	}
	if len(p.calls) != 0 {
		t.Fatalf("fetched %v", p.calls)
	}
}

// TestLocateNamesTheFourteenNetworks: the Network and Review of one post on each,
// as Locate returns them.
func TestLocateNamesTheFourteenNetworks(t *testing.T) {
	var got []string
	for _, in := range []string{
		"https://x.com/ada/status/1",
		"https://bsky.app/profile/ada.bsky.social/post/3kabc123xyz",
		"https://www.reddit.com/r/ab/comments/1abc2de/",
		"https://news.ycombinator.com/item?id=41",
		"https://hachyderm.io/@ada/77",
		"https://dev.to/ada/post-1abc",
		"https://medium.com/@ada/t-1a2b3c4d5e6f",
		"https://ada.hashnode.dev/post",
		"https://www.linkedin.com/posts/ada_x-activity-7123456789012345678-AbCd",
		"https://www.tiktok.com/@ada/video/77",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://www.threads.com/@ada/post/C8x7Yz_pQ1a",
		"https://www.instagram.com/p/CxYz1AbCdEf/",
		"https://www.facebook.com/ada/posts/10161234567890123",
	} {
		tg, err := Locate(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		name := tg.Network
		if tg.Review {
			name += "*"
		}
		got = append(got, name)
	}
	want := []string{"x", "bluesky", "reddit", "hn", "mastodon", "devto", "medium", "hashnode", "linkedin*", "tiktok*", "youtube*", "threads*", "instagram*", "facebook*"}
	if !slices.Equal(got, want) {
		t.Fatalf("networks = %q, want %q", got, want)
	}
	// The refusal of an address on none of them names the networks the reader knows.
	_, err := Locate("https://example.com/post")
	for _, name := range want {
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), strings.TrimSuffix(name, "*")) {
			t.Fatalf("the refusal %v does not name %s", err, name)
		}
	}
}
