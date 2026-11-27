// Copyright © 2026 Hanzo AI. MIT License.

package planetest

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// Settings is the settings app's half of the plane: the platform's own
// configuration of each product, read by settings_fleet and replaced by
// settings_fleet_set, which only a SuperAdmin caller may do — as the real op asks.
// A product never set answers an empty document, which every reader takes as
// "use the default".
type Settings struct {
	mu   sync.Mutex
	docs map[string]string
}

// ServeSettings stands the settings peer up on this test's runtime directory.
func ServeSettings(t *testing.T) *Settings {
	t.Helper()
	runtimeDir(t)
	s := &Settings{docs: map[string]string{}}
	app := zip.New(zip.Config{AppName: "settings"})
	app.Post("/settings/fleet", func(_ context.Context, in *client.Product) (*client.Configured, error) {
		return &client.Configured{Config: s.Doc(in.Product)}, nil
	}, zip.WithOperationID(client.SettingsFleet))
	app.Post("/settings/fleet/set", func(ctx context.Context, in *client.FleetConfig) (*client.Configured, error) {
		if !zip.CallerOf(ctx).Admin {
			return nil, zip.ErrForbidden("settings: SuperAdmin required")
		}
		s.Set(in.Product, in.Config)
		return &client.Configured{Config: in.Config}, nil
	}, zip.WithOperationID(client.SettingsFleetSet))
	listen(t, app, "settings")
	return s
}

// Set stores doc as the platform's configuration of product; "" is nothing set.
func (s *Settings) Set(product, doc string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[product] = doc
}

// Doc is the platform's configuration of product as stored.
func (s *Settings) Doc(product string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.docs[product]
}
