package social

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAdmitRefusesWhatIsNotAHostedHTTPSAddress: the shape wall. Every form of a
// URL that names an address, carries credentials, or spells a host two ways is
// refused before a name is looked up.
func TestAdmitRefusesWhatIsNotAHostedHTTPSAddress(t *testing.T) {
	for _, raw := range []string{
		"http://publish.twitter.com/oembed",
		"ftp://publish.twitter.com/",
		"//publish.twitter.com/oembed",
		"https:publish.twitter.com",
		"https://x.com@evil.com/",
		"https://user:pw@publish.twitter.com/oembed",
		"https://publish.twitter.com@127.0.0.1/",
		"https://127.0.0.1/",
		"https://127.1/",
		"https://0x7f000001/",
		"https://2130706433/",
		"https://[::1]/",
		"https://[::ffff:127.0.0.1]/",
		"https://[::ffff:7f00:1]/",
		"https://[fd00:ec2::254]/latest/meta-data/",
		"https://169.254.169.254/latest/meta-data/",
		"https://10.0.0.1/",
		"https://publish.twitter.com:8443/oembed",
		"https://publish.twitter.com:22/",
		"https://PUBLISH.twitter.com/oembed",
		"https://publish.twitter.com./oembed",
		"https://ｐublish.twitter.com/oembed",
		"https://publish.twitter.com%2F@evil.com/",
		"https://evil.com\\@publish.twitter.com/",
		"https://publish.twitter.com\x00.evil.com/",
		"https://publish twitter.com/",
		"",
		"not a url",
	} {
		if u, err := admit(raw); err == nil {
			t.Errorf("admit(%q) = %v, want a refusal", raw, u)
		} else if !errors.Is(err, ErrRefused) {
			t.Errorf("admit(%q) = %v, want it to wrap ErrRefused", raw, err)
		}
	}
	for _, raw := range []string{"https://publish.twitter.com/oembed?url=x", "https://publish.twitter.com:443/oembed", "https://t.co/abc"} {
		if _, err := admit(raw); err != nil {
			t.Errorf("admit(%q): %v", raw, err)
		}
	}
}

// TestOnlyListedHostsAreFetched: a well-formed address on a host outside the
// allow-lists is refused by the real fetcher, with no lookup and no dial.
func TestOnlyListedHostsAreFetched(t *testing.T) {
	for _, raw := range []string{
		"https://evil.com/",
		"https://x.com.evil.com/",
		"https://evil.com/publish.twitter.com",
		"https://notpublish.twitter.com/oembed",
		"https://publish.twitter.com.evil.com/oembed",
		"https://twitter.com/",
		"https://bsky.app/",
		"https://news.ycombinator.com/item?id=1",
		"https://hanzo.ai/",
		"https://api.hanzo.ai/v1",
		"https://localhost/",
		"https://metadata.google.internal/",
		"https://xn--publish-twitter-3ib.com/",
		"https://mastodon.evil.example/api/v1/statuses/1",
		"https://evilmastodon.social/api/v1/statuses/1",
		"https://sub.mastodon.social/api/v1/statuses/1",
		"https://t.co.evil.com/x",
		// look-alikes of the hosts DEV, Medium and Hashnode are read at
		"https://dev.to.evil.com/api/articles/a/b",
		"https://evildev.to/api/articles/a/b",
		"https://www.dev.to/api/articles/a/b",
		"https://api.dev.to/",
		"https://medium.com.evil.com/feed/@a",
		"https://evilmedium.com/feed/@a",
		"https://www.medium.com/feed/@a",
		"https://pub.medium.com/feed/@a",
		"https://cdn-images-1.medium.com/x.png",
		"https://hashnode.dev/",
		"https://a.b.hashnode.dev/x",
		"https://evilhashnode.dev/x",
		"https://hashnode.dev.evil.com/x",
		"https://ada.hashnode.dev.evil.com/x",
		"https://hashnode.com.evil.com/x",
		"https://ada.hashnode.com/x",
		"https://cdn.hashnode.com/x.png",
		"https://-a.hashnode.dev/x",
		// the networks that are only reviewed are never fetched: TikTok's oEmbed among them
		"https://www.tiktok.com/oembed?url=https%3A%2F%2Fwww.tiktok.com%2F%40a%2Fvideo%2F1",
		"https://www.tiktok.com/@a/video/1",
		"https://tiktok.com/@a/video/1",
		"https://vm.tiktok.com/ZMabc/",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://www.threads.com/@a/post/C8x7Yz_pQ1a",
		"https://www.instagram.com/p/CxYz1AbCdEf/",
		"https://www.facebook.com/a/posts/10161234567890123",
		"https://fb.watch/AbCdEfGhI/",
		"https://www.linkedin.com/posts/a_x-activity-7123456789012345678-AbCd",
		// a blog on a domain of its own
		"https://blog.example.com/my-post",
	} {
		_, err := guardedGet(context.Background(), raw)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("guardedGet(%q) = %v, want ErrRefused", raw, err)
		}
	}
}

