package crawl

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Request is the /v1/crawl body. One URL per call: batching would make the
// response a partial-failure envelope that every caller then has to unpack, and no
// caller has asked for more than one.
type Request struct {
	URL string `json:"url"`
}

// Response is the /v1/crawl body.
//
// Success is a field rather than an HTTP status because "the page could not be
// fetched" is a normal outcome of asking about a URL, not a fault of the request:
// the caller sent a well-formed ask and gets a well-formed answer saying the page
// was unreachable. Reserving non-2xx for auth and malformed input keeps a caller's
// error handling honest — a 200 means the surface worked, and Success says what it
// found.
type Response struct {
	Success bool      `json:"success"`
	Data    *Document `json:"data,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Document is the crawled page.
type Document struct {
	URL      string         `json:"url"`
	Title    string         `json:"title,omitempty"`
	Markdown string         `json:"markdown"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// serviceKey is the shared key a service caller presents. It is deliberately the
// SAME credential the web-search surface takes (WEBSEARCH_API_KEY), not a second
// one: both surfaces exist for the same caller — the chat server, reaching cloud
// service-to-service with no user principal — and a second key would be a second
// thing to mint, mount and rotate, with nothing distinguishing when to use which.
//
// The name still says WEBSEARCH because that is the key that is minted and mounted
// today; renaming a live credential is its own coordinated change, and doing it
// inside this one would put a rename in the path of a fix.
func serviceKey() string { return strings.TrimSpace(os.Getenv("WEBSEARCH_API_KEY")) }

// Mount registers /v1/crawl.
//
// The gate mirrors /v1/websearch/search exactly, and it is not optional here. This
// surface fetches a URL the caller chooses, from inside the cluster — an open one
// is a proxy into the private network, and the address guard in crawl.go is the
// second line of that defence, not the first. A caller is admitted with EITHER a
// validated principal (a signed-in user, already authenticated and metered) OR the
// shared service key. Neither ⇒ refused. An unset key 503s rather than defaulting
// open, so a misconfigured deploy fails closed and loudly.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("crawl.Mount: nil app")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("crawl.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "crawl")

	direct := zip.AdaptNetHTTP(http.HandlerFunc(handle))
	keyed := zip.AdaptNetHTTP(guard(http.HandlerFunc(handle)))

	g := app.Group("/v1/crawl")
	g.Post("", func(c *zip.Ctx) error {
		if principal.Validated(c) {
			return direct(c)
		}
		return keyed(c)
	})
	g.Post("/", func(c *zip.Ctx) error {
		if principal.Validated(c) {
			return direct(c)
		}
		return keyed(c)
	})

	logger.Info("crawl surface mounted (native in-process fetch + extract; no external crawler)")
	return nil
}

// guard admits a caller holding the shared service key, presented as either
// X-API-Key or a Bearer. Both are accepted because the two clients that reach this
// surface already differ on that point and neither is wrong; requiring one would
// break a working caller to no benefit.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := serviceKey()
		if want == "" {
			writeJSON(w, http.StatusServiceUnavailable, Response{Error: "crawl not configured"})
			return
		}
		got := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if got == "" {
			got = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		}
		// Constant-time: a byte-at-a-time comparison leaks the key's prefix to a
		// caller willing to time enough requests.
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeJSON(w, http.StatusUnauthorized, Response{Error: "invalid api key"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handle(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		writeJSON(w, http.StatusBadRequest, Response{Error: "missing url"})
		return
	}

	page, err := Fetch(r.Context(), req.URL)
	if err != nil {
		// 200 with Success:false — see the note on Response. The message is the
		// error verbatim: a caller debugging a failed crawl needs to know whether the
		// host was refused, unreachable, or served the wrong type.
		writeJSON(w, http.StatusOK, Response{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, Response{
		Success: true,
		Data: &Document{
			URL:      page.URL,
			Title:    page.Title,
			Markdown: page.Markdown,
			Metadata: page.Metadata,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
