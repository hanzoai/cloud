// Copyright © 2026 Hanzo AI. MIT License.

package framework

import (
	"context"
	"encoding/json"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// remote.go — the exported reads and the one write reach the framework wherever
// it runs.
//
// An org's DocType store is single-open: exactly one process holds it. When THIS
// process is that one (mounted != nil), the exported functions above call the
// engine directly. When it is not — a lane in its own plugin, split from the
// framework — they ask the framework peer by name (client.Framework*), which
// answers from the process that holds the store. The plane handlers (lane_rpc.go)
// call the same exported functions, and never recurse: the process that serves
// them is by definition the one that mounted the store, so its mounted is set and
// the direct branch is taken. Co-resident tests never leave the direct branch.

func remoteInstalled(ctx context.Context, org string, id ID) bool {
	out, err := cloud.Ask[plane.FwRef, plane.FwInstalled](cloud.For(ctx, org), "framework",
		plane.FrameworkInstalled, &plane.FwRef{Doctype: id.String()})
	return err == nil && out != nil && out.Installed
}

func remoteSearch(ctx context.Context, org string, id ID, filters map[string]string, limit int) ([]Document, error) {
	fs := make([]plane.FwFilter, 0, len(filters))
	for k, v := range filters {
		fs = append(fs, plane.FwFilter{Field: k, Value: v})
	}
	out, err := cloud.Ask[plane.FwDocsIn, plane.FwDocs](cloud.For(ctx, org), "framework",
		plane.FrameworkDocs, &plane.FwDocsIn{Doctype: id.String(), Filters: fs, Limit: limit})
	if err != nil {
		return nil, err
	}
	docs := make([]Document, 0, len(out.Docs))
	for _, d := range out.Docs {
		doc := Document{Name: d.Name}
		if len(d.Data) > 0 {
			_ = json.Unmarshal(d.Data, &doc.Data)
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func remoteGet(ctx context.Context, org string, id ID, name string) (Document, error) {
	out, err := cloud.Ask[plane.FwDocIn, plane.FwDoc](cloud.For(ctx, org), "framework",
		plane.FrameworkDoc, &plane.FwDocIn{Doctype: id.String(), Name: name})
	if err != nil {
		return Document{}, err
	}
	doc := Document{Name: out.Name}
	if len(out.Data) > 0 {
		_ = json.Unmarshal(out.Data, &doc.Data)
	}
	return doc, nil
}

func remoteIngest(ctx context.Context, org string, id ID, data map[string]any, requestedName string) (Ingested, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Ingested{}, err
	}
	out, err := cloud.Ask[plane.FwIngestIn, plane.FwIngested](cloud.For(ctx, org), "framework",
		plane.FrameworkIngest, &plane.FwIngestIn{Doctype: id.String(), Data: raw, Name: requestedName})
	if err != nil {
		return Ingested{}, err
	}
	return Ingested{Org: org, DocType: id, Name: out.Name}, nil
}

func remoteFind(ctx context.Context, org string, id ID, field, value string) (string, error) {
	out, err := cloud.Ask[plane.FwFindIn, plane.FwFound](cloud.For(ctx, org), "framework",
		plane.FrameworkFind, &plane.FwFindIn{Doctype: id.String(), Field: field, Value: value})
	if err != nil {
		return "", err
	}
	return out.Name, nil
}
