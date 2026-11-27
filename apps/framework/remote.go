// Copyright © 2026 Hanzo AI. MIT License.

package framework

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hanzoai/cloud/client"
	frameworkpeer "github.com/hanzoai/cloud/client/framework"
	"github.com/hanzoai/cloud/sdk"
)

// remote.go — the exported reads and writes reach the framework wherever it
// runs.
//
// An org's DocType store is single-open: exactly one process holds it. When THIS
// process is that one (mounted != nil), the exported functions above call the
// engine directly. When it is not — a lane in its own plugin, split from the
// framework — they ask the framework peer by name (client/framework), which
// answers from the process that holds the store. The plane handlers (lane_rpc.go)
// call the same exported functions, and never recurse: the process that serves
// them is by definition the one that mounted the store, so its mounted is set and
// the direct branch is taken. Co-resident tests never leave the direct branch.

func remoteInstalled(ctx context.Context, org string, id ID) bool {
	out, err := frameworkpeer.FrameworkInstalled(cloud.For(ctx, org), &client.FwRef{Doctype: id.String()})
	return err == nil && out != nil && out.Installed
}

func remoteSearch(ctx context.Context, org string, id ID, filters map[string]string, limit int) ([]Document, error) {
	fs := make([]client.FwFilter, 0, len(filters))
	for k, v := range filters {
		fs = append(fs, client.FwFilter{Field: k, Value: v})
	}
	out, err := frameworkpeer.FrameworkDocs(cloud.For(ctx, org), &client.FwDocsIn{Doctype: id.String(), Filters: fs, Limit: limit})
	if err != nil {
		return nil, err
	}
	docs := make([]Document, 0, len(out.Docs))
	for _, d := range out.Docs {
		doc := Document{Name: d.Name, CreatedAt: d.Created}
		if len(d.Data) > 0 {
			_ = json.Unmarshal(d.Data, &doc.Data)
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func remoteGet(ctx context.Context, org string, id ID, name string) (Document, error) {
	out, err := frameworkpeer.FrameworkDoc(cloud.For(ctx, org), &client.FwDocIn{Doctype: id.String(), Name: name})
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
	out, err := frameworkpeer.FrameworkIngest(cloud.For(ctx, org), &client.FwIngestIn{Doctype: id.String(), Data: raw, Name: requestedName})
	if err != nil {
		return Ingested{}, err
	}
	return Ingested{Org: org, DocType: id, Name: out.Name}, nil
}

// remoteAmong carries a set as repeated pairs, which is what the plane's encoder
// holds: a field named twice names two acceptable values.
func remoteAmong(ctx context.Context, org string, id ID, among map[string][]string, limit int) ([]Document, error) {
	pairs := make([]client.FwFilter, 0, len(among))
	for field, vals := range among {
		// A field named with no acceptable value matches nothing, which the engine
		// answers for itself and repeated pairs cannot say: no pair and no question
		// look the same on the wire.
		if len(vals) == 0 {
			return []Document{}, nil
		}
		for _, v := range vals {
			pairs = append(pairs, client.FwFilter{Field: field, Value: v})
		}
	}
	out, err := frameworkpeer.FrameworkDocs(cloud.For(ctx, org), &client.FwDocsIn{Doctype: id.String(), Among: pairs, Limit: limit})
	if err != nil {
		return nil, err
	}
	docs := make([]Document, 0, len(out.Docs))
	for _, d := range out.Docs {
		doc := Document{Name: d.Name, CreatedAt: d.Created}
		if len(d.Data) > 0 {
			_ = json.Unmarshal(d.Data, &doc.Data)
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func remoteNames(ctx context.Context, org string, id ID, filters map[string]string, limit int) ([]string, error) {
	fs := make([]client.FwFilter, 0, len(filters))
	for k, v := range filters {
		fs = append(fs, client.FwFilter{Field: k, Value: v})
	}
	out, err := frameworkpeer.FrameworkDocs(cloud.For(ctx, org),
		&client.FwDocsIn{Doctype: id.String(), Filters: fs, Names: true, Limit: limit})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Docs))
	for _, d := range out.Docs {
		names = append(names, d.Name)
	}
	return names, nil
}

func remoteModule(ctx context.Context, org, module string) bool {
	out, err := frameworkpeer.FrameworkModule(cloud.For(ctx, org), &client.FwModuleIn{Module: module})
	return err == nil && out != nil && out.Installed
}

func remoteUpdate(ctx context.Context, org string, id ID, name string, data map[string]any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = frameworkpeer.FrameworkUpdate(cloud.For(ctx, org), &client.FwUpdateIn{Doctype: id.String(), Name: name, Data: raw})
	return err
}

func remoteDelete(ctx context.Context, org string, id ID, name string) error {
	_, err := frameworkpeer.FrameworkDelete(cloud.For(ctx, org), &client.FwDocIn{Doctype: id.String(), Name: name})
	return err
}

// remoteLease takes the lease in the process that holds the store and keeps the
// holder token, which is what the release is scoped to.
func remoteLease(ctx context.Context, org, key string, ttl, wait time.Duration) (Lease, bool, error) {
	out, err := frameworkpeer.FrameworkLease(cloud.For(ctx, org), &client.FwLeaseIn{
		Key: key, Hold: ttl.Milliseconds(), Wait: wait.Milliseconds()})
	if err != nil {
		return nil, false, err
	}
	if out == nil || !out.Held {
		return nil, false, nil
	}
	return held{org: org, key: key, holder: out.Holder}, true, nil
}

// held is a lease taken through the plane: the claim is a row in the framework's
// store and this is the token that owns it.
type held struct{ org, key, holder string }

func (h held) Release(ctx context.Context) error {
	_, err := frameworkpeer.FrameworkRelease(cloud.For(context.WithoutCancel(ctx), h.org),
		&client.FwReleaseIn{Key: h.key, Holder: h.holder})
	return err
}

func remoteFind(ctx context.Context, org string, id ID, field, value string) (string, error) {
	out, err := frameworkpeer.FrameworkFind(cloud.For(ctx, org), &client.FwFindIn{Doctype: id.String(), Field: field, Value: value})
	if err != nil {
		return "", err
	}
	return out.Name, nil
}
