package kv

import (
	"context"
	"net/http"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/driver/pubsub/stream"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go — the ONLY way this prose reaches the published document and
// the MCP tool list (Go drops comments at compile time). Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so
// it arrives as a RECEIVER and every op is a method value (o.get), the only
// bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// state is empty on purpose: this app owns no store of its own. The store is
// the plane, reached through bus.Bus.
type state struct{}

// Mount registers the surface. There is nothing to start.
func Use(app cloud.Router, deps cloud.Deps) error {
	// A typed op is a route PLUS a registry entry, and the registry lives on
	// the App. A router that cannot reach it would serve every route with no
	// schema, no prose, no MCP tool and no SDK method — so the mount FAILS
	// rather than quietly publishing a surface no projection knows about.
	routes(app, &cloud.Service[state]{Base: cloud.NewBase(deps, "kv")})
	return nil
}

// routes registers the tenant endpoints. Registration order is match order, and
// the three patterns nest strictly (bucket, key, history), which the router
// resolves by specificity — so nothing can shadow anything.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	// A typed op receives only a context, so the validated org reaches it by
	// being parked there — never as an In field. cloud.Bridge parks it, and the
	// composer owns that install: the fused host at its root, a plugin program
	// in its constructor.

	g := app.Group("/v1/kv")
	g.Post("/:bucket", o.createBucket, zip.WithStatus(http.StatusCreated))
	g.Delete("/:bucket", o.deleteBucket)
	g.Get("/:bucket/:key", o.get)
	g.Put("/:bucket/:key", o.put)
	g.Delete("/:bucket/:key", o.del)
	g.Get("/:bucket/:key/history", o.history)
}

// ----- inputs ---------------------------------------------------------------

// bucketWrite creates a KV bucket.
type bucketWrite struct {
	// Bucket is the bucket's name within the org, from the path: 1–64 of
	// [A-Za-z0-9_], no dash.
	Bucket string `json:"bucket"`
	// History is how many revisions each key keeps, 1–64. 0 means 1.
	History int `json:"history"`
	// TTL expires entries after this many SECONDS. 0 means no expiry.
	TTL int64 `json:"ttl"`
	// MaxValue caps one value's size in bytes. 0 or less means the server's
	// ceiling.
	MaxValue int `json:"maxValue"`
}

// bucketRef addresses ONE bucket of the org by name, from the path.
type bucketRef struct {
	// Bucket is the bucket's name, from the path.
	Bucket string `json:"bucket"`
}

// keyRef addresses ONE key of one org bucket.
type keyRef struct {
	// Bucket is the bucket, from the path.
	Bucket string `json:"bucket"`
	// Key is the key, from the path.
	Key string `json:"key"`
}

// kvWrite sets one key to one value.
type kvWrite struct {
	// Bucket is the bucket, from the path.
	Bucket string `json:"bucket"`
	// Key is the key, from the path.
	Key string `json:"key"`
	// Value is the value, carried verbatim as UTF-8 text (typically JSON).
	Value string `json:"value"`
}

// ----- outputs --------------------------------------------------------------

// bucketRecord is a KV bucket as the API publishes it.
type bucketRecord struct {
	// Bucket is the bucket's name within the org.
	Bucket string `json:"bucket"`
	// History is how many revisions each key keeps.
	History int `json:"history"`
	// TTL is the entry expiry in seconds; 0 means none.
	TTL int64 `json:"ttl"`
	// Values is how many values the bucket holds right now.
	Values uint64 `json:"values"`
}

// kvEntry is one key's value at one revision.
type kvEntry struct {
	// Key is the entry's key.
	Key string `json:"key"`
	// Value is the value as UTF-8 text; empty for delete and purge markers.
	Value string `json:"value"`
	// Revision is the entry's revision in the bucket.
	Revision uint64 `json:"revision"`
	// Created is when this revision was written, RFC3339.
	Created string `json:"created"`
	// Operation is what wrote the revision: put, del or purge.
	Operation string `json:"operation"`
}

// kvAck is the bucket's receipt for one write.
type kvAck struct {
	// Revision is the revision the write created.
	Revision uint64 `json:"revision"`
}

// kvPage is one key's history, oldest revision first.
type kvPage struct {
	// Data are the key's retained revisions.
	Data []kvEntry `json:"data"`
}

// ----- ops ------------------------------------------------------------------

// CreateBucket creates a KV bucket and returns it. A bucket is keyed state on
// the same durable plane as the streams: each key holds up to History
// revisions, entries can expire by TTL, and watchers on the NATS port see every
// write. 409 when the org already has a bucket of that name.
//
// Example: {"history": 5, "ttl": 3600}
func (o ops) createBucket(ctx context.Context, in *bucketWrite) (*bucketRecord, error) {
	org, err := bus.Org(ctx)
	if err != nil {
		return nil, err
	}
	name, ok := bus.Qualify(org, in.Bucket)
	if !ok {
		return nil, zip.ErrBadRequest("bucket must be 1-64 of letters, digits or _")
	}
	history := in.History
	if history <= 0 {
		history = 1
	}
	if history > 64 {
		return nil, zip.ErrBadRequest("history is capped at 64")
	}
	js, _, err := stream.Bus()
	if err != nil {
		return nil, err
	}
	// -1 is the plane's own ceiling; a caller ceiling is taken as given up to
	// the 1 GiB the wire could carry at all.
	maxValue := int32(-1)
	if in.MaxValue > 0 {
		maxValue = int32(min(in.MaxValue, 1<<30))
	}
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:       name,
		History:      uint8(history),
		TTL:          time.Duration(max(in.TTL, 0)) * time.Second,
		MaxValueSize: maxValue,
	})
	if err != nil {
		return nil, bus.Err(err)
	}
	return bucket(ctx, in.Bucket, kv)
}

