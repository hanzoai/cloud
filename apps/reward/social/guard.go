package social

// guard.go - THE READER of a URL: the one place this package reaches the
// network, and so the place that must not be turned on Hanzo's own network.
//
// A member pastes a link; the post is read server-side, because a browser can
// say anything. That makes the member's text a request the server makes, which
// is the shape of a server-side request forgery. Four walls hold it:
//
//  1. NAMES. Only hosts in the allow-lists below are fetched: each network's own
//     public read endpoint, the Mastodon instances named, Hashnode's blogs, and
//     the short-link hosts. A URL names no IP, carries no credentials, speaks
//     https on 443 and has an ASCII, lower-case host exactly equal to a listed
//     one, or to one label under hashnode.dev (admit, fetchable).
//  2. ADDRESSES. The dialer resolves the host itself, refuses the connection if
//     ANY address it resolves to is not public (loopback, private, link-local,
//     CGNAT, ULA, multicast, unspecified, documentation, reserved, the cloud
//     metadata addresses, and every IPv4 hidden in an IPv6 form), and then dials
//     the address it checked, so the name cannot answer differently the second
//     time (no DNS rebinding).
//  3. REDIRECTS. None is followed. A short link is resolved by resolveLink, one
//     hop at a time, each hop admitted by 1 and 2, three at most.
//  4. BOUNDS. Five seconds, 512 KiB of body, no proxy, no connection reuse, one
//     fixed User-Agent.
//
// fetch is a variable so a test feeds canned pages; getWith is the real client
// the guard tests drive against servers on the loopback, which must be refused.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// fetchTimeout bounds one fetch, dial to last byte.
	fetchTimeout = 5 * time.Second
	// fetchLimit bounds the body read, in bytes.
	fetchLimit = 512 << 10
	// fetchAgent is the one User-Agent a page is fetched with.
	fetchAgent = "HanzoRewards/1 (+https://hanzo.ai/leaderboard)"
	// maxHops bounds the short links followed to a post's real link.
	maxHops = 3
	// maxLinks bounds the links of one post that are kept and looked into.
	maxLinks = 12
)

// apiHosts are the networks' own public read endpoints: the article API of DEV,
// the author feed of Medium, and Hashnode's own pages.
var apiHosts = []string{
	"publish.twitter.com",
	"public.api.bsky.app",
	"www.reddit.com",
	"hacker-news.firebaseio.com",
	"dev.to",
	"medium.com",
	"hashnode.com",
}

