package cloud

// Markdown content-negotiation middleware. ONE data model (the structured value
// a handler produces via c.JSON), MANY serialization adapters chosen by content
// negotiation: JSON for machines (the default), markdown for agents. It touches
// NO handler — handlers keep returning structured data; this re-serializes the
// already-encoded JSON body through zap-proto/md when the caller asks for
// markdown, so token-efficient, agent-ready output is a request-time choice, not
// a second code path.
//
// A caller selects markdown with `Accept: text/markdown` or `?format=md`; JSON
// stays the default. Endpoints in cfg.MarkdownDefaultPrefixes (e.g. /v1/code/,
// /v1/agents/) may default to markdown, but a caller always keeps the override
// (?format=json or Accept: application/json).
//
// Fail-safe by construction: only SUCCESSFUL (err==nil) application/json
// responses are considered, and any md render error leaves the JSON body
// untouched — a formatting choice can never turn a 200 into a 500. Error bodies,
// streams (SSE), and non-JSON payloads (HTML/SPA, S3 bytes) pass through as-is.

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/zap-proto/md"
	"github.com/zap-proto/zip"
)

// MarkdownNegotiation returns the response-path middleware. Register it ONCE at
// the compose root and every /v1 endpoint negotiates markdown uniformly.
func MarkdownNegotiation(defaultPrefixes []string) zip.Handler {
	return func(c *zip.Ctx) error {
		want := wantsMarkdown(c.Path(), c.Header("Accept"), c.Query("format"), defaultPrefixes)

		if err := c.Continue(); err != nil {
			return err // error → JSON error body via the error handler; never transformed.
		}

		resp := c.Fiber().Response()
		// The representation varies by Accept — keep a cache from cross-serving
		// markdown to a JSON client and vice versa.
		resp.Header.Set("Vary", "Accept")
		if !want {
			return nil
		}
		if !isJSONContentType(string(resp.Header.ContentType())) {
			return nil
		}
		body := resp.Body()
		if len(body) == 0 {
			return nil
		}
		rendered, err := md.Render(json.RawMessage(body))
		if err != nil {
			return nil // fail-safe: keep the JSON response exactly as written.
		}
		resp.SetBody(rendered)
		resp.Header.SetContentType("text/markdown; charset=utf-8")
		return nil
	}
}

// wantsMarkdown resolves the negotiation. Precedence: an explicit ?format wins,
// then the Accept header, then a default-markdown path prefix; JSON otherwise.
func wantsMarkdown(path, accept, format string, prefixes []string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "md", "markdown", "text/markdown":
		return true
	case "json", "application/json":
		return false
	}
	switch acceptPreference(accept) {
	case prefMarkdown:
		return true
	case prefJSON:
		return false
	}
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

type acceptPref int

const (
	prefUnset acceptPref = iota
	prefMarkdown
	prefJSON
)

// acceptPreference picks markdown vs JSON from an Accept header by comparing the
// q-values of text/markdown and application/json. Absent either, it is unset so
// the caller falls through to the path-prefix default.
func acceptPreference(accept string) acceptPref {
	if strings.TrimSpace(accept) == "" {
		return prefUnset
	}
	qMD, qJSON := -1.0, -1.0
	for _, part := range strings.Split(accept, ",") {
		mt, q := parseMediaRange(part)
		switch mt {
		case "text/markdown", "text/x-markdown":
			if q > qMD {
				qMD = q
			}
		case "application/json":
			if q > qJSON {
				qJSON = q
			}
		}
	}
	switch {
	case qMD < 0 && qJSON < 0:
		return prefUnset
	case qMD < 0:
		return prefJSON
	case qJSON < 0:
		return prefMarkdown
	case qMD >= qJSON:
		return prefMarkdown
	default:
		return prefJSON
	}
}

// parseMediaRange splits "type/subtype;q=0.9" into the lowercased media type and
// its quality (default 1.0).
func parseMediaRange(part string) (string, float64) {
	segs := strings.Split(part, ";")
	mt := strings.ToLower(strings.TrimSpace(segs[0]))
	q := 1.0
	for _, s := range segs[1:] {
		s = strings.TrimSpace(s)
		if v, ok := strings.CutPrefix(s, "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
	}
	return mt, q
}

func isJSONContentType(ct string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json")
}
