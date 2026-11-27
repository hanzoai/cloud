// Package crawl is any web page turned into clean markdown a model can read.
//
// Hanzo Crawl: fetch one URL and return its readable content as markdown,
// in-process, in Go.
//
// The FETCH is in-process Go rather than a dial to a separate service, which is
// the same move already made for the SEARCH half in apps/websearch/search.go:
// one fewer non-Go dependency, one fewer thing that can be down, and no network
// hop for work that is a fetch and a parse.
//
// A browser still lives out of process, because rendering is not a fetch and a
// parse. `crawl` (ghcr.io/hanzoai/crawl, headless Chromium) is deployed at
// crawl.hanzo.svc:11235 and browser.go escalates to it for the pages this file
// cannot read alone.
//
// The engine is three orthogonal steps, each in its own file and each testable
// without the others:
//
//	Fetch    (this file)  URL   → HTML bytes, over guarded HTTP
//	extract  (extract.go) HTML  → the readable subtree + metadata
//	render   (markdown.go) node → markdown
//
// # Fetching untrusted URLs from inside the cluster is the security boundary
//
// The caller supplies the URL, so this is a server-side request forgery primitive
// by construction, and it runs INSIDE the cluster with an in-namespace service DNS
// and a cloud metadata endpoint on 169.254.169.254 that hands out credentials to
// anyone who asks. A naive fetcher here is a credential exfiltration hole, not a
// bug. The separate service got network policy for free by being a separate pod;
// folding it in means this package must carry the guard itself.
//
// So: only http/https, and every address actually dialed must be public. The
// fetch goes through hanzoai/outbound, whose client judges the address in the
// DIALER, not on the hostname, because checking a hostname up front and then
// letting the transport resolve it again is a time-of-check/time-of-use gap — DNS
// rebinding walks straight through it. Dialing is the only moment the real
// destination is known, and that is where it is enforced. Redirects are followed
// but re-enter the same dialer, so a public URL that 302s to 169.254.169.254 is
// refused at the hop that matters.
package crawl
