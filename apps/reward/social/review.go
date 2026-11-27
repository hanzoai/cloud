package social

// review.go - TikTok, YouTube, Threads, Instagram and Facebook: the networks whose
// posts cannot be proven from public data: they need the member's own sign-in
// there or a key Hanzo does not hold, or (TikTok) the public oEmbed names neither
// the caption nor the author, so it cannot show the member's link. Each is located
// like LinkedIn: named once, with the network's own id for the post as its Key,
// never fetched, and marked Review so a maintainer confirms it. An address on any
// other site is not a network (a blog is refused).
//
// The ids are case-sensitive, so they keep their case; the host and the account
// names, which are not, are lower-case.

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	tiktokHosts    = []string{"tiktok.com", "www.tiktok.com", "m.tiktok.com"}
	youtubeHosts   = []string{"youtube.com", "www.youtube.com", "m.youtube.com"}
	threadsHosts   = []string{"threads.net", "www.threads.net", "threads.com", "www.threads.com"}
	instagramHosts = []string{"instagram.com", "www.instagram.com"}
	facebookHosts  = []string{"facebook.com", "www.facebook.com", "m.facebook.com", "web.facebook.com"}

	tiktokPath  = regexp.MustCompile(`^/@([A-Za-z0-9_.]{1,24})/video/([1-9][0-9]{0,19})/?$`)
	tiktokShort = regexp.MustCompile(`^/t/[A-Za-z0-9]{1,32}/?$`)

	// errTikTokShort is a TikTok short link, which names its video only by
	// following a redirect, and so is not followed.
	errTikTokShort = fmt.Errorf("%w: tiktok short links are not followed: paste the full video address, tiktok.com/@user/video/<number>", ErrRefused)

	// youtubeID is a video id: eleven characters of the base64url alphabet.
	youtubeID    = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	youtubeShort = regexp.MustCompile(`^/shorts/([A-Za-z0-9_-]{11})/?$`)
	youtubeBe    = regexp.MustCompile(`^/([A-Za-z0-9_-]{11})/?$`)
	threadsPath  = regexp.MustCompile(`^/@([A-Za-z0-9_.]{1,30})/post/([A-Za-z0-9_-]{5,20})/?$`)
	// instagramPath is a post, reel or tv video, with or without the account before it.
	instagramPath = regexp.MustCompile(`^(?:/[A-Za-z0-9_.]{1,30})?/(p|reels?|tv)/([A-Za-z0-9_-]{5,20})/?$`)

	// fbStory is a post's id on Facebook: a number, or a pfbid.
	fbStory     = regexp.MustCompile(`^(?:pfbid[A-Za-z0-9]{10,100}|[0-9]{5,25})$`)
	fbNumber    = regexp.MustCompile(`^[0-9]{5,25}$`)
	fbPostPath  = regexp.MustCompile(`^/([A-Za-z0-9.]{1,100})/(posts|videos)/(pfbid[A-Za-z0-9]{10,100}|[0-9]{5,25})/?$`)
	fbReelPath  = regexp.MustCompile(`^/reel/([0-9]{5,25})/?$`)
	fbSharePath = regexp.MustCompile(`^/share/([pvr])/([A-Za-z0-9]{5,40})/?$`)
	fbWatchPath = regexp.MustCompile(`^/([A-Za-z0-9_-]{5,40})/?$`)
)

// tiktok locates a video: tiktok.com/@user/video/<id>.
func tiktok(p string) (Target, string, error) {
	if tiktokShort.MatchString(p) {
		return Target{}, "", errTikTokShort
	}
	m := tiktokPath.FindStringSubmatch(p)
	if m == nil {
		return Target{}, "", errPaste
	}
	return Target{Network: "tiktok", Key: m[2], URL: "https://www.tiktok.com/@" + strings.ToLower(m[1]) + "/video/" + m[2], Review: true}, "", nil
}

