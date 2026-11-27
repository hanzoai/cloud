package social

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Target is one post located: which network it is on and the one name it has
// there. Locate makes it; Read takes it.
type Target struct {
	// Network is one of "x", "bluesky", "reddit", "hn", "mastodon", "devto",
	// "medium", "hashnode" (read), or "linkedin", "tiktok", "youtube", "threads",
	// "instagram", "facebook" (never read: Review).
	Network string
	// Key is the network's own id for the post. One post has one Key, spelled
	// lower-case where the network is case-blind, so two spellings of an address
	// are one claim. Where the network's ids are only unique per author (Bluesky,
	// Mastodon) the Key carries the author's side too, so nobody can take a Key by
	// posting under another account.
	Key string
	// URL is the canonical https address of the post.
	URL string
	// Review is true for a network the reader cannot read (LinkedIn, TikTok,
	// YouTube, Threads, Instagram, Facebook): the post is never fetched and goes to a
	// maintainer.
	Review bool
}

var (
	xHosts    = []string{"x.com", "www.x.com", "mobile.x.com", "twitter.com", "www.twitter.com", "mobile.twitter.com"}
	redHosts  = []string{"reddit.com", "www.reddit.com", "old.reddit.com", "new.reddit.com", "np.reddit.com"}
	inHosts   = []string{"linkedin.com", "www.linkedin.com"}
	bskyHost  = "bsky.app"
	hnHost    = "news.ycombinator.com"
	xPath     = regexp.MustCompile(`^/([A-Za-z0-9_]{1,15})/status(?:es)?/([1-9][0-9]{0,19})(?:/.*)?$`)
	bskyPath  = regexp.MustCompile(`^/profile/([^/]{1,253})/post/([A-Za-z0-9]{6,20})/?$`)
	redPath   = regexp.MustCompile(`^/r/([A-Za-z0-9_]{2,21})/comments/([A-Za-z0-9]{5,10})(?:/[^/]*)?/?$`)
	hnID      = regexp.MustCompile(`^[1-9][0-9]{0,11}$`)
	mastoName = `[A-Za-z0-9_](?:[A-Za-z0-9_.-]{0,62}[A-Za-z0-9_])?`
	mastoID   = regexp.MustCompile(`^[1-9][0-9]{0,29}$`)
	mastoA    = regexp.MustCompile(`^/@(` + mastoName + `)(?:@([A-Za-z0-9.-]{1,253}))?/([1-9][0-9]{0,29})/?$`)
	mastoB    = regexp.MustCompile(`^/users/(` + mastoName + `)/statuses/([1-9][0-9]{0,29})/?$`)
	inPath    = regexp.MustCompile(`^/(?:posts|feed/update)/[A-Za-z0-9._~%:-]{1,300}/?$`)
	inID      = regexp.MustCompile(`(?:activity|share|ugcPost)[-:]([0-9]{10,25})`)
	// domain is a DNS name of at least two labels, lower-case: a Bluesky handle, or
	// the instance part of a Mastodon address.
	domain = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
	// did is the two identifier forms Bluesky allows in place of a handle.
	did = regexp.MustCompile(`^did:(?:plc:[a-z2-7]{24}|web:[a-z0-9.-]{1,253})$`)
)

// bskyActor reports whether s is a Bluesky handle or DID in the lower-case form
// the reader uses.
func bskyActor(s string) bool {
	return len(s) <= 253 && (domain.MatchString(s) || did.MatchString(s))
}

// Locate parses a pasted address and says which network's post it is, as a
// Target. It fetches nothing. An address that is not https, names credentials,
// an IP, a port other than 443 or a host the reader does not know, or is not the
// shape of one post on a known host, is an error that wraps ErrRefused.
//
// X, Bluesky, Reddit, Hacker News, Mastodon (on the instances the reader lists),
// DEV (dev.to/user/slug), Medium (medium.com/@user/title-id) and Hashnode (a blog
// on hashnode.dev) are readable. A LinkedIn post (linkedin.com/posts/... or
// linkedin.com/feed/update/...), a TikTok video (tiktok.com/@user/video/id; its
// short links are refused), a YouTube video, a Threads or Instagram post and a
// Facebook post or video are located with Review set and are never fetched.
func Locate(raw string) (Target, error) {
	t, _, err := plan(raw)
	return t, err
}