// TestWhatIsPublic: the address wall. Only the public internet is public, and an
// IPv4 hidden in an IPv6 form is judged as the IPv4 it is.
func TestWhatIsPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "93.184.216.34": true, "2606:4700:4700::1111": true, "2001:4860:4860::8888": true,
		"127.0.0.1": false, "127.255.255.254": false, "::1": false, "0.0.0.0": false, "::": false, "0.1.2.3": false,
		"10.0.0.1": false, "172.16.0.1": false, "172.31.255.255": false, "192.168.1.1": false,
		"169.254.169.254": false, "169.254.0.1": false, "fe80::1": false, "fd00:ec2::254": false, "fc00::1": false,
		"100.64.0.1": false, "100.100.100.200": false, "192.0.0.192": false, "192.0.2.1": false, "198.51.100.1": false,
		"203.0.113.9": false, "198.18.0.1": false, "240.0.0.1": false, "255.255.255.255": false,
		"224.0.0.1": false, "239.255.255.250": false, "ff02::1": false,
		"::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false, "::ffff:169.254.169.254": false, "::ffff:8.8.8.8": true,
		"64:ff9b::7f00:1": false, "64:ff9b::808:808": false, "2002:7f00:1::": false, "2001::1": false, "2001:db8::1": false,
		"::7f00:1": false, "fec0::1": false,
	} {
		ip := netip.MustParseAddr(addr)
		if got := isPublic(ip); got != want {
			t.Errorf("isPublic(%s) = %v, want %v", addr, got, want)
		}
	}
}

// loopback serves h on 127.0.0.1 and counts the requests that reach it.
func loopback(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestTheGuardRefusesTheLoopbackForReal: the real client, against real servers on
// the machine's own loopback, in every spelling of the address a test can write
// without asking a resolver (127.1 and 2130706433 are refused earlier, by admit),
// never connects, and says it was refused.
func TestTheGuardRefusesTheLoopbackForReal(t *testing.T) {
	srv, hits := loopback(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) })
	_, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	c := newClient(isPublic)
	for _, host := range []string{
		"127.0.0.1", "[::ffff:127.0.0.1]", "[::ffff:7f00:1]", "[::1]", "0.0.0.0",
		"10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254", "[fd00:ec2::254]", "100.100.100.200", "[64:ff9b::7f00:1]",
	} {
		u, err := url.Parse("http://" + host + ":" + port + "/")
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		r, err := getWith(ctx, c, u)
		cancel()
		if err == nil {
			t.Errorf("%s: fetched the loopback: %+v", host, r)
		} else if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v, want it to wrap ErrRefused", host, err)
		}
	}
	// A name that resolves to the loopback is refused the same way.
	u, _ := url.Parse("http://localhost:" + port + "/")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if r, err := getWith(ctx, c, u); err == nil {
		t.Errorf("localhost: fetched the loopback: %+v", r)
	}
	cancel()
	if hits.Load() != 0 {
		t.Fatalf("the loopback server was reached %d times", hits.Load())
	}
	// The same server is reachable by a client that allows it, so the refusals
	// above are the guard's and not the network's; and the address the guard was
	// asked about is the address that was dialed.
	var asked atomic.Value
	open := newClient(func(ip netip.Addr) bool { asked.Store(ip.String()); return true })
	u, _ = url.Parse(srv.URL)
	r, err := getWith(context.Background(), open, u)
	if err != nil || string(r.Body) != "secret" || hits.Load() != 1 {
		t.Fatalf("the open client = %+v %v, hits %d", r, err, hits.Load())
	}
	if asked.Load() != "127.0.0.1" {
		t.Fatalf("the guard judged %v, the server is on 127.0.0.1", asked.Load())
	}
}

