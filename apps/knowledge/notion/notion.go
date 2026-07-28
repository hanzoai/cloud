// Package notion is the pure record-shaping logic for the Notion long-tail connector:
// how to turn the raw JSON a Notion search returns (via the activepieces piece run
// through the auto engine) into normalized {title, body, external_id, url, timestamp}
// documents for KB ingestion. It has NO cloud dependency, so the Notion parsing —
// where the real per-provider complexity lives — is unit-testable in isolation.
// clients/kb/sync_piece.go wires this into the framework.Ingest path.
package notion

import (
	"strings"
	"time"
)

// Doc is a normalized Notion record ready to file as a kb-source. The KB sync maps it
// onto the framework document fields (title/body/external_id/url/source_ts).
type Doc struct {
	Title      string
	Body       string
	ExternalID string
	URL        string
	Timestamp  string // "YYYY-MM-DD HH:MM:SS" or ""
}

// Results extracts the item list from a custom_api_call output. The piece returns
// { status, headers, body }; a Notion search puts items under body.results.
func Results(output any) []map[string]any {
	m, _ := output.(map[string]any)
	body, _ := m["body"].(map[string]any)
	raw, _ := body["results"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if rm, ok := r.(map[string]any); ok {
			out = append(out, rm)
		}
	}
	return out
}

// Normalize maps ONE Notion page/database record to a Doc.
func Normalize(rec map[string]any) Doc {
	id := s(rec["id"])
	return Doc{
		Title:      Title(rec),
		Body:       Body(rec),
		ExternalID: "notion:" + s(rec["object"]) + ":" + id,
		URL:        s(rec["url"]),
		Timestamp:  Time(s(rec["last_edited_time"])),
	}
}

// Title extracts a human title from a Notion page/database record. A database stores
// its title at the top-level `title` array; a page under the title-typed property.
func Title(rec map[string]any) string {
	if t := richTextPlain(rec["title"]); t != "" {
		return t
	}
	props, _ := rec["properties"].(map[string]any)
	for _, v := range props {
		pm, _ := v.(map[string]any)
		if pm == nil {
			continue
		}
		if pm["type"] == "title" {
			if t := richTextPlain(pm["title"]); t != "" {
				return t
			}
		}
	}
	return ""
}

// Body renders a short body from the record's plain-text property values, so the
// embedding has prose beyond the title. Full block content is a follow-up
// getPageOrBlockChildren pull; this keeps the first ingest bounded and honest.
func Body(rec map[string]any) string {
	var b strings.Builder
	props, _ := rec["properties"].(map[string]any)
	// Sort-free but deterministic enough: property order isn't guaranteed by Notion,
	// but the embedding is over the concatenated text, so ordering is immaterial.
	for k, v := range props {
		pm, _ := v.(map[string]any)
		if pm == nil {
			continue
		}
		switch pm["type"] {
		case "rich_text":
			if t := richTextPlain(pm["rich_text"]); t != "" {
				b.WriteString(k + ": " + t + "\n")
			}
		case "select":
			if sel, ok := pm["select"].(map[string]any); ok {
				if name := s(sel["name"]); name != "" {
					b.WriteString(k + ": " + name + "\n")
				}
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// Time converts a Notion ISO timestamp to the store's "YYYY-MM-DD HH:MM:SS".
func Time(iso string) string {
	if iso == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, iso); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	return ""
}

// richTextPlain flattens a Notion rich-text array ([]{plain_text}) to a string.
func richTextPlain(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range arr {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if pt := s(m["plain_text"]); pt != "" {
			b.WriteString(pt)
		}
	}
	return b.String()
}

// s is a nil-safe string coercion (local copy so this package stays cloud-free).
func s(v any) string {
	if str, ok := v.(string); ok {
		return str
	}
	return ""
}