// bucket publishes a bucket in the org's own view, from its live status.
func bucket(ctx context.Context, name string, kv jetstream.KeyValue) (*bucketRecord, error) {
	status, err := kv.Status(ctx)
	if err != nil {
		return nil, bus.Err(err)
	}
	return &bucketRecord{
		Bucket:  name,
		History: int(status.History()),
		TTL:     int64(status.TTL() / time.Second),
		Values:  status.Values(),
	}, nil
}

// bucketOf resolves ONE org bucket by its caller-visible name. A name that
// cannot BE a bucket answers 404 rather than 400: telling a caller its name was
// malformed and telling it the bucket is missing are the same fact here, and
// only one of the two says nothing about another org.
func bucketOf(ctx context.Context, org, name string) (jetstream.KeyValue, error) {
	q, ok := bus.Qualify(org, name)
	if !ok {
		return nil, zip.ErrNotFound("bucket not found")
	}
	js, _, err := stream.Bus()
	if err != nil {
		return nil, err
	}
	kv, err := js.KeyValue(ctx, q)
	if err != nil {
		return nil, bus.Err(err)
	}
	return kv, nil
}

// DeleteBucket removes one bucket of the caller's org — every key and every
// revision with it — and answers 204 with no body. 404 when the org has no
// bucket of that name.
func (o ops) deleteBucket(ctx context.Context, in *bucketRef) (*cloud.Unit, error) {
	org, err := bus.Org(ctx)
	if err != nil {
		return nil, err
	}
	name, ok := bus.Qualify(org, in.Bucket)
	if !ok {
		return nil, zip.ErrNotFound("bucket not found")
	}
	js, _, err := stream.Bus()
	if err != nil {
		return nil, err
	}
	if err := js.DeleteKeyValue(ctx, name); err != nil {
		return nil, bus.Err(err)
	}
	return nil, nil
}

// Get returns one key's current value and revision. 404 when the bucket does
// not exist, the key was never written, or its latest revision is a delete.
func (o ops) get(ctx context.Context, in *keyRef) (*kvEntry, error) {
	org, err := bus.Org(ctx)
	if err != nil {
		return nil, err
	}
	kv, err := bucketOf(ctx, org, in.Bucket)
	if err != nil {
		return nil, err
	}
	e, err := kv.Get(ctx, in.Key)
	if err != nil {
		return nil, bus.Err(err)
	}
	out := entry(e)
	return &out, nil
}

// entry publishes one KV entry.
func entry(e jetstream.KeyValueEntry) kvEntry {
	op := "put"
	switch e.Operation() {
	case jetstream.KeyValueDelete:
		op = "del"
	case jetstream.KeyValuePurge:
		op = "purge"
	}
	return kvEntry{
		Key:       e.Key(),
		Value:     string(e.Value()),
		Revision:  e.Revision(),
		Created:   e.Created().UTC().Format(time.RFC3339),
		Operation: op,
	}
}

// Put sets one key to one value and returns the revision the write created.
// Writes are versioned: each put is a new revision and the bucket retains up to
// its History of them per key.
//
// Example: {"value": "{\"theme\":\"dark\"}"}
func (o ops) put(ctx context.Context, in *kvWrite) (*kvAck, error) {
	org, err := bus.Org(ctx)
	if err != nil {
		return nil, err
	}
	kv, err := bucketOf(ctx, org, in.Bucket)
	if err != nil {
		return nil, err
	}
	rev, err := kv.Put(ctx, in.Key, []byte(in.Value))
	if err != nil {
		return nil, bus.Err(err)
	}
	return &kvAck{Revision: rev}, nil
}

// Delete removes one key — a delete marker in the key's history, so watchers
// see it and Get answers 404 — and answers 204 with no body. 404 when the
// bucket does not exist.
func (o ops) del(ctx context.Context, in *keyRef) (*cloud.Unit, error) {
	org, err := bus.Org(ctx)
	if err != nil {
		return nil, err
	}
	kv, err := bucketOf(ctx, org, in.Bucket)
	if err != nil {
		return nil, err
	}
	if err := kv.Delete(ctx, in.Key); err != nil {
		return nil, bus.Err(err)
	}
	return nil, nil
}

// History returns one key's retained revisions, oldest first — every put and
// every delete marker up to the bucket's History depth. 404 when the bucket
// does not exist or the key was never written.
func (o ops) history(ctx context.Context, in *keyRef) (*kvPage, error) {
	org, err := bus.Org(ctx)
	if err != nil {
		return nil, err
	}
	kv, err := bucketOf(ctx, org, in.Bucket)
	if err != nil {
		return nil, err
	}
	entries, err := kv.History(ctx, in.Key)
	if err != nil {
		return nil, bus.Err(err)
	}
	page := make([]kvEntry, 0, len(entries))
	for _, e := range entries {
		page = append(page, entry(e))
	}
	return &kvPage{Data: page}, nil
}
