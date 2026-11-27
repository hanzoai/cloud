package social

// articles.go - DEV, Medium and Hashnode: where a post lives, and what each
// network's public data says about it.
//
//   - devto reads the article API: dev.to/api/articles/<user>/<slug>.
//   - medium reads the author's public RSS feed, medium.com/feed/@<user>, which
//     holds only the latest items (about ten): a story older than that is not in
//     it, reads as Gone, and the caller treats it as removed.
//   - hashnode reads the blog's own page, the exact canonical address pasted. It
//     takes the anchors of the whole document, not only the article's, so what
//     makes a link count is Ref: it must be on hanzo.ai and carry the member's ref.
//
// An article carries more links than maxLinks, so only the links a referral can
// be (a link domain or a listed short-link host) are kept (relevant).

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	devtoHost = "dev.to"
	devtoPath = regexp.MustCompile(`^/([A-Za-z0-9_]{1,30})/([A-Za-z0-9_-]{1,200})/?$`)
	// mediumBlog is a name under medium.com: a publication, or an author's own.
	mediumBlog = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.medium\.com$`)
	// mediumPath is a story of an author: the last part of the title's slug is the
	// story's id, 10 to 12 hex digits.
	mediumPath   = regexp.MustCompile(`^/@([a-z0-9_.-]{1,60})/([a-z0-9-]*[a-z0-9]-([0-9a-f]{10,12}))/?$`)
	hashnodePath = regexp.MustCompile(`^/([A-Za-z0-9][A-Za-z0-9-]{0,248})/?$`)

	// errMediumShape is a Medium address that is not a story under an author.
	errMediumShape = fmt.Errorf("%w: paste the story as medium.com/@author/title-<id>; publication addresses are not supported", ErrRefused)
)

// devto locates an article: dev.to/<user>/<slug>.
func devto(p string) (Target, string, error) {
	m := devtoPath.FindStringSubmatch(p)
	if m == nil {
		return Target{}, "", errPaste
	}
	key := strings.ToLower(m[1] + "/" + m[2])
	return Target{Network: "devto", Key: key, URL: "https://dev.to/" + key}, "https://dev.to/api/articles/" + key, nil
}

// medium locates a story: <host>/@<user>/<slug>-<id>, the host medium.com or a
// name under it. The author's feed is the same for each of them.
func medium(p string) (Target, string, error) {
	m := mediumPath.FindStringSubmatch(strings.ToLower(p))
	if m == nil {
		return Target{}, "", errMediumShape
	}
	return Target{Network: "medium", Key: m[3], URL: "https://medium.com/@" + m[1] + "/" + m[2]}, "https://medium.com/feed/@" + m[1], nil
}

// hashnode locates a post on its blog: the page itself is what is read, so the
// address read is the canonical one.
func hashnode(host, p string) (Target, string, error) {
	m := hashnodePath.FindStringSubmatch(p)
	if m == nil {
		return Target{}, "", errPaste
	}
	key := host + "/" + strings.ToLower(m[1])
	return Target{Network: "hashnode", Key: key, URL: "https://" + key}, "https://" + key, nil
}

// relevant is links without the ones a member's referral cannot be: those not on
// a link domain or a listed short-link host. An article holds more links than
// maxLinks, and the ones that matter must not be cut for the ones that cannot.
func relevant(links []string) []string {
	return slices.DeleteFunc(links, func(l string) bool {
		u, err := url.Parse(l)
		return err != nil || !(ours(u.Hostname()) || slices.Contains(shortHosts, u.Hostname()))
	})
}

// rfc1123 parses an RSS date as UTC; zero when it is not one.
func rfc1123(s string) time.Time {
	for _, layout := range []string{time.RFC1123, time.RFC1123Z} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// readDevto reads an article of the DEV API. key is the user and the slug, as
// Locate spells them; the answer must be that article by that user.
func readDevto(b []byte, key string) (Page, error) {
	var o struct {
		Path      string `json:"path"`
		BodyHTML  string `json:"body_html"`
		Published string `json:"published_at"`
		User      struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	user, _, _ := strings.Cut(key, "/")
	if json.Unmarshal(b, &o) != nil || strings.ToLower(o.Path) != "/"+key || strings.ToLower(o.User.Username) != user {
		return Page{}, errUnreadable
	}
	return Page{Public: true, Author: user, Links: relevant(linksIn([]string{o.BodyHTML}, nil)), Published: stamp(o.Published)}, nil
}

// mediumID is the story id at the end of a Medium address, lower-case: the part of
// the last path segment after its last hyphen.
func mediumID(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return ""
	}
	seg := path.Base(u.Path)
	return strings.ToLower(seg[strings.LastIndexByte(seg, '-')+1:])
}

// readMedium reads an author's RSS feed for the story with the id. The feed is the
// author's: its channel link names them. A story that is not among the items is
// Gone, whether it was removed or is older than the feed reaches.
func readMedium(b []byte, id string) (Page, error) {
	var feed struct {
		Channel struct {
			// Links holds the channel's own link and every Atom link beside it; the
			// first one with an address is the channel's.
			Links []string `xml:"link"`
			Items []struct {
				Link    string `xml:"link"`
				GUID    string `xml:"guid"`
				PubDate string `xml:"pubDate"`
				Content string `xml:"encoded"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if xml.Unmarshal(b, &feed) != nil {
		return Page{}, errUnreadable
	}
	author := ""
	for _, l := range feed.Channel.Links {
		if u, err := url.Parse(strings.TrimSpace(l)); err == nil && strings.HasPrefix(u.Path, "/@") {
			author = strings.ToLower(strings.TrimPrefix(strings.TrimRight(u.Path, "/"), "/@"))
			break
		}
	}
	if author == "" {
		return Page{}, errUnreadable
	}
	for _, it := range feed.Channel.Items {
		if mediumID(it.Link) == id || mediumID(it.GUID) == id {
			return Page{Public: true, Author: author, Links: relevant(linksIn([]string{it.Content}, nil)), Published: rfc1123(it.PubDate)}, nil
		}
	}
	return Page{Gone: true}, nil
}

