package team

// fulltext.go answers the SPA's `searchFulltext` RPC.
//
// It used to return a hardcoded empty result — the client's search box worked,
// asked, and was told "no matches" forever. The answer now comes from the ONE
// retrieval composition (clients/search), called IN-PROCESS: the transactor and
// the search surface are the same binary, so a second HTTP hop would add latency
// and a second failure mode to reach a package already linked in.
//
// TENANT. The org is s.org — the transactor token's VERIFIED extra.org, never a
// client field — so a space can only ever search its own org's knowledge.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud/apps/search"
)

// maxFulltextLimit bounds what one RPC can pull back, mirroring the surface's own
// cap so the two cannot disagree about what "too much" means.
const maxFulltextLimit = 50

// searchFulltext runs the space's query through the fused retrieval path and
// renders the result in the shape the client's reviver expects: {docs, total}.
//
// DEGRADATION IS NOT AN ERROR HERE. When a leg is down the surface still answers
// with whatever the survivors found; this returns those docs. Only a hard failure
// (no tenant, malformed params) yields an empty result — and it stays a 200-shaped
// RPC reply either way, because a search outage must not break the client's
// session.
func (s *session) searchFulltext(id int64, params []json.RawMessage) []byte {
	q, limit := parseFulltextParams(params)
	// A guest's reach is what names them, so a guest's socket gets no
	// org-wide search at all — refused before the index is asked anything.
	if q == "" || (s.client && s.roleNow() == roleGuest) {
		return s.result(id, map[string]any{"docs": []any{}, "total": 0})
	}
	res, err := search.ForOrg(context.Background(), s.org, "", &search.Request{Query: q, Limit: limit})
	if err != nil || res == nil {
		return s.result(id, map[string]any{"docs": []any{}, "total": 0})
	}
	// A person's socket is answered only with hits that ARE documents of this
	// space they may open: the index is the org's, and a hit is its title and
	// url, which is a read of the thing it names.
	var sg *sight
	if s.client {
		sg = newSight()
	}
	docs := make([]map[string]any, 0, len(res.Hits))
	for _, h := range res.Hits {
		if sg != nil {
			if d := s.doc(h.ID, sg); d == nil || !s.sees(d, sg) {
				continue
			}
		}
		docs = append(docs, map[string]any{
			"id":      h.ID,
			"_class":  h.DocType,
			"title":   h.Title,
			"doctype": h.DocType,
			"project": h.Project,
			"url":     h.URL,
			"score":   h.Score,
		})
	}
	return s.result(id, map[string]any{"docs": docs, "total": len(docs)})
}

// parseFulltextParams reads the client's (query, options) pair. The client sends
// either a bare string or a {query: "..."} object, and options carry the
// limit; anything absent or out of range falls back to a sane default rather than
// failing the RPC.
func parseFulltextParams(params []json.RawMessage) (string, int) {
	limit := 10
	if len(params) == 0 {
		return "", limit
	}
	var q string
	if err := json.Unmarshal(params[0], &q); err != nil {
		var obj struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(params[0], &obj); err == nil {
			q = obj.Query
		}
	}
	if len(params) > 1 {
		var opts struct {
			Limit *int `json:"limit"`
		}
		if err := json.Unmarshal(params[1], &opts); err == nil && opts.Limit != nil {
			if *opts.Limit > 0 && *opts.Limit <= maxFulltextLimit {
				limit = *opts.Limit
			}
		}
	}
	return strings.TrimSpace(q), limit
}
