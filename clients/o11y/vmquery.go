package o11y

// vmquery.go — the VictoriaMetrics read client shared by the scoped o11y surface.
// status.go's VM up-inventory uses newVMClient()/promLabel() over the vmClient
// defined here (status.go owns the queryInstant method + the instant-response
// envelope). The VM read endpoint is resolved from O11Y_VM_URL, and when it is
// unset/unreachable every query degrades to an honest-empty result rather than a
// synthesized series — the "honest, never fabricated" contract the whole surface
// follows.

import (
	"net/http"
	"os"
	"strings"
	"time"
)

// vmClient reads a Prometheus/VictoriaMetrics HTTP API. status.go hangs the
// queryInstant method off this type; the fields it needs are the read base URL and
// an HTTP client.
type vmClient struct {
	base string
	http *http.Client
}

const vmQueryTimeout = 5 * time.Second

// newVMClient builds the VM read client from O11Y_VM_URL (the in-cluster
// VictoriaMetrics / vmselect query endpoint, e.g. http://victoria-metrics.hanzo.svc:8428).
// An EMPTY base makes every query fail cleanly, so an un-wired VM degrades to an
// honest-empty inventory — never a fabricated replica.
func newVMClient() *vmClient {
	return &vmClient{
		base: strings.TrimRight(os.Getenv("O11Y_VM_URL"), "/"),
		http: &http.Client{Timeout: vmQueryTimeout},
	}
}

// promLabel escapes a PromQL double-quoted label value so a product/service id can
// never break out of the SERVER-built selector. Defense in depth — resolveService
// already allowlists the id before it reaches a query.
func promLabel(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", "",
		"\r", "",
		"{", "",
		"}", "",
	).Replace(s)
}
