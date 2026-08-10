// Package channels is one inbox for the chat apps you connect — Discord, Slack,
// Teams, Telegram.
//
// The /v1/channels routes carry a portable chat envelope, per-org access policy
// (pairing / allowlist / open), a durable inbox, and outbound send across every
// connected transport. Identity and token custody stay in apps/integrations:
// inbound events arrive on the plane (plane.ChannelsIngest) and replies leave
// through that package's send doors, so the dependency points one way —
// channels → integrations, never back.
package channels

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/hanzoai/cloud"
)

// state is the subsystem's mounted state: the ONE channels store.
type state struct {
	store *store
}

// mounted is the active service, read by ingest on emit goroutines and written
// once at Mount/Shutdown — an atomic.Pointer (apps/sync pattern) so a
// detached event reads it race-free. nil ⇒ unmounted; ingest drops.
var mounted atomic.Pointer[cloud.Service[state]]

// Mount wires /v1/channels/* onto app and registers the ingress consumer.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("channels.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("channels.Mount: empty DataDir")
	}
	st, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("channels.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "channels")
	s := &cloud.Service[state]{Base: b, State: state{store: st}}
	// Publish state BEFORE serving the ingest door so the first event finds a
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
	b.Log.Info("channels mounted", "transports", len(transports))
	return nil
}

// Shutdown unpublishes the service, then closes the store. Idempotent. The
// context is unused; the signature matches integrations.Shutdown so apps.go
// wires it directly. Unpublish-first stops new ingest events from adopting a
// store that is about to close.
func Shutdown(_ context.Context) error {
	s := mounted.Load()
	if s == nil {
		return nil
	}
	mounted.Store(nil)
	return s.State.store.Close()
}
