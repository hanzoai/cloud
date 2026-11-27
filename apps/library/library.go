package library

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/zap-proto/zip"
)

// service is the Library's state.
type service struct {
	store store
	audit *audit.Recorder
	log   luxlog.Logger
	close func() error
	stop  context.CancelFunc
}

var mounted *service

// Use mounts /v1/library and publishes the plane ops.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("library: nil app")
	}
	if cloud.DataDir() == "" {
		return errors.New("library.Use: empty DataDir")
	}
	log := luxlog.Default().New("subsystem", "library")
	db, closeDB, err := locus.Tenants(context.Background(), locus.Tenancy{
		Name:    "library",
		DataDir: cloud.DataDir(),
		Tables:  tables,
		Tenant:  tenantOf,
		Audit: func(_ context.Context, why string) {
			log.Info("library: platform scope", "why", why)
		},
	})
	if err != nil {
		return fmt.Errorf("library.Use: %w", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &service{store: store{db: db}, audit: deps.Audit, log: log, close: closeDB, stop: stop}
	mounted = s
	routes(app, s)
	expose(s)
	go s.sweep(ctx)
	log.Info("library mounted", "prefix", "/v1/library")
	return nil
}

// Shutdown stops the sweep and closes the store. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	mounted.stop()
	err := mounted.close()
	mounted = nil
	return err
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, the only way that prose reaches the published document.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes is the one place the surface is wired.
func routes(app cloud.Router, s *service) {
	o := ops{s: s}
	g := app.Group("/v1/library")
	g.Get("", o.listItems)
	g.Post("", o.keepItem, zip.WithStatus(http.StatusCreated))
	g.Get("/unowned", o.listUnowned)
	g.Get("/:id", o.getItem)
	g.Patch("/:id", o.changeItem)
	g.Delete("/:id", o.deleteItem)
}

// ops is the receiver the typed ops hang off; a method value is the bound form
// zipdoc lifts prose from.
type ops struct{ s *service }

// sweepEvery and reservedFor bound how long a reservation whose bytes never came
// is kept.
const (
	sweepEvery  = 30 * time.Minute
	reservedFor = 24 * time.Hour
)

// sweep removes, every half hour, reservations older than a day whose upload
// never came, and their bytes if any arrived.
func (s *service) sweep(ctx context.Context) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rows, err := s.store.stale(ctx, time.Now().Add(-reservedFor).UnixMilli(), 500)
		if err != nil {
			s.log.Warn("library: sweep read failed", "err", err)
			continue
		}
		for _, r := range rows {
			if err := s.store.remove(scoped(ctx, r.Org), r.Owner, r.ID); err != nil && !noRows(err) {
				s.log.Warn("library: sweep remove failed", "id", r.ID, "err", err)
				continue
			}
			if err := bucket.remove(ctx, r.Object); err != nil {
				s.log.Warn("library: sweep object remove failed", "id", r.ID, "err", err)
			}
		}
	}
}
