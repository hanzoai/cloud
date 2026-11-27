package social

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Page is what a network's public data says about one post.
type Page struct {
	// Status is the HTTP status of the fetch that read it.
	Status int
	// At is when it was fetched, UTC.
	At time.Time
	// Public is true when the post is publicly readable: the network answered 200
	// with the post in it. A deleted, private, protected or hidden post is false.
	Public bool
	// Gone is true when the network said the post does not exist or was removed
	// (404, 410, a deleted or removed marker). A post that exists but is not
	// public is not Gone.
	Gone bool
	// Author is the author's handle on that network as the public data states it,
	// lower-case, without a leading @; "" when it does not say.
	Author string
	// Links are the links found in the post's text, facets and card, https only,
	// in order, without repeats, at most 12. For an article (DEV, Medium,
	// Hashnode) they are the ones on hanzo.ai or a listed short-link host: the
	// only ones a referral can be, kept so a long page does not cut them off.
	Links []string
	// Published is when the network says the post was published, UTC; zero when
	// its public data does not say. Bluesky states the time its index first saw
	// the post, which an author cannot set, in preference to the time the author
	// wrote into the record.
	Published time.Time
}

// Read fetches the post t names from its network's public endpoint, through the
// guard, and reports what the network states.
//
// A post that is not there is Page{Gone: true} and one that is not public is
// Page{Public: false}: neither is an error. An error means the network could not
// be read or answered something that is not the post (a transport failure, a
// 429 or 5xx, a timeout, an answer for another post): the caller tries again
// later. A Target with Review set, or one that is not exactly what Locate makes
// of its URL, is refused (ErrRefused) and nothing is fetched.
func Read(ctx context.Context, t Target) (Page, error) {
	if t.Review {
		return Page{}, fmt.Errorf("%w: %s posts are confirmed by a maintainer, not read", ErrRefused, t.Network)
	}
	c, api, err := plan(t.URL)
	if err != nil {
		return Page{}, err
	}
	if c != t {
		return Page{}, fmt.Errorf("%w: the target is not the one Locate makes of its address", ErrRefused)
	}
	r, err := fetch(ctx, api)
	if err != nil {
		return Page{}, fmt.Errorf("social: read %s: %w", t.Network, err)
	}
	p := Page{Status: r.Status, At: time.Now().UTC()}
	switch r.Status {
	case http.StatusOK:
		var got Page
		switch t.Network {
		case "x":
			got, err = readX(r.Body, t.Key)
		case "bluesky":
			got, err = readBluesky(r.Body, t.Key)
		case "reddit":
			got, err = readReddit(r.Body, t.Key)
		case "hn":
			got, err = readHackerNews(r.Body, t.Key)
		case "mastodon":
			got, err = readMastodon(r.Body, t.Key)
		case "devto":
			got, err = readDevto(r.Body, t.Key)
		case "medium":
			got, err = readMedium(r.Body, t.Key)
		case "hashnode":
			got, err = readHashnode(r.Body, t.Key)
		}
		if err != nil {
			return Page{}, fmt.Errorf("social: read %s: %w", t.Network, err)
		}
		got.Status, got.At = p.Status, p.At
		got.Links = capLinks(got.Links)
		return got, nil
	case http.StatusNotFound, http.StatusGone, http.StatusBadRequest:
		p.Gone = true
		return p, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return p, nil
	}
	return Page{}, fmt.Errorf("social: read %s: the network answered %d", t.Network, r.Status)
}