// TestARedirectIsNeverFollowed: the answer is the redirect itself, and the target
// is not requested.
func TestARedirectIsNeverFollowed(t *testing.T) {
	target, hit := loopback(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("metadata")) })
	for _, to := range []string{"http://169.254.169.254/latest/meta-data/", "http://localhost/", target.URL} {
		srv, _ := loopback(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) })
		u, _ := url.Parse(srv.URL)
		r, err := getWith(context.Background(), newClient(func(netip.Addr) bool { return true }), u)
		if err != nil || r.Status != http.StatusFound || r.Location != to {
			t.Fatalf("redirect to %s = %+v %v", to, r, err)
		}
	}
	if hit.Load() != 0 {
		t.Fatal("a redirect was followed to its target")
	}
}

// TestTheBodyAndTheClockAreBounded: 512 KiB, five seconds, no proxy, no reuse of
// a connection, and one fixed User-Agent.
func TestTheBodyAndTheClockAreBounded(t *testing.T) {
	if fetchTimeout != 5*time.Second || fetchLimit != 512<<10 || fetchAgent != "HanzoRewards/1 (+https://hanzo.ai/leaderboard)" {
		t.Fatalf("bounds = %s %d %q", fetchTimeout, fetchLimit, fetchAgent)
	}
	tr := guarded.Transport.(*http.Transport)
	if tr.Proxy != nil || !tr.DisableKeepAlives || guarded.Timeout != fetchTimeout || guarded.CheckRedirect == nil {
		t.Fatalf("the guarded client is not the bounded one: %+v", tr)
	}
	var agent atomic.Value
	big, _ := loopback(t, func(w http.ResponseWriter, r *http.Request) {
		agent.Store(r.Header.Get("User-Agent"))
		_, _ = w.Write([]byte(strings.Repeat("a", fetchLimit+1)))
	})
	edge, _ := loopback(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("a", fetchLimit))) })
	open := newClient(func(netip.Addr) bool { return true })
	u, _ := url.Parse(big.URL)
	if _, err := getWith(context.Background(), open, u); !errors.Is(err, errTooLarge) {
		t.Fatalf("a body past the limit: %v", err)
	}
	if agent.Load() != fetchAgent {
		t.Fatalf("user agent = %v", agent.Load())
	}
	u, _ = url.Parse(edge.URL)
	if r, err := getWith(context.Background(), open, u); err != nil || len(r.Body) != fetchLimit {
		t.Fatalf("a body at the limit: %d %v", len(r.Body), err)
	}
	release := make(chan struct{})
	slow, _ := loopback(t, func(w http.ResponseWriter, r *http.Request) { <-release })
	t.Cleanup(func() { close(release) })
	u, _ = url.Parse(slow.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := getWith(ctx, open, u); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("a slow server: %v after %s", err, time.Since(start))
	}
}

// pages answers fetch from a table and records what was asked. An address with
// no page is an error, so a test that asks for something it did not expect fails.
type pages struct {
	m     map[string]reply
	err   map[string]error
	calls []string
}

// stand replaces fetch with a table for the length of the test.
func stand(t *testing.T) *pages {
	t.Helper()
	p := &pages{m: map[string]reply{}, err: map[string]error{}}
	was := fetch
	fetch = func(_ context.Context, raw string) (reply, error) {
		p.calls = append(p.calls, raw)
		if err, ok := p.err[raw]; ok {
			return reply{}, err
		}
		if r, ok := p.m[raw]; ok {
			return r, nil
		}
		return reply{}, fmt.Errorf("no canned page for %s", raw)
	}
	t.Cleanup(func() { fetch = was })
	return p
}

