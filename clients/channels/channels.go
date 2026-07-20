// Package channels is the /v1/channels transport plane: the portable chat
// envelope, per-org access policy (pairing / allowlist / open), a durable
// inbox, and outbound send across the connected chat transports (Discord,
// Slack, Teams, Telegram). Identity and token custody stay in
// clients/integrations — channels consumes its ingress seam
// (integrations.RegisterIngress) and its send doors, so the dependency points
// one way: channels → integrations, never back.
package channels

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
	"github.com/zap-proto/zip"
)

// state is the subsystem's mounted state: the ONE channels store.
type state struct {
	store *store
}

// mounted is the active service, read by ingest on emit goroutines and written
// once at Mount/Shutdown — an atomic.Pointer (clients/sync pattern) so a
// detached event reads it race-free. nil ⇒ unmounted; ingest drops.
var mounted atomic.Pointer[cloud.Service[state]]

// Mount wires /v1/channels/* onto app and registers the ingress consumer.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("channels.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("channels.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("channels.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("channels.Mount: data dir: %w", err)
	}
	st, err := openStore(filepath.Join(deps.DataDir, "channels.db"))
	if err != nil {
		return fmt.Errorf("channels.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "channels")
	s := &cloud.Service[state]{Base: b, State: state{store: st}}
	// Publish state BEFORE registering the ingress consumer so the first
	// emitted event finds a mounted service.
	mounted.Store(s)
	routes(app, s)
	integrations.RegisterIngress(ingest)
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
