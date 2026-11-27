package reward

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	rewardmod "github.com/hanzoai/reward"

	"github.com/hanzoai/cloud/sdk"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Intervals of the feeders' loops.
const (
	feedEvery = 15 * time.Minute
	postEvery = time.Hour
)

// mounted is what Shutdown stops.
var mounted struct {
	sync.Mutex
	stop  func()
	state *state
}

// Use mounts the module on app, opens its store, registers this app's own
// operations and starts the feeders.
func Use(app cloud.Router, deps cloud.Deps) error {
	b := cloud.NewBase(deps, "reward")
	db, closeDB, err := openStore(b.DataDir)
	if err != nil {
		return fmt.Errorf("reward: open the store: %w", err)
	}
	eng, err := rewardmod.Mount(app, rewardmod.Config{DB: db, Log: slog.Default().With("subsystem", "reward")})
	if err != nil {
		_ = closeDB()
		return err
	}
	s := &state{eng: eng, db: db, base: b}
	if err := ready(db); err != nil {
		_ = closeDB()
		return err
	}
	s.routes(app)
	stop := s.start()
	mounted.Lock()
	mounted.stop, mounted.state = func() { stop(); _ = closeDB() }, s
	mounted.Unlock()
	b.Log.Info("reward mounted", "prefix", "/v1/reward", "admin", "/v1/admin/reward")
	return nil
}

// Shutdown stops the feeders and closes the store. Idempotent.
func Shutdown(context.Context) error {
	mounted.Lock()
	defer mounted.Unlock()
	if mounted.stop != nil {
		mounted.stop()
		mounted.stop = nil
	}
	return nil
}

// start runs the loops under the deployment's leases, so each runs on one replica
// at a time, and returns the func that stops them and waits for a pass in flight.
func (s *state) start() func() {
	done := make(chan struct{})
	var wg sync.WaitGroup
	loop := func(name string, every time.Duration, passes ...func(context.Context) error) {
		lead := s.base.Coord.Lead("reward:" + name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					_ = lead.Do(context.Background(), func(ctx context.Context) {
						for _, pass := range passes {
							if err := pass(ctx); err != nil {
								s.base.Log.Warn("reward: a feeder pass failed; it is tried again", "loop", name, "err", err)
							}
						}
					})
				}
			}
		}()
	}
	loop("usage", feedEvery, s.sweepUsage, s.sweepModels, s.sweepReferrals)
	loop("milestones", postEvery, s.daily("tenure", s.sweepTenure), s.daily("streak", s.sweepStreaks), s.daily("founders", s.sweepFounders))
	loop("posts", postEvery, func(ctx context.Context) error { s.sweepPosts(ctx); return nil })
	return func() { close(done); wg.Wait() }
}
