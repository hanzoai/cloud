package knowledge

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/apps/framework"
	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/sdk/plane"
)

// lexical.go — the lexical leg of knowledge, in the ONE index store
// (apps/index) beside the vector leg in index.go. Each knowledge document is one
// row in the org's "kb" index: its identity, what /v1/search shows, the text it
// is scored on, and — as the row's user — its owner, which is how the index
// bounds a person's rows to that person (index.Query's users). The index answers in the process that
// mounted it and over the plane everywhere else; a deployment without the index
// app has no lexical leg, which is plane.ErrNoPeer and not a failure.

const (
	lexicalIndex = "kb"
	lexicalKey   = "key"
)

// lexicalRow is a knowledge document as the lexical index stores it. The key
// is doctype/name — the identity both fusions (/v1/knowledge/search and
// /v1/search) join on, and the same pair every passage row carries — so one
// document found by both legs is one result.
func lexicalRow(dt framework.ID, name string, data map[string]any) map[string]any {
	title := str(data["title"])
	row := map[string]any{
		lexicalKey: dt.String() + "/" + name,
		"doctype":  dt.String(),
		"name":     name,
		"title":    title,
		"text":     docText(dt, title, data),
		"user":     str(data["owner"]),
	}
	for _, k := range []string{"project", "provider", "url"} {
		if v := str(data[k]); v != "" {
			row[k] = v
		}
	}
	return row
}

// lexicalPut writes one document's row; lexicalRemove takes it out. Neither
// fails for a deployment without the index app.
func lexicalPut(ctx context.Context, org string, dt framework.ID, name string, data map[string]any) error {
	return lexicalErr(lexical.Put(ctx, org, lexicalIndex, lexicalKey, lexicalRow(dt, name, data)))
}

func lexicalRemove(ctx context.Context, org string, dt framework.ID, name string) error {
	return lexicalErr(lexical.Remove(ctx, org, lexicalIndex, dt.String()+"/"+name))
}

func lexicalErr(err error) error {
	if noLexical(err) {
		return nil
	}
	return err
}

// noLexical reports the one error that is not one: no index app in this
// deployment.
func noLexical(err error) bool { return errors.Is(err, plane.ErrNoPeer) }
