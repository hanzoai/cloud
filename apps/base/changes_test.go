// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/base/core"
	luxlog "github.com/luxfi/log"
)

// sink is a bus that records what reached it.
type sink struct {
	mu  sync.Mutex
	got []change
}

func (s *sink) send(_ context.Context, c change) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, c)
	return nil
}

func (s *sink) wait(t *testing.T, n int) []change {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.got) >= n {
			out := append([]change(nil), s.got...)
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("the bus got %d facts, want %d: %+v", len(s.got), n, s.got)
	return nil
}

// Every committed record write of an org's Base is a fact on the bus, as that
// org's, under one of three closed names; Base's own tables state nothing.
func TestRecordWritesReachTheBus(t *testing.T) {
	mountApp(t)
	bus := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mounted.pool.changes = newChanges(ctx, luxlog.New("test"), bus.send)

	app := seedPublicNotes(t, "acme")
	c, _ := app.FindCollectionByNameOrId("notes")

	r := core.NewRecord(c)
	r.Set("title", "one")
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	r.Set("title", "two")
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(r); err != nil {
		t.Fatal(err)
	}

	got := bus.wait(t, 3)
	want := []string{recordCreated, recordUpdated, recordDeleted}
	for i, c := range got[:3] {
		if c.org != "acme" || c.collection != "notes" || c.id != r.Id || c.name != want[i] {
			t.Errorf("fact %d: %+v, want acme/notes/%s %s", i, c, r.Id, want[i])
		}
	}
	for _, c := range got {
		if c.collection != "notes" {
			t.Errorf("a write to %q reached the bus", c.collection)
		}
	}
}

// A bus that cannot keep up costs facts, never the write.
func TestAFullBusNeverBlocksAWrite(t *testing.T) {
	block := make(chan struct{})
	c := newChanges(context.Background(), luxlog.New("test"), func(context.Context, change) error {
		<-block
		return nil
	})

	app, err := mountedApp(t, "acme")
	if err != nil {
		t.Fatal(err)
	}
	coll := core.NewBaseCollection("notes")
	coll.Fields.Add(&core.TextField{Name: "title"})
	if err := app.Save(coll); err != nil {
		t.Fatal(err)
	}
	c.bind("acme", app)

	stop, done := make(chan struct{}), make(chan struct{})
	wrote := make(chan struct{}, 1)
	go func() {
		defer close(done)
		for i := 0; i < changesQueue+10; i++ {
			select {
			case <-stop:
				return
			default:
			}
			r := core.NewRecord(coll)
			if err := app.Save(r); err != nil {
				t.Error(err)
				return
			}
			select {
			case wrote <- struct{}{}:
			default:
			}
		}
	}()
	// The writer is gone before the app is shut down beneath it, whatever the
	// verdict: a Save on a closed app dereferences its nil DB and takes the whole
	// test binary with it. Releasing the bus first frees a writer the bug held.
	t.Cleanup(func() {
		close(stop)
		close(block)
		<-done
	})
	// Blocked is no write completing, not a slow runner: CI shares its cores
	// with every other package, and 4,106 saves outran a fixed minute there.
	stall := time.NewTimer(30 * time.Second)
	defer stall.Stop()
	for finished := false; !finished; {
		select {
		case <-done:
			finished = true
		case <-wrote:
			stall.Reset(30 * time.Second)
		case <-stall.C:
			t.Fatal("a write blocked on a bus that was not reading")
		}
	}
	if c.dropped.Load() == 0 {
		t.Fatal("a full queue dropped nothing, so something waited")
	}
}

// mountedApp opens org's Base through the pool of a mounted subsystem whose
// pool states nothing to the bus, so a test binds the bus it means.
func mountedApp(t *testing.T, org string) (core.App, error) {
	t.Helper()
	mountApp(t)
	mounted.pool.changes = nil
	return mounted.pool.appFor(org)
}
