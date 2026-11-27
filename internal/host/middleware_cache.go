package host

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/zap-proto/zip"
)

// credentialHeaders are the request headers that carry a caller's identity
// (hanzoai/outbound's secret set, plus the bare Api-Key event capture reads).
// An answer to a request carrying any of them may be computed for that caller.
var credentialHeaders = []string{"Authorization", "Cookie", "X-Api-Key", "Api-Key"}

// EdgeCache says, once for every route, what a cache may keep of a GET or HEAD.
//
//   - A request carrying a credential is answered for its caller: when the
//     handler said nothing, the answer is `private, no-store`, so no browser or
//     shared cache keeps it.
//   - A 2xx answer to a request carrying none is public by construction — it was
//     computed for nobody in particular. Where the platform edge policy gives the
//     path a TTL (edge.Policy CachePaths, CacheTTLSec) and the handler said
//     nothing, it becomes `public, max-age=<≤60>, s-maxage=<ttl>`.
//   - Any public answer, declared here or by its handler, carries a weak ETag
//     over the body, and a matching If-None-Match gets a 304 with no body. Weak,
//     because the same entity also leaves as markdown (Negotiation,
//     Vary: Accept) and compressed.
//
// A Vary a caller varies by stays true: EdgeCORS appends Origin for every
// cross-origin request it answers per origin and Negotiation appends
// Accept, and nothing here removes either. An anonymous read of a catalog path is
// answered to every origin with `*` and no Vary: Origin, so it is one copy.
// Cloudflare keys its cache on the URL alone, so the edge rule (hanzo-inc/universe
// infra/cf-zones/cache.py) keeps a markdown Accept away from the shared copy, and
// a request with an Origin too unless the path is one of those.
func EdgeCache(pol *edge.Store) zip.Handler {
	return func(c *zip.Ctx) error {
		method := c.Method()
		if method != http.MethodGet && method != http.MethodHead {
			return c.Continue()
		}
		credentialed := false
		for _, h := range credentialHeaders {
			if c.Header(h) != "" {
				credentialed = true
				break
			}
		}
		path := c.Path()
		if err := c.Continue(); err != nil {
			return err
		}

		resp := c.Fiber().Response()
		declared := strings.TrimSpace(string(resp.Header.Peek("Cache-Control")))
		if credentialed {
			if declared == "" {
				resp.Header.Set("Cache-Control", "private, no-store")
			}
			return nil
		}
		status := resp.StatusCode()
		if status < 200 || status >= 300 || len(resp.Header.Peek("Set-Cookie")) > 0 {
			return nil
		}
		if declared == "" {
			ttl := pol.PublicTTL(path)
			if ttl <= 0 {
				return nil
			}
			declared = publicCache(ttl)
			resp.Header.Set("Cache-Control", declared)
		}
		if !isPublic(declared) || method == http.MethodHead || resp.IsBodyStream() {
			return nil
		}

		etag := string(resp.Header.Peek("ETag"))
		if etag == "" {
			sum := sha256.Sum256(resp.Body())
			etag = `W/"` + base64.RawURLEncoding.EncodeToString(sum[:20]) + `"`
			resp.Header.Set("ETag", etag)
		}
		if etagMatches(c.Header("If-None-Match"), etag) {
			resp.SetStatusCode(http.StatusNotModified)
			resp.ResetBody()
		}
		return nil
	}
}

// staleFor is how long, past its ttl, a cache may serve a public answer while it
// fetches the next one: a day. A reader on a slow network is then answered from
// the nearest copy at once, and the copy is replaced behind them.
const staleFor = 86400

// publicCache is the Cache-Control for a public answer the edge keeps ttl
// seconds: a browser holds it at most a minute, the edge the full ttl, and
// either may serve it stale for a day while it revalidates.
func publicCache(ttl int) string {
	browser := min(ttl, 60)
	return "public, max-age=" + strconv.Itoa(browser) + ", s-maxage=" + strconv.Itoa(ttl) +
		", stale-while-revalidate=" + strconv.Itoa(staleFor)
}

// isPublic reports whether a Cache-Control value lets a shared cache keep the
// answer.
func isPublic(cc string) bool {
	public := false
	for d := range strings.SplitSeq(strings.ToLower(cc), ",") {
		switch strings.TrimSpace(d) {
		case "public":
			public = true
		case "private", "no-store", "no-cache":
			return false
		}
	}
	return public
}

// etagMatches is RFC 9110 §13.1.2's weak comparison of an If-None-Match list
// against one tag.
func etagMatches(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	want := strings.TrimPrefix(etag, "W/")
	for tag := range strings.SplitSeq(ifNoneMatch, ",") {
		if strings.TrimPrefix(strings.TrimSpace(tag), "W/") == want {
			return true
		}
	}
	return false
}
