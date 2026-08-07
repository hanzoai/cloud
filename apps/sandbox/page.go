package sandbox

// page.go — the terminal as a PAGE, so that anything with an iframe already has
// a terminal.
//
// The socket alone is not a product. Every host that wants to show a shell —
// the console's dock, tabs' panes, whatever comes next — would have to carry its
// own terminal emulator, its own resize arithmetic and its own reconnect
// affordance, and the day one of them is wrong is the day one product's terminal
// is subtly worse than another's. So the terminal is served: ONE document, from
// the same address as the socket, and a host embeds it.
//
// IT IS A CONSTANT. The page never has anything substituted into it, because it
// reads its own location: the socket is this document's path plus `/ws`, carrying
// this document's query string unchanged, and the ticket and the session name are
// already in that query string. A document with no substitution has no injection
// surface, so the whole class of "a value from the URL reached the markup" simply
// does not exist here.
//
// AND IT IS SELF-CONTAINED. xterm ships inline rather than from an address,
// because a terminal that fetches its emulator from somewhere else is a terminal
// that stops working when the somewhere else does — and a strict page is one that
// needs no origin but its own. The cost is one large response; the alternative is
// a second surface to serve, version and keep in step with this one.

import (
	_ "embed"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/brand"
	"github.com/zap-proto/zip"
)

//go:embed term/page.html
var pageHTML string

//go:embed term/xterm.js
var xtermJS string

//go:embed term/fit.js
var fitJS string

//go:embed term/xterm.css
var xtermCSS string

// document is the assembled page, built once. The three vendored files are
// substituted into their markers here rather than being concatenated at build
// time, so what is in the repository is what upstream published and the seams are
// visible in the template instead of in a script nobody runs.
var document = sync.OnceValue(func() string {
	r := strings.NewReplacer("__CSS__", xtermCSS, "__XTERM__", xtermJS, "__FIT__", fitJS)
	return r.Replace(pageHTML)
})

// framers is who may put this page in a frame.
//
// It is DERIVED from the brand registry and not listed, because a list would rot:
// tabs.hanzo.ai is one host of many that will ever want a terminal, and the next
// one is a subdomain somebody adds without finding this file. Every brand's own
// domains, and only those, may frame it.
//
// It is defence in depth rather than the gate. The page is inert markup whose
// only power comes from the ticket in its own URL, and a hostile origin cannot
// mint one — what this closes is the clickjack: a terminal framed invisibly under
// something a user is willing to click.
var framers = sync.OnceValue(func() string {
	seen := map[string]bool{}
	for _, d := range brand.Domains() {
		if d = strings.TrimSpace(d); d != "" {
			// The apex AND its subdomains: tabs, console and the API host are all
			// subdomains, and a policy naming only the apex would frame none of them.
			seen["https://"+d] = true
			seen["https://*."+d] = true
		}
	}
	out := make([]string, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Strings(out)
	return "frame-ancestors 'self' " + strings.Join(out, " ")
})

// serve answers the terminal page.
//
// It does NOT redeem the ticket, and that is the whole reason the page and the
// socket are two addresses: a ticket is spent once, and spending it here would
// leave the page holding a credential that no longer opens anything. The page is
// markup — the socket is the gate, and it is the socket that checks.
func serve(s *Service, c *zip.Ctx) error {
	// The page is a constant, so its own headers are the only thing that can vary,
	// and both of these are about where it may be shown rather than what it says.
	c.SetHeader("Content-Security-Policy", framers())
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("Content-Type", "text/html; charset=utf-8")
	return c.String(http.StatusOK, document())
}