var (
	headRe   = regexp.MustCompile(`(?is)<(meta|link)\s([^>]*)>`)
	attrRe   = regexp.MustCompile(`(?is)([a-z][a-z0-9:_-]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	anchorRe = regexp.MustCompile(`(?is)<a\s[^>]*?href\s*=\s*(?:"([^"]+)"|'([^']+)')`)
	ldRe     = regexp.MustCompile(`(?is)<script[^>]+application/ld\+json[^>]*>(.*?)</script>`)
)

// tagsOf is the attributes of each <meta> and <link> tag of doc, names lower-case,
// with the tag's own name under "tag".
func tagsOf(doc string) []map[string]string {
	var out []map[string]string
	for _, m := range headRe.FindAllStringSubmatch(doc, -1) {
		a := map[string]string{"tag": strings.ToLower(m[1])}
		for _, kv := range attrRe.FindAllStringSubmatch(m[2], -1) {
			a[strings.ToLower(kv[1])] = html.UnescapeString(kv[2] + kv[3])
		}
		out = append(out, a)
	}
	return out
}

// sameAddress reports whether two addresses are one page: the same host and path,
// whatever the case and a trailing slash, and no query.
func sameAddress(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil || ua.RawQuery != "" || ub.RawQuery != "" {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host) && strings.EqualFold(strings.TrimRight(ua.Path, "/"), strings.TrimRight(ub.Path, "/"))
}

// readHashnode reads a blog page. key is the blog's host and the slug. The page
// must say it is this post: its canonical link is this address and it holds a
// BlogPosting. The author is the handle in the profile address the markup gives,
// else its author meta, else the author's name; the time is datePublished.
func readHashnode(b []byte, key string) (Page, error) {
	doc := string(b)
	canon := false
	meta := ""
	for _, a := range tagsOf(doc) {
		switch {
		case a["tag"] == "link" && strings.EqualFold(a["rel"], "canonical"):
			canon = canon || sameAddress(a["href"], "https://"+key)
		case a["tag"] == "meta" && strings.EqualFold(a["name"], "author"):
			meta = a["content"]
		}
	}
	if !canon {
		return Page{}, errUnreadable
	}
	for _, m := range ldRe.FindAllStringSubmatch(doc, -1) {
		var ld struct {
			Type      string          `json:"@type"`
			Author    json.RawMessage `json:"author"`
			Published string          `json:"datePublished"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(m[1])), &ld) != nil || ld.Type != "BlogPosting" {
			continue
		}
		name, profile := ldAuthor(ld.Author)
		author := name
		if meta != "" {
			author = meta
		}
		if u, err := url.Parse(profile); err == nil {
			if h := path.Base(u.Path); strings.HasPrefix(h, "@") {
				author = h
			}
		}
		var hrefs []string
		for _, a := range anchorRe.FindAllStringSubmatch(doc, -1) {
			hrefs = append(hrefs, a[1]+a[2])
		}
		return Page{Public: true, Author: strings.ToLower(strings.TrimPrefix(strings.TrimSpace(author), "@")), Links: relevant(linksIn(nil, hrefs)), Published: stamp(ld.Published)}, nil
	}
	return Page{}, errUnreadable
}

// ldAuthor is the name and profile address of a JSON-LD author, which is an
// object, a list of them, or a name.
func ldAuthor(raw json.RawMessage) (name, profile string) {
	var one struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if json.Unmarshal(raw, &one) == nil {
		return one.Name, one.URL
	}
	var many []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if json.Unmarshal(raw, &many) == nil && len(many) > 0 {
		return many[0].Name, many[0].URL
	}
	_ = json.Unmarshal(raw, &name)
	return name, ""
}
