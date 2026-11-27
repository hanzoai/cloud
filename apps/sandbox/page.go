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
	"encoding/base64"
	"net/http"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/sdk/brand"
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

//go:embed term/font.woff2
var fontWOFF2 []byte

// document is the assembled page, built once. The four vendored files are
// substituted into their markers here rather than being concatenated at build
// time, so what is in the repository is what upstream published and the clients are
// visible in the template instead of in a script nobody runs.
var document = sync.OnceValue(func() string {
	fontB64 := base64.StdEncoding.EncodeToString(fontWOFF2)
	r := strings.NewReplacer(
		"__CSS__", xtermCSS,
		"__XTERM__", xtermJS,
		"__FIT__", fitJS,
		"__FONT__", fontB64,
	)
	return r.Replace(pageHTML)
})

// framers is who may put this page in a frame: the surfaces the brand registry
// NAMES as framing it (brand.Info.Framers), origin by origin.
//
// It was every brand domain and every subdomain of one, which admitted every
// customer site — each <slug>.hanzo.app is a customer's — to frame a terminal
// typing into somebody's sandbox. A wildcard over an apex is a statement about
// every host anyone is ever given under it, so there is none: a surface that
// embeds a terminal or a screen is added to the registry by name.
//
// It is defence in depth rather than the gate. The page is inert markup whose
// only power comes from the ticket in its own URL, and a hostile origin cannot
// mint one — what this closes is the clickjack: a terminal framed invisibly under
// something a user is willing to click.
// desktopFramers are the fixed Tauri webview origins Hanzo Desktop runs under
// (tauri://localhost on macOS/Linux, http://tauri.localhost on Windows).
// Tabs no longer inlines the terminal document (hanzo-inc/tabs#6), so the
// iframe loads directly from api.hanzo.ai and needs Desktop admitted here.
var desktopFramers = []string{"tauri://localhost", "http://tauri.localhost"}

var framers = sync.OnceValue(func() string {
	all := make([]string, 0, len(brand.Framers())+len(desktopFramers))
	all = append(all, brand.Framers()...)
	all = append(all, desktopFramers...)
	return "frame-ancestors 'self' " + strings.Join(all, " ")
})

// serve answers one of these pages — the terminal's (above) or the screen's
// (screen.go). They differ only in the document, so how a page is served is
// written once: the headers are the whole of it, and a second copy of them is a
// second answer to who may frame us.
//
// It does NOT redeem the ticket, and that is the whole reason the page and the
// socket are two addresses: a ticket is spent once, and spending it here would
// leave the page holding a credential that no longer opens anything. The page is
// markup — the socket is the gate, and it is the socket that checks.
func serve(doc func() string) func(*Service, *zip.Ctx) error {
	return func(s *Service, c *zip.Ctx) error {
		// A page is a constant, so its own headers are the only thing that can vary,
		// and both of these are about where it may be shown rather than what it says.
		c.SetHeader("Content-Security-Policy", framers())
		c.SetHeader("Cache-Control", "no-store")
		c.SetHeader("Content-Type", "text/html; charset=utf-8")
		return c.String(http.StatusOK, doc())
	}
}
