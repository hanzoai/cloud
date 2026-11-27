package transport

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/sdk"
)

// state is the subsystem's mounted state: the ONE transports store.
type state struct {
	store *store
	// stop ends the looking for turns nobody is showing (resume.go).
	stop context.CancelFunc
}

// mounted is the active service, read by ingest on emit goroutines and written
// once at Mount/Shutdown — an atomic.Pointer (apps/sync pattern) so a
// detached event reads it race-free. nil ⇒ unmounted; ingest drops.
var mounted atomic.Pointer[cloud.Service[state]]

// Mount wires /v1/transport/* onto app and registers the ingress consumer.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("transport: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("transport.Use:  empty DataDir")
	}
	st, err := openStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("transport.Use:  open store: %w", err)
	}
	b := cloud.NewBase(deps, "transport")
	s := &cloud.Service[state]{Base: b, State: state{store: st}}
	// Publish state BEFORE serving the ingest endpoint so the first event finds a
	// mounted service.
	mounted.Store(s)
	if err := routes(app, s); err != nil {
		return err
	}
	serveIngest()
	// The read side of the same inbox: a chat bridge answers with the conversation
	// in front of it instead of one message. Published beside the write so the two
	// halves of one record are declared together.
	serveRecent()
	serveAgent()
	exposeStop(s)
	exposeAct()
	// Carry on the turns no process is showing (resume.go). After the routes are up,
	// and off this goroutine: a mount that waited on agents to answer for every
	// inherited turn would hold the whole fleet's start on it. Taken only when their
	// leases have lapsed, so a replica starting beside others leaves theirs alone,
	// and looked for again every beat, because a replica can go at any time.
	life, stop := context.WithCancel(context.Background())
	s.State.stop = stop
	go resuming(life, s)
	b.Log.Info("transport mounted", "transports", len(transports))
	return nil
}

// Shutdown unpublishes the service, then closes the store. Idempotent. The
// context is unused; the signature matches provider.Shutdown so apps.go
// wires it directly. Unpublish-first stops new ingest events from adopting a
// store that is about to close.
func Shutdown(_ context.Context) error {
	s := mounted.Load()
	if s == nil {
		return nil
	}
	mounted.Store(nil)
	if s.State.stop != nil {
		s.State.stop()
	}
	// Give the turns this process is showing up, so another takes them at its next
	// beat and not when their leases have run out.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.State.store.leave(ctx); err != nil {
		s.Log.Warn("transport: the turns this process was showing were not given up; they are taken when their leases lapse", "err", err)
	}
	return s.State.store.Close()
}
