package knowledge

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/hanzoai/cloud/apps/framework"
	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/apps/search/rank"
	luxlog "github.com/luxfi/log"
)

// retrieve.go answers /v1/knowledge/search from BOTH of knowledge's legs: the
// passages in the org's own store (index.go) and the org's "kb" lexical index
// (lexical.go), fused by rank.RRF — the one fusion in the codebase — on the
// identity the two legs share, doctype/name. A document both legs found outranks
// one either found alone; a document only one leg found still answers.
//
// /v1/search does NOT come through here. It fuses the vector leg (Semantic) with
// the lexical index and the code corpus itself, so routing it through this fusion
// would count the lexical leg twice.

// Leg names, as each leg is recorded in a fused row's origins.
const (
	legVector  = "vector"
	legLexical = "lexical"
)

// lexicalWiden is how much further the lexical leg reads when the request is
// scoped by project or doctype. The index bounds rows by owner and by nothing
// else, so the scope is applied to what it returns; reading five windows keeps a
// scoped query from coming back short on lexical matches without reading the
// whole corpus. The vector leg needs no widening — its scope is in the query.
const lexicalWiden = 5

// retrieve runs both legs, fuses them, and answers each fused document from the
// store (present), cut to the request's window. degraded is true when a leg that
// is here FAILED — the query could not be embedded, or a store could not be read
// — and hits are then what the other leg found. A leg that is not here (no vector
// leg in this process, no index app in this deployment) is not a failure.
func retrieve(ctx context.Context, log luxlog.Logger, req searchReq) (hits []hit, degraded bool) {
	found := map[string]hit{}
	var lists []rank.List[string]

	if idx.enabled() {
		vh, err := idx.searchDoc(ctx, req)
		if err != nil {
			degraded = true
			log.Warn("kb search: vector leg failed", "org", req.org, "err", err)
		} else {
			l := rank.List[string]{Source: legVector}
			for _, h := range vh {
				key := h.DocType + "/" + h.Name
				l.Keys = append(l.Keys, key)
				l.Scores = append(l.Scores, h.Score)
				found[key] = h
			}
			lists = append(lists, l)
		}
	}

	rows, err := lexicalQuery(ctx, req)
	switch {
	case err == nil:
		l := rank.List[string]{Source: legLexical}
		for _, h := range rows {
			key := h.DocType + "/" + h.Name
			l.Keys = append(l.Keys, key)
			l.Scores = append(l.Scores, 0) // the index exposes no per-row score
			if _, seen := found[key]; !seen {
				found[key] = h
			}
		}
		lists = append(lists, l)
	case noLexical(err):
	default:
		degraded = true
		log.Warn("kb search: lexical leg failed", "org", req.org, "err", err)
	}

	// Every candidate is fused and presented, and the window is cut after, so a
	// candidate present drops does not leave the answer short.
	fused := rank.RRF(lists, 0)
	cands := make([]hit, 0, len(fused))
	for _, f := range fused {
		cands = append(cands, found[f.Key])
	}
	hits, err = present(ctx, req, cands)
	if err != nil {
		log.Warn("kb search: documents unreadable", "org", req.org, "err", err)
		return []hit{}, true
	}
	if len(hits) > req.window() {
		hits = hits[:req.window()]
	}
	return hits, degraded
}

// present answers each candidate from the document it names, as the framework
// store holds it now. Both legs are indexes of that store and a row in either is
// a pointer, never an answer: the lexical index takes writes from any member
// through its public dialect, and a delete can outrun either leg. So a candidate
// is dropped when the store holds no such document, or when the document is out
// of the asker's reach — owned by someone else, or outside the request's project
// or doctypes — and a kept one carries the document's own title, project,
// provider and url, and its passage of the document's text. Order is kept.
func present(ctx context.Context, req searchReq, cands []hit) ([]hit, error) {
	names := map[framework.ID][]string{}
	for _, h := range cands {
		id, err := framework.ParseID(h.DocType)
		if err != nil || !slices.Contains(indexedDocTypes, id) {
			continue
		}
		names[id] = append(names[id], h.Name)
	}
	docs := map[string]framework.Document{}
	for id, ns := range names {
		got, err := current(ctx, req.org, id, ns)
		if err != nil {
			return nil, err
		}
		for name, d := range got {
			docs[id.String()+"/"+name] = d
		}
	}
	out := make([]hit, 0, len(cands))
	for _, h := range cands {
		d, ok := docs[h.DocType+"/"+h.Name]
		if !ok {
			continue
		}
		owner, project := str(d.Data["owner"]), str(d.Data["project"])
		if owner != "" && owner != req.subject ||
			req.project != "" && project != req.project ||
			len(req.doctypes) > 0 && !slices.Contains(req.doctypes, h.DocType) {
			continue
		}
		id, _ := framework.ParseID(h.DocType)
		h.Title, h.Project = str(d.Data["title"]), project
		h.Provider, h.URL = str(d.Data["provider"]), str(d.Data["url"])
		h.Text = h.passage(docText(id, h.Title, d.Data))
		out = append(out, h)
	}
	return out, nil
}

// lexicalQuery reads the org's "kb" index for the documents it points at: the
// org's rows and, for a person, their own (the index's users bound). What a row
// claims beyond its doctype and name is not read — present answers from the
// document.
func lexicalQuery(ctx context.Context, req searchReq) ([]hit, error) {
	users := []string{""}
	if req.subject != "" {
		users = append(users, req.subject)
	}
	n := req.window()
	if req.project != "" || len(req.doctypes) > 0 {
		n *= lexicalWiden
	}
	raws, err := lexical.Query(ctx, req.org, lexicalIndex, req.query, users, n, 0)
	if err != nil {
		return nil, err
	}
	out := make([]hit, 0, len(raws))
	for _, raw := range raws {
		var row struct {
			DocType string `json:"doctype"`
			Name    string `json:"name"`
		}
		if json.Unmarshal(raw, &row) != nil || row.Name == "" {
			continue
		}
		out = append(out, hit{DocType: row.DocType, Name: row.Name})
	}
	return out, nil
}