// blogHost is a Hashnode member's blog: exactly one label under hashnode.dev. Its
// page is the post's public data; no deeper name and no other domain is a blog.
var blogHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.hashnode\.dev$`)

// mastodonHosts are the Mastodon instances a post can be read from.
var mastodonHosts = []string{
	"mastodon.social", "hachyderm.io", "fosstodon.org", "infosec.exchange", "mas.to",
	"techhub.social", "mstdn.social", "universeodon.com", "social.vivaldi.net",
}

// shortHosts are the link shorteners a post's link may pass through.
var shortHosts = []string{"t.co", "bit.ly", "lnkd.in", "buff.ly", "ow.ly", "tinyurl.com", "is.gd"}

// linkDomains are the domains a member's link is on, themselves and subdomains.
var linkDomains = []string{"hanzo.ai"}

// fetchable reports whether host may be fetched at all.
func fetchable(host string) bool {
	return slices.Contains(apiHosts, host) || slices.Contains(mastodonHosts, host) || slices.Contains(shortHosts, host) || blogHost.MatchString(host)
}

// ours reports whether host is a link domain or a subdomain of one.
func ours(host string) bool {
	return slices.ContainsFunc(linkDomains, func(d string) bool { return host == d || strings.HasSuffix(host, "."+d) })
}

var (
	// ErrRefused marks a URL, a handle, a network or an address this package will
	// not touch. It is permanent: asking again changes nothing.
	ErrRefused = errors.New("social: refused")
	// errShape is a URL that is not an https address naming a host on port 443.
	errShape = fmt.Errorf("%w: not an https address naming a host", ErrRefused)
	// errTooLarge is a body past fetchLimit.
	errTooLarge = errors.New("social: the page is larger than the reader reads")
	// errUnreadable is a 200 that is not the thing asked for.
	errUnreadable = errors.New("social: the network answered something that is not the post")
)

// reply is one answer: the status, the body, and where a redirect points.
type reply struct {
	Status   int
	Body     []byte
	Location string
}

// fetch reads one URL the guard admits. A variable so a test feeds pages.
var fetch = guardedGet

// admit parses raw and returns it only if it is a URL the reader may fetch in
// shape: https, port 443 or none, no credentials, no opaque form, and an ASCII
// lower-case host that is not an IP literal and has no trailing dot. Which hosts
// are allowed is the caller's second test (fetchable).
func admit(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" {
		return nil, errShape
	}
	if u.Scheme != "https" || u.User != nil {
		return nil, errShape
	}
	host := u.Hostname()
	if host == "" || host != strings.ToLower(host) || strings.HasSuffix(host, ".") || strings.Contains(host, "%") {
		return nil, errShape
	}
	for i := 0; i < len(host); i++ {
		if c := host[i]; c >= 0x80 || c <= ' ' || strings.IndexByte("[]\\@:/?#", c) >= 0 {
			return nil, errShape
		}
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil, errShape
	}
	// A name has a dot and ends in letters: "127.1", "0x7f000001" and "2130706433"
	// are IPv4 addresses in the spellings a resolver accepts, and are not names.
	if dot := strings.LastIndexByte(host, '.'); dot < 0 || host[dot+1] < 'a' || host[dot+1] > 'z' {
		return nil, errShape
	}
	if p := u.Port(); p != "" && p != "443" {
		return nil, errShape
	}
	return u, nil
}

// special are the ranges that are not the public internet, beyond what netip
// already names: shared address space, the metadata and test ranges, reserved
// space, and the IPv6 forms that carry an IPv4 inside them.
var special = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
		"::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "fec0::/10",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// isPublic reports whether ip is an address on the public internet. An IPv4
// written as an IPv6 (::ffff:10.0.0.1) is judged as the IPv4 it is.
func isPublic(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	return !slices.ContainsFunc(special, func(p netip.Prefix) bool { return p.Contains(ip) })
}

// newClient is an HTTP client whose every connection is to an address allow
// admits, resolved here and dialed as resolved. It follows no redirect, uses no
// proxy and reuses no connection.
func newClient(allow func(netip.Addr) bool) *http.Client {
	dialer := &net.Dialer{Timeout: fetchTimeout}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, errShape
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", rooted(host))
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("%w: %s resolves to no address", ErrRefused, host)
			}
			// ANY non-public answer refuses the host: a name that is public to one
			// resolver and private to another is not one to dial.
			for _, ip := range ips {
				if !allow(ip.Unmap()) {
					return nil, fmt.Errorf("%w: %s resolves to an address that is not public", ErrRefused, host)
				}
			}
			var last error
			for _, ip := range ips {
				c, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
		TLSHandshakeTimeout:    fetchTimeout,
		ResponseHeaderTimeout:  fetchTimeout,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   fetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// rooted is host as the absolute name it is, so the resolver asks for that name
// and no other. A pod's resolv.conf searches the cluster's own domains first
// (ndots:5), and a public host like dev.to was asked as
// dev.to.<namespace>.svc.cluster.local before itself. An address is returned as
// it is.
func rooted(host string) string {
	if _, err := netip.ParseAddr(host); err == nil || strings.HasSuffix(host, ".") {
		return host
	}
	return host + "."
}

// guarded is the client every real fetch goes through.
var guarded = newClient(isPublic)

// getWith is one GET of u through c: no redirect followed, the body bounded.
func getWith(ctx context.Context, c *http.Client, u *url.URL) (reply, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return reply{}, errShape
	}
	req.Header.Set("User-Agent", fetchAgent)
	req.Header.Set("Accept", "application/json, text/html;q=0.8, */*;q=0.5")
	resp, err := c.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchLimit+1))
	if err != nil {
		return reply{}, err
	}
	if len(body) > fetchLimit {
		return reply{}, errTooLarge
	}
	return reply{Status: resp.StatusCode, Body: body, Location: resp.Header.Get("Location")}, nil
}

// guardedGet reads raw if, and only if, it is an https URL on a listed host.
func guardedGet(ctx context.Context, raw string) (reply, error) {
	u, err := admit(raw)
	if err != nil {
		return reply{}, err
	}
	if !fetchable(u.Hostname()) {
		return reply{}, fmt.Errorf("%w: %s is not a host the reader fetches", ErrRefused, u.Hostname())
	}
	return getWith(ctx, guarded, u)
}

// metaRefresh is the URL an HTML page redirects to by <meta http-equiv=refresh>
// or by location.replace, as a link shortener answers a browser; "" when none.
func metaRefresh(body []byte) string {
	if m := metaRe.FindSubmatch(body); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	if m := replaceRe.FindSubmatch(body); m != nil {
		return strings.ReplaceAll(string(m[1]), `\/`, "/")
	}
	return ""
}

var (
	metaRe    = regexp.MustCompile(`(?is)<meta[^>]+http-equiv=["']?refresh["']?[^>]+content=["'][^"']*url=([^"'>\s]+)`)
	replaceRe = regexp.MustCompile(`location\.replace\("([^"]+)"\)`)
)

// resolveLink follows a short link to the URL it stands for, one hop at a time,
// at most maxHops. Only listed short-link hosts are fetched; a link on any other
// host is returned as it is and never fetched. A link that cannot be resolved
// (refused, gone, not a redirect) is returned as it is: it is not ours. A fetch
// that failed, or that the shortener answered with 429 or a 5xx, is an error, so
// the caller can try again later.
func resolveLink(ctx context.Context, link string) (string, error) {
	cur := link
	for range maxHops {
		u, err := admit(cur)
		if err != nil {
			return cur, nil
		}
		if !slices.Contains(shortHosts, u.Hostname()) {
			return u.String(), nil
		}
		r, err := fetch(ctx, u.String())
		switch {
		case errors.Is(err, ErrRefused):
			return u.String(), nil
		case err != nil:
			return "", err
		}
		next := ""
		switch {
		case r.Status >= 300 && r.Status < 400:
			next = r.Location
		case r.Status == http.StatusOK:
			next = metaRefresh(r.Body)
		case r.Status == http.StatusTooManyRequests || r.Status >= 500:
			return "", fmt.Errorf("social: %s answered %d", u.Hostname(), r.Status)
		}
		if next == "" {
			return u.String(), nil
		}
		nu, err := u.Parse(next)
		if err != nil {
			return u.String(), nil
		}
		cur = nu.String()
	}
	return cur, nil
}