// plan is Locate, and also names the address the network answers the post at;
// "" for a network that is not read.
func plan(raw string) (Target, string, error) {
	bad := errPaste
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || len(raw) > 2048 {
		return Target{}, "", bad
	}
	host := u.Hostname()
	if host != strings.ToLower(host) || strings.HasSuffix(host, ".") {
		return Target{}, "", bad
	}
	for i := 0; i < len(host); i++ {
		if host[i] >= 0x80 {
			return Target{}, "", bad
		}
	}
	if p := u.Port(); p != "" && p != "443" {
		return Target{}, "", bad
	}
	path := u.Path
	switch {
	case slices.Contains(xHosts, host):
		m := xPath.FindStringSubmatch(path)
		if m == nil {
			return Target{}, "", bad
		}
		user := strings.ToLower(m[1])
		return Target{Network: "x", Key: m[2], URL: "https://x.com/" + user + "/status/" + m[2]},
			"https://publish.twitter.com/oembed?omit_script=true&dnt=true&url=" + url.QueryEscape("https://twitter.com/"+user+"/status/"+m[2]), nil
	case host == bskyHost:
		m := bskyPath.FindStringSubmatch(path)
		if m == nil {
			return Target{}, "", bad
		}
		actor := strings.ToLower(m[1])
		if !bskyActor(actor) {
			return Target{}, "", bad
		}
		return Target{Network: "bluesky", Key: actor + "/" + m[2], URL: "https://bsky.app/profile/" + actor + "/post/" + m[2]},
			"https://public.api.bsky.app/xrpc/app.bsky.feed.getPostThread?depth=0&parentHeight=0&uri=" + url.QueryEscape("at://"+actor+"/app.bsky.feed.post/"+m[2]), nil
	case slices.Contains(redHosts, host):
		m := redPath.FindStringSubmatch(path)
		if m == nil {
			return Target{}, "", bad
		}
		sub, id := strings.ToLower(m[1]), strings.ToLower(m[2])
		return Target{Network: "reddit", Key: id, URL: "https://www.reddit.com/r/" + sub + "/comments/" + id + "/"},
			"https://www.reddit.com/r/" + sub + "/comments/" + id + ".json?raw_json=1&limit=1", nil
	case host == hnHost:
		id := u.Query().Get("id")
		if path != "/item" || !hnID.MatchString(id) {
			return Target{}, "", bad
		}
		return Target{Network: "hn", Key: id, URL: "https://news.ycombinator.com/item?id=" + id},
			"https://hacker-news.firebaseio.com/v0/item/" + id + ".json", nil
	case slices.Contains(mastodonHosts, host):
		var name, remote, id string
		if m := mastoA.FindStringSubmatch(path); m != nil {
			name, remote, id = m[1], m[2], m[3]
		} else if m := mastoB.FindStringSubmatch(path); m != nil {
			name, id = m[1], m[2]
		} else {
			return Target{}, "", bad
		}
		name, remote = strings.ToLower(name), strings.ToLower(remote)
		acct := "@" + name
		if remote != "" {
			if !domain.MatchString(remote) {
				return Target{}, "", bad
			}
			acct += "@" + remote
		}
		return Target{Network: "mastodon", Key: host + "/" + id, URL: "https://" + host + "/" + acct + "/" + id},
			"https://" + host + "/api/v1/statuses/" + id, nil
	case slices.Contains(tiktokHosts, host):
		return tiktok(path)
	case host == "vm.tiktok.com" || host == "vt.tiktok.com":
		return Target{}, "", errTikTokShort
	case host == devtoHost:
		return devto(path)
	case host == "medium.com" || mediumBlog.MatchString(host):
		return medium(path)
	case host == "hashnode.com" || blogHost.MatchString(host):
		return hashnode(host, path)
	case slices.Contains(youtubeHosts, host) || host == "youtu.be":
		return youtube(host, u)
	case slices.Contains(threadsHosts, host):
		return threads(path)
	case slices.Contains(instagramHosts, host):
		return instagram(path)
	case slices.Contains(facebookHosts, host) || host == "fb.watch":
		return facebook(host, u)
	case slices.Contains(inHosts, host):
		esc := u.EscapedPath()
		if !inPath.MatchString(esc) {
			return Target{}, "", bad
		}
		key := digest("linkedin", esc)
		if m := inID.FindStringSubmatch(esc); m != nil {
			key = m[1]
		}
		return Target{Network: "linkedin", Key: key, URL: "https://www.linkedin.com" + esc, Review: true}, "", nil
	}
	return Target{}, "", fmt.Errorf("%w: that is not a post on a network the reader knows: x, bluesky, reddit, hn, mastodon, devto, medium, hashnode, linkedin, tiktok, youtube, threads, instagram, facebook", ErrRefused)
}

// errPaste is the refusal of an address that is not the https address of one post.
var errPaste = fmt.Errorf("%w: paste the https address of one post", ErrRefused)

// digest is a hash of parts, hex.
func digest(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}