// Ref reports whether the post carries the member's own referral link: some link
// of p, after following short links (at most three hops, and only the listed
// short-link hosts are fetched), is on hanzo.ai or a subdomain and has the query
// parameter ref equal to code, ignoring case. At most 12 links are looked at.
//
// A link that is not ours, or cannot be followed because it is refused, is not
// an error: it does not count. A short link that could not be fetched is an
// error, so the caller tries again later.
func Ref(ctx context.Context, p Page, code string) (bool, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return false, nil
	}
	for _, link := range capLinks(p.Links) {
		final, err := resolveLink(ctx, link)
		if err != nil {
			return false, fmt.Errorf("social: follow %s: %w", link, err)
		}
		u, err := admit(final)
		if err != nil || !ours(u.Hostname()) {
			continue
		}
		for k, vs := range u.Query() {
			if !strings.EqualFold(k, "ref") {
				continue
			}
			for _, v := range vs {
				if strings.EqualFold(v, code) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// capLinks is links without the ones past maxLinks.
func capLinks(links []string) []string {
	if len(links) > maxLinks {
		return links[:maxLinks]
	}
	return links
}

// idOf is the post's id on its network: the last part of its Key.
func idOf(key string) string {
	return key[strings.LastIndexByte(key, '/')+1:]
}

// stamp parses a network's RFC 3339 time as UTC; zero when it is not one.
func stamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// unix is a Unix time in seconds as UTC; zero for none.
func unix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

var (
	hrefRe     = regexp.MustCompile(`(?i)href\s*=\s*["']([^"']+)["']`)
	tagRe      = regexp.MustCompile(`(?s)<[^>]*>`)
	urlRe      = regexp.MustCompile(`https?://[^\s<>"'\x60]+`)
	bareRe     = regexp.MustCompile(`(?i)(?:^|[\s(])((?:[a-z0-9-]+\.)*hanzo\.ai/[^\s<>"'\x60)]*)`)
	paragraph  = regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`)
	datetimeRe = regexp.MustCompile(`(?i)\bdatetime\s*=\s*["']([^"']+)["']`)
)

// trimLink drops the punctuation a sentence puts after a link.
func trimLink(l string) string { return strings.TrimRight(l, ".,;:!?)]}'\"…") }

// linksIn is every https link in the HTML and the text, in order, without
// repeats: hrefs, URLs written out, and hanzo.ai addresses written without a
// scheme. An http link is taken as the https one.
func linksIn(htmls []string, texts []string) []string {
	var out []string
	add := func(l string) {
		l = trimLink(strings.TrimSpace(html.UnescapeString(l)))
		if l == "" {
			return
		}
		if rest, ok := strings.CutPrefix(l, "http://"); ok {
			l = "https://" + rest
		}
		if !strings.HasPrefix(l, "https://") {
			return
		}
		if u, err := url.Parse(l); err == nil {
			u.Host = strings.ToLower(u.Host)
			l = u.String()
		}
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	for _, h := range htmls {
		for _, m := range hrefRe.FindAllStringSubmatch(h, -1) {
			add(m[1])
		}
	}
	for _, t := range append(slices.Clone(texts), htmls...) {
		t = html.UnescapeString(tagRe.ReplaceAllString(t, " "))
		for _, m := range urlRe.FindAllString(t, -1) {
			add(m)
		}
		for _, m := range bareRe.FindAllStringSubmatch(t, -1) {
			add("https://" + m[1])
		}
	}
	return out
}

// readX reads X's oEmbed answer. oEmbed states no time; the page carries one
// only if its HTML does.
func readX(b []byte, id string) (Page, error) {
	var o struct {
		URL       string `json:"url"`
		AuthorURL string `json:"author_url"`
		HTML      string `json:"html"`
	}
	if json.Unmarshal(b, &o) != nil || o.HTML == "" {
		return Page{}, errUnreadable
	}
	u, err := url.Parse(o.URL)
	if err != nil || !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/status/"+id) {
		return Page{}, errUnreadable
	}
	words := o.HTML
	if m := paragraph.FindStringSubmatch(o.HTML); m != nil {
		words = m[1]
	}
	author := ""
	if a, err := url.Parse(o.AuthorURL); err == nil {
		if base := path.Base(strings.TrimRight(a.Path, "/")); base != "." && base != "/" {
			author = strings.ToLower(strings.TrimPrefix(base, "@"))
		}
	}
	var published time.Time
	if m := datetimeRe.FindStringSubmatch(o.HTML); m != nil {
		published = stamp(m[1])
	}
	return Page{Public: true, Author: author, Links: linksIn([]string{words}, nil), Published: published}, nil
}

// bskyLink is the external card of an embed.
type bskyLink struct {
	URI string `json:"uri"`
}

// bskyEmbed is the part of an embed that can hold a link: a card, or the card of
// the media beside a quoted record.
type bskyEmbed struct {
	External *bskyLink `json:"external"`
	Media    *struct {
		External *bskyLink `json:"external"`
	} `json:"media"`
}

// uris are the links the embed holds.
func (e *bskyEmbed) uris() []string {
	var out []string
	if e == nil {
		return out
	}
	if e.External != nil {
		out = append(out, e.External.URI)
	}
	if e.Media != nil && e.Media.External != nil {
		out = append(out, e.Media.External.URI)
	}
	return out
}

// bskyTime is when a post was published as Bluesky's index states it, else as its
// author wrote it.
func bskyTime(indexedAt, createdAt string) time.Time {
	if t := stamp(indexedAt); !t.IsZero() {
		return t
	}
	return stamp(createdAt)
}

// readBluesky reads the thread of one post from the AppView. key is the actor and
// the record key, as Locate spells them.
func readBluesky(b []byte, key string) (Page, error) {
	actor, rkey, _ := strings.Cut(key, "/")
	var o struct {
		Thread struct {
			Type string `json:"$type"`
			Post *struct {
				URI       string `json:"uri"`
				IndexedAt string `json:"indexedAt"`
				Author    struct {
					Handle string `json:"handle"`
					Labels []struct {
						Val string `json:"val"`
					} `json:"labels"`
				} `json:"author"`
				Record struct {
					Text      string `json:"text"`
					CreatedAt string `json:"createdAt"`
					Facets    []struct {
						Features []struct {
							Type string `json:"$type"`
							URI  string `json:"uri"`
						} `json:"features"`
					} `json:"facets"`
					Embed *bskyEmbed `json:"embed"`
				} `json:"record"`
				Embed *bskyEmbed `json:"embed"`
			} `json:"post"`
		} `json:"thread"`
	}
	if json.Unmarshal(b, &o) != nil {
		return Page{}, errUnreadable
	}
	switch {
	case strings.HasSuffix(o.Thread.Type, "notFoundPost"):
		return Page{Gone: true}, nil
	case strings.HasSuffix(o.Thread.Type, "blockedPost"):
		return Page{}, nil
	case o.Thread.Post == nil || !strings.HasSuffix(o.Thread.Post.URI, "/"+rkey):
		return Page{}, errUnreadable
	}
	p := o.Thread.Post
	handle := strings.ToLower(p.Author.Handle)
	// The answer must be for the author the address names, by DID or by handle.
	ofActor := handle == actor
	if strings.HasPrefix(actor, "did:") {
		ofActor = strings.HasPrefix(p.URI, "at://"+actor+"/")
	}
	if !ofActor {
		return Page{}, errUnreadable
	}
	for _, l := range p.Author.Labels {
		if l.Val == "!no-unauthenticated" {
			return Page{}, nil
		}
	}
	var uris []string
	for _, f := range p.Record.Facets {
		for _, ft := range f.Features {
			uris = append(uris, ft.URI)
		}
	}
	uris = append(uris, p.Record.Embed.uris()...)
	uris = append(uris, p.Embed.uris()...)
	return Page{Public: true, Author: handle, Links: linksIn(nil, append(uris, p.Record.Text)), Published: bskyTime(p.IndexedAt, p.Record.CreatedAt)}, nil
}

// readReddit reads a post's .json.
func readReddit(b []byte, id string) (Page, error) {
	var o []struct {
		Data struct {
			Children []struct {
				Kind string `json:"kind"`
				Data struct {
					ID            string  `json:"id"`
					Author        string  `json:"author"`
					Title         string  `json:"title"`
					Selftext      string  `json:"selftext"`
					URL           string  `json:"url"`
					IsSelf        bool    `json:"is_self"`
					RemovedBy     *string `json:"removed_by_category"`
					SubredditType string  `json:"subreddit_type"`
					CreatedUTC    float64 `json:"created_utc"`
				} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &o) != nil || len(o) == 0 || len(o[0].Data.Children) == 0 || o[0].Data.Children[0].Data.ID != id {
		return Page{}, errUnreadable
	}
	d := o[0].Data.Children[0].Data
	switch {
	case d.SubredditType == "private":
		return Page{}, nil
	case d.Author == "[deleted]" || d.Selftext == "[removed]" || d.Selftext == "[deleted]" || (d.RemovedBy != nil && *d.RemovedBy != ""):
		return Page{Gone: true}, nil
	}
	texts := []string{d.Title, d.Selftext}
	if !d.IsSelf {
		texts = append(texts, d.URL)
	}
	return Page{Public: true, Author: strings.ToLower(d.Author), Links: linksIn(nil, texts), Published: unix(int64(d.CreatedUTC))}, nil
}

// readHackerNews reads an item of the Hacker News API.
func readHackerNews(b []byte, id string) (Page, error) {
	var it *struct {
		ID      int64  `json:"id"`
		By      string `json:"by"`
		Text    string `json:"text"`
		URL     string `json:"url"`
		Title   string `json:"title"`
		Time    int64  `json:"time"`
		Deleted bool   `json:"deleted"`
		Dead    bool   `json:"dead"`
	}
	if json.Unmarshal(b, &it) != nil {
		return Page{}, errUnreadable
	}
	if it == nil || it.Deleted || it.Dead {
		return Page{Gone: true}, nil
	}
	if strconv.FormatInt(it.ID, 10) != id {
		return Page{}, errUnreadable
	}
	return Page{Public: true, Author: strings.ToLower(it.By), Links: linksIn([]string{it.Text}, []string{it.URL, it.Title}), Published: unix(it.Time)}, nil
}

// readMastodon reads a status from an instance's public API. key is the
// instance and the status id, as Locate spells them.
func readMastodon(b []byte, key string) (Page, error) {
	id := idOf(key)
	var o struct {
		ID         string `json:"id"`
		CreatedAt  string `json:"created_at"`
		Content    string `json:"content"`
		Visibility string `json:"visibility"`
		Account    struct {
			Acct   string `json:"acct"`
			Locked bool   `json:"locked"`
		} `json:"account"`
		Card *struct {
			URL string `json:"url"`
		} `json:"card"`
	}
	if json.Unmarshal(b, &o) != nil || o.ID != id {
		return Page{}, errUnreadable
	}
	if o.Account.Locked || (o.Visibility != "public" && o.Visibility != "unlisted") {
		return Page{}, nil
	}
	var texts []string
	if o.Card != nil {
		texts = append(texts, o.Card.URL)
	}
	return Page{Public: true, Author: strings.ToLower(strings.TrimPrefix(o.Account.Acct, "@")), Links: linksIn([]string{o.Content}, texts), Published: stamp(o.CreatedAt)}, nil
}