// TestShortLinksAreFollowedByHandAndBounded: a short link is followed one hop at a
// time, three at most, only on listed shortener hosts, and a hop to anywhere else
// is returned and never fetched.
func TestShortLinksAreFollowedByHandAndBounded(t *testing.T) {
	const home = "https://hanzo.ai/beta?ref=ADA1"
	for _, tc := range []struct {
		name  string
		chain map[string]reply
		from  string
		want  string
		calls int
	}{
		{"a link on hanzo.ai is not fetched", nil, home, home, 0},
		{"one hop", map[string]reply{"https://t.co/a": {Status: 301, Location: home}}, "https://t.co/a", home, 1},
		{"three hops", map[string]reply{
			"https://t.co/a":        {Status: 302, Location: "https://bit.ly/b"},
			"https://bit.ly/b":      {Status: 301, Location: "https://tinyurl.com/c"},
			"https://tinyurl.com/c": {Status: 307, Location: home},
		}, "https://t.co/a", home, 3},
		{"four hops stop at three", map[string]reply{
			"https://t.co/a":        {Status: 302, Location: "https://bit.ly/b"},
			"https://bit.ly/b":      {Status: 301, Location: "https://tinyurl.com/c"},
			"https://tinyurl.com/c": {Status: 307, Location: "https://is.gd/d"},
			"https://is.gd/d":       {Status: 307, Location: home},
		}, "https://t.co/a", "https://is.gd/d", 3},
		{"a relative Location", map[string]reply{"https://bit.ly/a": {Status: 301, Location: "/b"}, "https://bit.ly/b": {Status: 301, Location: home}}, "https://bit.ly/a", home, 2},
		{"a page that redirects by meta refresh", map[string]reply{"https://t.co/a": {Status: 200, Body: []byte(`<noscript><META http-equiv="refresh" content="0;URL=https://hanzo.ai/beta?ref=ADA1"></noscript>`)}}, "https://t.co/a", home, 1},
		{"a page that redirects by script", map[string]reply{"https://t.co/a": {Status: 200, Body: []byte(`<script>location.replace("https:\/\/hanzo.ai\/beta?ref=ADA1")</script>`)}}, "https://t.co/a", home, 1},
		{"a redirect to the metadata address is not fetched", map[string]reply{"https://t.co/a": {Status: 302, Location: "https://169.254.169.254/latest/meta-data/"}}, "https://t.co/a", "https://169.254.169.254/latest/meta-data/", 1},
		{"a redirect to http metadata is not fetched", map[string]reply{"https://t.co/a": {Status: 302, Location: "http://169.254.169.254/"}}, "https://t.co/a", "http://169.254.169.254/", 1},
		{"a redirect to localhost is not fetched", map[string]reply{"https://t.co/a": {Status: 302, Location: "https://localhost/admin"}}, "https://t.co/a", "https://localhost/admin", 1},
		{"a redirect to another site is not fetched", map[string]reply{"https://t.co/a": {Status: 302, Location: "https://example.com/?ref=ADA1"}}, "https://t.co/a", "https://example.com/?ref=ADA1", 1},
		{"a short link that is gone stays what it is", map[string]reply{"https://t.co/gone": {Status: 404}}, "https://t.co/gone", "https://t.co/gone", 1},
		{"a link on an unlisted site is not fetched", nil, "https://example.com/x", "https://example.com/x", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := stand(t)
			for k, v := range tc.chain {
				p.m[k] = v
			}
			got, err := resolveLink(context.Background(), tc.from)
			if err != nil || got != tc.want || len(p.calls) != tc.calls {
				t.Fatalf("resolve = %q %v after %v, want %q after %d fetches", got, err, p.calls, tc.want, tc.calls)
			}
		})
	}
	// A fetch that fails is an error, so the caller tries again later.
	p := stand(t)
	p.err["https://t.co/a"] = fmt.Errorf("connection reset")
	if _, err := resolveLink(context.Background(), "https://t.co/a"); err == nil {
		t.Fatal("a failed fetch was taken for a link that is not ours")
	}
	// A shortener that will not answer now is an error too, not a link that is not ours.
	for _, status := range []int{429, 500, 502, 503} {
		p.m["https://t.co/busy"] = reply{Status: status}
		if got, err := resolveLink(context.Background(), "https://t.co/busy"); err == nil {
			t.Fatalf("a shortener that answered %d was taken for a link that is not ours: %q", status, got)
		}
	}
	// A refusal is permanent and is not an error: the link is not ours.
	p.err["https://t.co/b"] = ErrRefused
	if got, err := resolveLink(context.Background(), "https://t.co/b"); err != nil || got != "https://t.co/b" {
		t.Fatalf("a refused fetch = %q %v", got, err)
	}
}

