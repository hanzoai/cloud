package preview

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/zap-proto/zip"
)

// Prefix is where the router hands a preview request to the sandbox app.
const Prefix = "/v1/sandbox/preview/"

// label is a preview host's first label: the sandbox's 24 hex digits and the port.
var label = regexp.MustCompile(`^sandbox-([0-9a-f]{24})-preview-([1-9][0-9]{0,4})$`)

// Apex is the domain previews are served under: the sites apex.
func Apex() string { return sites.ConfigFromEnv("").Apex }

// Host is the preview host of port in the sandbox whose id is hex.
func Host(hex string, port int) string {
	return "sandbox-" + hex + "-preview-" + strconv.Itoa(port) + "." + Apex()
}

// Parse reads a preview host: the sandbox's hex and the port, or false for any
// other host.
func Parse(host string) (hex string, port int, ok bool) { return parse(host, Apex()) }

func parse(host, apex string) (hex string, port int, ok bool) {
	first, rest, found := strings.Cut(strings.ToLower(host), ".")
	if !found || rest != apex {
		return "", 0, false
	}
	m := label.FindStringSubmatch(first)
	if m == nil {
		return "", 0, false
	}
	port, err := strconv.Atoi(m[2])
	if err != nil || port > 65535 {
		return "", 0, false
	}
	return m[1], port, true
}

// Middleware rewrites a request for a preview host onto Prefix, and passes
// every other request on untouched. It must run before the sandbox app is
// mounted, since a rewrite reaches only what is composed after it.
//
// It runs twice on the way in — in the router, and again in the sandbox app's
// own process, which composes the same root — so a path already rewritten is
// left as it is.
func Middleware() zip.Handler {
	apex := Apex()
	return func(c *zip.Ctx) error {
		if hex, port, ok := parse(sites.Host(c), apex); ok {
			if at := Prefix + hex + "/" + strconv.Itoa(port); c.Path() != at && !strings.HasPrefix(c.Path(), at+"/") {
				c.Fiber().Path(at + c.Path())
			}
		}
		return c.Next()
	}
}