// youtube locates a video: watch?v=<id>, shorts/<id>, or youtu.be/<id>.
func youtube(host string, u *url.URL) (Target, string, error) {
	switch {
	case host == "youtu.be":
		if m := youtubeBe.FindStringSubmatch(u.Path); m != nil {
			return Target{Network: "youtube", Key: m[1], URL: "https://www.youtube.com/watch?v=" + m[1], Review: true}, "", nil
		}
	case u.Path == "/watch":
		if v := u.Query()["v"]; len(v) == 1 && youtubeID.MatchString(v[0]) {
			return Target{Network: "youtube", Key: v[0], URL: "https://www.youtube.com/watch?v=" + v[0], Review: true}, "", nil
		}
	default:
		if m := youtubeShort.FindStringSubmatch(u.Path); m != nil {
			return Target{Network: "youtube", Key: m[1], URL: "https://www.youtube.com/shorts/" + m[1], Review: true}, "", nil
		}
	}
	return Target{}, "", errPaste
}

// threads locates a post: @<user>/post/<code>, on threads.net or threads.com.
func threads(p string) (Target, string, error) {
	m := threadsPath.FindStringSubmatch(p)
	if m == nil {
		return Target{}, "", errPaste
	}
	return Target{Network: "threads", Key: m[2], URL: "https://www.threads.com/@" + strings.ToLower(m[1]) + "/post/" + m[2], Review: true}, "", nil
}

// instagram locates a post, reel or tv video by its shortcode.
func instagram(p string) (Target, string, error) {
	m := instagramPath.FindStringSubmatch(p)
	if m == nil {
		return Target{}, "", errPaste
	}
	kind := m[1]
	if kind == "reels" {
		kind = "reel"
	}
	return Target{Network: "instagram", Key: m[2], URL: "https://www.instagram.com/" + kind + "/" + m[2] + "/", Review: true}, "", nil
}

// facebook locates a post or video: <page>/posts/<id>, <page>/videos/<id>,
// permalink.php?story_fbid=<id>&id=<page>, watch/?v=<id>, reel/<id>, share/<p|v|r>/<code>,
// or fb.watch/<code>. A code that is not an id is kept apart from one that is.
func facebook(host string, u *url.URL) (Target, string, error) {
	post := func(key, canon string) (Target, string, error) {
		return Target{Network: "facebook", Key: key, URL: canon, Review: true}, "", nil
	}
	q := u.Query()
	switch {
	case host == "fb.watch":
		if m := fbWatchPath.FindStringSubmatch(u.Path); m != nil {
			return post("fb.watch/"+m[1], "https://fb.watch/"+m[1]+"/")
		}
	case u.Path == "/permalink.php":
		if s, id := q["story_fbid"], q["id"]; len(s) == 1 && len(id) == 1 && fbStory.MatchString(s[0]) && fbNumber.MatchString(id[0]) {
			return post(s[0], "https://www.facebook.com/permalink.php?story_fbid="+s[0]+"&id="+id[0])
		}
	case u.Path == "/watch" || u.Path == "/watch/":
		if v := q["v"]; len(v) == 1 && fbNumber.MatchString(v[0]) {
			return post(v[0], "https://www.facebook.com/watch/?v="+v[0])
		}
	default:
		if m := fbPostPath.FindStringSubmatch(u.Path); m != nil {
			return post(m[3], "https://www.facebook.com/"+strings.ToLower(m[1])+"/"+m[2]+"/"+m[3])
		}
		if m := fbReelPath.FindStringSubmatch(u.Path); m != nil {
			return post(m[1], "https://www.facebook.com/reel/"+m[1])
		}
		if m := fbSharePath.FindStringSubmatch(u.Path); m != nil {
			return post("share/"+m[2], "https://www.facebook.com/share/"+m[1]+"/"+m[2]+"/")
		}
	}
	return Target{}, "", errPaste
}