// TestTheNewHostsAreFetchedAndOnlyThey: the hosts DEV, Medium and Hashnode are read at pass the name wall, and nothing beside them does: not a sibling
// host, not a deeper name under hashnode.dev, not a blog on its own domain.
func TestTheNewHostsAreFetchedAndOnlyThey(t *testing.T) {
	for _, host := range []string{"dev.to", "medium.com", "hashnode.com", "ada.hashnode.dev", "a-1.hashnode.dev", "x.hashnode.dev"} {
		if !fetchable(host) {
			t.Errorf("fetchable(%q) = false, want true", host)
		}
	}
	for _, host := range []string{
		"www.tiktok.com", "tiktok.com", "m.tiktok.com", "vm.tiktok.com", "vt.tiktok.com", "www.dev.to", "api.dev.to", "www.medium.com", "pub.medium.com",
		"hashnode.dev", "a.b.hashnode.dev", ".hashnode.dev", "-a.hashnode.dev", "a-.hashnode.dev", "ada.hashnode.com", "evilhashnode.dev",
		"hashnode.dev.evil.com", "ada.hashnode.dev.evil.com", "hashnode.com.evil.com", "ada.hashnode.devx", "ada.xhashnode.dev",
		"www.youtube.com", "youtube.com", "youtu.be", "threads.com", "www.instagram.com", "www.facebook.com", "fb.watch", "www.linkedin.com", "",
	} {
		if fetchable(host) {
			t.Errorf("fetchable(%q) = true, want false", host)
		}
	}
}

// stubDNS answers every lookup of an address of addr's family with addr, and the
// other family with nothing, and makes the process resolver ask only it, for the
// length of the test. asked says the names it was asked for.
func stubDNS(t *testing.T, addr string) (asked func() []string) {
	t.Helper()
	ip := netip.MustParseAddr(addr)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var names []string
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			// The question: a name of length-prefixed labels, a type, a class.
			end, name := 12, ""
			for end < len(q) && q[end] != 0 {
				name += string(q[end+1:end+1+int(q[end])]) + "."
				end += 1 + int(q[end])
			}
			if end+5 > len(q) {
				continue
			}
			qtype := int(q[end+1])<<8 | int(q[end+2])
			mu.Lock()
			names = append(names, strings.TrimSuffix(name, "."))
			mu.Unlock()
			answer := (qtype == 1 && ip.Is4()) || (qtype == 28 && ip.Is6())
			resp := append([]byte{}, q[:2]...)
			resp = append(resp, 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0)
			if answer {
				resp[7] = 1
			}
			resp = append(resp, q[12:end+5]...)
			if answer {
				resp = append(resp, 0xC0, 0x0C, byte(qtype>>8), byte(qtype), 0, 1, 0, 0, 0, 60, 0, byte(ip.BitLen()/8))
				resp = append(resp, ip.AsSlice()...)
			}
			_, _ = conn.WriteTo(resp, from)
		}
	}()
	was := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", conn.LocalAddr().String())
	}}
	t.Cleanup(func() {
		net.DefaultResolver = was
		_ = conn.Close()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(names)
	}
}

// TestEveryAllowedHostIsRefusedWhenItResolvesToAPrivateAddress: the address wall,
// for each host the reader fetches, the new ones among them. The name is listed, so
// it passes the name wall and is looked up; the answer is loopback, private,
// link-local, shared or unique-local, and the real fetcher refuses it before it
// connects.
func TestEveryAllowedHostIsRefusedWhenItResolvesToAPrivateAddress(t *testing.T) {
	hosts := slices.Concat(apiHosts, mastodonHosts, shortHosts, []string{"ada.hashnode.dev", "a-1.hashnode.dev"})
	for _, host := range []string{"dev.to", "medium.com", "hashnode.com", "ada.hashnode.dev"} {
		if !slices.Contains(hosts, host) {
			t.Fatalf("%s is not among the hosts tried", host)
		}
	}
	for _, addr := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.0.9", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fd00:ec2::254", "::ffff:127.0.0.1"} {
		asked := stubDNS(t, addr)
		for _, host := range hosts {
			before := len(asked())
			_, err := guardedGet(context.Background(), "https://"+host+"/")
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "not public") {
				t.Errorf("%s resolving to %s = %v, want a refusal for an address that is not public", host, addr, err)
				continue
			}
			if !slices.Contains(asked()[before:], host) {
				t.Errorf("%s resolving to %s was refused without being looked up", host, addr)
			}
		}
	}
	// A host that is not listed is refused before any lookup is made.
	asked := stubDNS(t, "127.0.0.1")
	for _, host := range []string{"evil.com", "ada.hashnode.com", "a.b.hashnode.dev", "www.youtube.com", "blog.example.com"} {
		if _, err := guardedGet(context.Background(), "https://"+host+"/"); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "not a host the reader fetches") {
			t.Errorf("%s = %v, want a refusal of an unlisted host", host, err)
		}
	}
	if got := asked(); len(got) != 0 {
		t.Fatalf("an unlisted host was looked up: %v", got)
	}
}
