package social

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// recentMax bounds the posts Recent lists.
const recentMax = 25

var (
	redditUser = regexp.MustCompile(`^[A-Za-z0-9_-]{3,20}$`)
	hnUser     = regexp.MustCompile(`^[A-Za-z0-9_-]{2,15}$`)
	devtoUser  = regexp.MustCompile(`^[A-Za-z0-9_]{1,30}$`)
	mastoAcct  = regexp.MustCompile(`^@?(` + mastoName + `)@([a-z0-9.-]{1,253})$`)
)

// Recent lists the canonical addresses of a member's recent public posts, newest
// first, at most 25, by the member's handle on the network. The addresses are the
// ones Locate makes, so each can be read and claimed as it stands.
//
//   - bluesky: the handle, e.g. ada.bsky.social. The author feed of the public
//     AppView, original posts only: no replies, and no reposts, because a post is
//     listed only if its author is the handle.
//   - mastodon: user@instance, the instance one of the listed ones. The account is
//     looked up, then its statuses: no boosts, no replies, only public and
//     unlisted ones. A locked account lists nothing.
//   - reddit: the user name, without u/. Their submitted posts.
//   - hn: the user id. Their stories and comments, deleted and dead ones left out.
//   - devto: the user name. Their published articles, newest first.
//
// X, LinkedIn, TikTok, YouTube, Threads, Instagram and Facebook are an error:
// none can be proven from public data, a maintainer confirms their posts. So are
// Medium and Hashnode, whose posts are read from a pasted address but have no
// list the reader can ask for. So is a handle that is not the shape of one on the network, and
// a network the reader does not know; each wraps ErrRefused. An account the
// network does not know has no posts: an empty list and no error. A network that
// could not be read is an error, to try again later.
func Recent(ctx context.Context, network, handle string) ([]string, error) {
	switch network {
	case "bluesky":
		return recentBluesky(ctx, handle)
	case "mastodon":
		return recentMastodon(ctx, handle)
	case "reddit":
		return recentReddit(ctx, handle)
	case "hn":
		return recentHackerNews(ctx, handle)
	case "devto":
		return recentDevto(ctx, handle)
	case "x", "linkedin", "tiktok", "youtube", "threads", "instagram", "facebook":
		return nil, fmt.Errorf("%w: %s posts are not readable from public data: a maintainer confirms them", ErrRefused, network)
	case "medium", "hashnode":
		return nil, fmt.Errorf("%w: %s posts cannot be listed: paste the address of the post", ErrRefused, network)
	}
	return nil, fmt.Errorf("%w: %q is not a network the reader knows", ErrRefused, network)
}

// canonical reports whether u is exactly the address Locate makes of itself: a
// post the reader can read and claim as it stands.
func canonical(u string) bool {
	t, err := Locate(u)
	return err == nil && t.URL == u
}

// badHandle is the refusal of a handle that is not the shape of one.
func badHandle(network, handle string) error {
	return fmt.Errorf("%w: %q is not a %s handle", ErrRefused, handle, network)
}

// getJSON reads raw through the guard and decodes the answer into v. found is
// false when the network says the thing is not there (400, 404, 410).
func getJSON(ctx context.Context, raw string, v any) (found bool, err error) {
	r, err := fetch(ctx, raw)
	if err != nil {
		return false, fmt.Errorf("social: fetch: %w", err)
	}
	switch r.Status {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusNotFound, http.StatusGone:
		return false, nil
	default:
		return false, fmt.Errorf("social: fetch: the network answered %d", r.Status)
	}
	if json.Unmarshal(r.Body, v) != nil {
		return false, errUnreadable
	}
	return true, nil
}

// recentBluesky lists the original posts of a handle's author feed.
func recentBluesky(ctx context.Context, handle string) ([]string, error) {
	h := strings.ToLower(strings.TrimPrefix(handle, "@"))
	if len(h) > 253 || !domain.MatchString(h) {
		return nil, badHandle("bluesky", handle)
	}
	var o struct {
		Feed []struct {
			Post struct {
				URI       string `json:"uri"`
				IndexedAt string `json:"indexedAt"`
				Author    struct {
					Handle string `json:"handle"`
				} `json:"author"`
				Record struct {
					CreatedAt string `json:"createdAt"`
				} `json:"record"`
			} `json:"post"`
		} `json:"feed"`
	}
	found, err := getJSON(ctx, "https://public.api.bsky.app/xrpc/app.bsky.feed.getAuthorFeed?actor="+url.QueryEscape(h)+"&filter=posts_no_replies&limit="+strconv.Itoa(recentMax), &o)
	if err != nil || !found {
		return nil, err
	}
	type dated struct {
		url string
		at  time.Time
	}
	var posts []dated
	for _, e := range o.Feed {
		u := "https://bsky.app/profile/" + h + "/post/" + e.Post.URI[strings.LastIndexByte(e.Post.URI, '/')+1:]
		if strings.ToLower(e.Post.Author.Handle) != h || !canonical(u) {
			continue
		}
		if !slices.ContainsFunc(posts, func(d dated) bool { return d.url == u }) {
			posts = append(posts, dated{u, bskyTime(e.Post.IndexedAt, e.Post.Record.CreatedAt)})
		}
	}
	// A pinned post is listed first wherever it was written.
	slices.SortStableFunc(posts, func(a, b dated) int { return b.at.Compare(a.at) })
	var out []string
	for _, d := range posts[:min(len(posts), recentMax)] {
		out = append(out, d.url)
	}
	return out, nil
}

