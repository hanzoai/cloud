// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

// changes.go — every record an org's Base writes is a fact on the bus.
//
// A webhook, an automation or the CRM's timeline learns that a person, a deal or
// a task changed by reading the event plane, never by reaching into a Base. So
// each pooled Base states its record writes there, after they commit, as the
// org's own occurrence: record_created, record_updated or record_deleted, with
// the collection and the record's id as attributes. The name is from this
// closed set and the collection rides an attribute, because the event plane
// groups by name and an org's collections are the org's to name.
//
// The write never waits for the bus. Facts queue in memory and one worker sends
// them; when the queue is full a fact is dropped and counted, never blocking the
// write that made it. The tables Base keeps for itself (a leading underscore)
// state nothing.

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/event"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/base/core"
	luxlog "github.com/luxfi/log"
)

// The names a record write is stated under.
const (
	recordCreated = "record_created"
	recordUpdated = "record_updated"
	recordDeleted = "record_deleted"
)

// changesQueue is how many facts wait for the worker before one is dropped.
const changesQueue = 4096

// change is one committed record write.
type change struct {
	org, name, collection, id string
	at                        time.Time
}

// changes queues record writes and sends them to the event plane.
type changes struct {
	q       chan change
	send    func(ctx context.Context, c change) error
	log     luxlog.Logger
	dropped atomic.Int64
}

// newChanges starts the worker that sends facts with send until ctx ends.
func newChanges(ctx context.Context, log luxlog.Logger, send func(ctx context.Context, c change) error) *changes {
	c := &changes{q: make(chan change, changesQueue), send: send, log: log}
	go c.run(ctx)
	return c
}

// capture is the send the deployment uses: the org's own occurrence, on the
// event plane, as the org.
func capture(ctx context.Context, c change) error {
	_, err := event.EventCapture(plane.For(ctx, c.org), &client.EventIn{
		Name:    c.name,
		Product: "base",
		Subject: c.id,
		At:      c.at.UTC().Format(time.RFC3339Nano),
		Attributes: []client.Signal{
			{Name: "collection", Value: c.collection},
			{Name: "record", Value: c.id},
		},
	})
	return err
}

// bind states app's record writes as org's. It is called once per opened Base.
func (c *changes) bind(org string, app core.App) {
	put := func(name string) func(e *core.RecordEvent) error {
		return func(e *core.RecordEvent) error {
			c.put(org, name, e.Record)
			return e.Next()
		}
	}
	app.OnRecordAfterCreateSuccess().BindFunc(put(recordCreated))
	app.OnRecordAfterUpdateSuccess().BindFunc(put(recordUpdated))
	app.OnRecordAfterDeleteSuccess().BindFunc(put(recordDeleted))
}

func (c *changes) put(org, name string, r *core.Record) {
	coll := r.Collection().Name
	if strings.HasPrefix(coll, "_") {
		return
	}
	select {
	case c.q <- change{org: org, name: name, collection: coll, id: r.Id, at: time.Now()}:
	default:
		if n := c.dropped.Add(1); n == 1 || n%1000 == 0 {
			c.log.Warn("base: the bus queue is full; record facts dropped", "dropped", n)
		}
	}
}

func (c *changes) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ch := <-c.q:
			sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := c.send(sendCtx, ch); err != nil {
				c.log.Warn("base: a record fact did not reach the bus", "org", ch.org, "collection", ch.collection, "event", ch.name, "err", err)
			}
			cancel()
		}
	}
}