// recentMastodon lists the public statuses of user@instance.
func recentMastodon(ctx context.Context, handle string) ([]string, error) {
	m := mastoAcct.FindStringSubmatch(strings.ToLower(handle))
	if m == nil || !slices.Contains(mastodonHosts, m[2]) {
		return nil, badHandle("mastodon", handle)
	}
	name, host := m[1], m[2]
	var acct struct {
		ID     string `json:"id"`
		Locked bool   `json:"locked"`
	}
	found, err := getJSON(ctx, "https://"+host+"/api/v1/accounts/lookup?acct="+url.QueryEscape(name), &acct)
	if err != nil || !found || acct.Locked {
		return nil, err
	}
	if !mastoID.MatchString(acct.ID) {
		return nil, errUnreadable
	}
	var sts []struct {
		ID         string `json:"id"`
		Visibility string `json:"visibility"`
		Reblog     *struct {
			ID string `json:"id"`
		} `json:"reblog"`
	}
	found, err = getJSON(ctx, "https://"+host+"/api/v1/accounts/"+acct.ID+"/statuses?exclude_reblogs=true&exclude_replies=true&limit="+strconv.Itoa(recentMax), &sts)
	if err != nil || !found {
		return nil, err
	}
	var out []string
	for _, s := range sts {
		u := "https://" + host + "/@" + name + "/" + s.ID
		if s.Reblog != nil || (s.Visibility != "public" && s.Visibility != "unlisted") || !canonical(u) {
			continue
		}
		out = append(out, u)
		if len(out) == recentMax {
			break
		}
	}
	return out, nil
}

// recentReddit lists the posts a user submitted.
func recentReddit(ctx context.Context, handle string) ([]string, error) {
	if !redditUser.MatchString(handle) {
		return nil, badHandle("reddit", handle)
	}
	var o struct {
		Data struct {
			Children []struct {
				Kind string `json:"kind"`
				Data struct {
					ID        string  `json:"id"`
					Subreddit string  `json:"subreddit"`
					Author    string  `json:"author"`
					RemovedBy *string `json:"removed_by_category"`
				} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	found, err := getJSON(ctx, "https://www.reddit.com/user/"+handle+"/submitted.json?raw_json=1&sort=new&limit="+strconv.Itoa(recentMax), &o)
	if err != nil || !found {
		return nil, err
	}
	var out []string
	for _, c := range o.Data.Children {
		d := c.Data
		u := "https://www.reddit.com/r/" + strings.ToLower(d.Subreddit) + "/comments/" + strings.ToLower(d.ID) + "/"
		if c.Kind != "t3" || !strings.EqualFold(d.Author, handle) || (d.RemovedBy != nil && *d.RemovedBy != "") || !canonical(u) {
			continue
		}
		out = append(out, u)
		if len(out) == recentMax {
			break
		}
	}
	return out, nil
}

// recentHackerNews lists the latest stories and comments of a user.
func recentHackerNews(ctx context.Context, handle string) ([]string, error) {
	if !hnUser.MatchString(handle) {
		return nil, badHandle("hn", handle)
	}
	var u *struct {
		Submitted []int64 `json:"submitted"`
	}
	found, err := getJSON(ctx, "https://hacker-news.firebaseio.com/v0/user/"+handle+".json", &u)
	if err != nil || !found || u == nil {
		return nil, err
	}
	var out []string
	for _, id := range u.Submitted[:min(len(u.Submitted), recentMax)] {
		n := strconv.FormatInt(id, 10)
		if !canonical("https://news.ycombinator.com/item?id=" + n) {
			continue
		}
		var it *struct {
			By      string `json:"by"`
			Type    string `json:"type"`
			Deleted bool   `json:"deleted"`
			Dead    bool   `json:"dead"`
		}
		found, err := getJSON(ctx, "https://hacker-news.firebaseio.com/v0/item/"+n+".json", &it)
		if err != nil {
			return nil, err
		}
		if !found || it == nil || it.Deleted || it.Dead || it.By != handle || (it.Type != "story" && it.Type != "comment") {
			continue
		}
		out = append(out, "https://news.ycombinator.com/item?id="+n)
	}
	return out, nil
}

// recentDevto lists the published articles of a user, newest first.
func recentDevto(ctx context.Context, handle string) ([]string, error) {
	if !devtoUser.MatchString(handle) {
		return nil, badHandle("devto", handle)
	}
	var o []struct {
		Path string `json:"path"`
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	found, err := getJSON(ctx, "https://dev.to/api/articles?username="+handle+"&per_page="+strconv.Itoa(recentMax), &o)
	if err != nil || !found {
		return nil, err
	}
	var out []string
	for _, a := range o {
		u := "https://dev.to" + strings.ToLower(a.Path)
		if !strings.EqualFold(a.User.Username, handle) || !canonical(u) || slices.Contains(out, u) {
			continue
		}
		out = append(out, u)
		if len(out) == recentMax {
			break
		}
	}
	return out, nil
}
