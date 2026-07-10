// The polling daemon. One JITDaemon owns:
//   - the config
//   - one TokenProvider
//   - one ghClient
//   - one runnerLauncher
//
// On every tick: fan out across orgs (bounded by Parallelism), list
// repos, list queued jobs per repo, for each match (label subset) mint
// a JIT config and spawn a runner. Each spawned runner runs in its own
// goroutine; a per-job dedup map prevents double-spawning while another
// runner picks it up. A global MaxConcurrentRunners semaphore (when set)
// caps how many runner subprocesses run at once across all orgs.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"
)

type JITDaemon struct {
	cfg    *Config
	tp     *TokenProvider
	gh     *ghClient
	rl     *runnerLauncher
	logger *slog.Logger
	state  *daemonState

	// runSem bounds concurrent runner subprocesses when
	// cfg.MaxConcurrentRunners > 0. Nil means unlimited — Parallelism
	// still bounds the org-scan fan-out, but spawn is uncapped (legacy
	// behavior).
	runSem *semaphore.Weighted

	inFlight  atomic.Int64
	dedupMu   sync.Mutex
	dedupSeen map[int64]time.Time // job_id -> last-seen
}

func NewJITDaemon(cfg *Config, logger *slog.Logger) (*JITDaemon, error) {
	tp, err := NewTokenProvider(cfg)
	if err != nil {
		return nil, err
	}
	gh := newGHClient(tp, cfg.GitHubAPIBase, cfg.RepoListTTL)
	state := newDaemonState()
	var runSem *semaphore.Weighted
	if cfg.MaxConcurrentRunners > 0 {
		runSem = semaphore.NewWeighted(int64(cfg.MaxConcurrentRunners))
	}
	return &JITDaemon{
		cfg:       cfg,
		tp:        tp,
		gh:        gh,
		rl:        newRunnerLauncher(cfg, gh, logger, state),
		logger:    logger,
		state:     state,
		runSem:    runSem,
		dedupSeen: make(map[int64]time.Time),
	}, nil
}

func (d *JITDaemon) Run(ctx context.Context) error {
	d.logger.Info("jit.start",
		"host", d.cfg.HostName,
		"orgs", len(d.cfg.Orgs),
		"labels", d.cfg.Labels,
		"poll_interval", d.cfg.PollInterval.String(),
		"max_concurrent_runners", d.cfg.MaxConcurrentRunners,
		"wsl", IsWSL(),
	)
	if IsWSL() {
		// Warn once at start so operators tailing the journal know the
		// daemon is in WSL2 mode and what label discipline that implies.
		d.logger.Warn("jit.wsl_detected",
			"reason", "running inside WSL2 — Linux runner namespace, NOT Windows",
			"effect", "jobs must include `wsl` label; native Windows jobs land on the Windows arcd daemon")
	}

	if d.cfg.HealthAddr != "" {
		go d.serveHealth(ctx)
	}

	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	d.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("jit.shutdown", "reason", ctx.Err().Error())
			return nil
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *JITDaemon) tick(ctx context.Context) {
	t0 := time.Now()
	d.gcDedup()

	sem := semaphore.NewWeighted(int64(d.cfg.Parallelism))
	var wg sync.WaitGroup
	for _, org := range d.cfg.Orgs {
		org := org
		if err := sem.Acquire(ctx, 1); err != nil {
			return
		}
		wg.Add(1)
		go func() {
			defer sem.Release(1)
			defer wg.Done()
			if err := d.scanOrg(ctx, org); err != nil {
				d.logger.Warn("jit.scan_org_error", "org", org, "err", err.Error())
			}
		}()
	}
	wg.Wait()
	d.logger.Debug("jit.tick_done", "duration_ms", time.Since(t0).Milliseconds())
}

func (d *JITDaemon) scanOrg(ctx context.Context, org string) error {
	// Pause is a soft drain — in-flight runners keep going, we just
	// stop picking up new work. Cheap top-of-loop check so we still
	// list repos/jobs (useful for /v1/status visibility) but never spawn.
	if d.state.IsPaused() {
		return nil
	}
	repos, err := d.gh.ListRepos(ctx, org)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if repo.Archived || repo.Disabled {
			continue
		}
		// Skip fork repositories the org owns — defense-in-depth, not a complete
		// fix. This does NOT by itself stop the classic external-fork-PR RCE: a
		// PR's run is enumerated under the BASE repo (repo.Fork==false), so this
		// guard never sees it. That vector is contained by GitHub's fork-PR
		// approval gate (the job stays non-`queued` until a maintainer approves,
		// and the daemon only claims `status=queued`) PLUS the operational rule:
		// point this daemon at PRIVATE/INTERNAL orgs only, never public. Any job
		// that does run executes as this OS user and can read the on-disk App
		// key/PAT — inherent to self-hosted runners. allow_forks: true opts back in.
		if repo.Fork && !d.cfg.AllowForks {
			continue
		}
		jobs, err := d.gh.ListQueuedJobs(ctx, repo)
		if err != nil {
			d.logger.Debug("jit.list_jobs_skip", "org", org, "repo", repo.Name, "err", err.Error())
			continue
		}
		for _, job := range jobs {
			if !d.cfg.HasLabels(job.Labels) {
				continue
			}
			if !d.markSeen(job.ID) {
				continue
			}
			job := job
			repo := repo
			d.inFlight.Add(1)
			go func() {
				defer func() {
					if d.inFlight.Add(-1) == 0 {
						d.state.MarkIdle()
					}
				}()
				// Cap concurrent runner subprocesses when configured. The
				// dedup mark stays held while we wait for a slot so no other
				// scan re-picks this job; released on return either way.
				if d.runSem != nil {
					if err := d.runSem.Acquire(ctx, 1); err != nil {
						d.unmarkSeen(job.ID)
						return
					}
					defer d.runSem.Release(1)
				}
				rctx, cancel := context.WithTimeout(ctx, 6*time.Hour)
				defer cancel()
				if err := d.rl.Run(rctx, org, repo, job); err != nil {
					d.logger.Warn("jit.runner_error", "org", org, "job_id", job.ID, "err", err.Error())
				}
				d.unmarkSeen(job.ID)
			}()
		}
	}
	return nil
}

func (d *JITDaemon) markSeen(jobID int64) bool {
	d.dedupMu.Lock()
	defer d.dedupMu.Unlock()
	if _, ok := d.dedupSeen[jobID]; ok {
		return false
	}
	d.dedupSeen[jobID] = time.Now()
	return true
}

func (d *JITDaemon) unmarkSeen(jobID int64) {
	d.dedupMu.Lock()
	defer d.dedupMu.Unlock()
	delete(d.dedupSeen, jobID)
}

func (d *JITDaemon) gcDedup() {
	d.dedupMu.Lock()
	defer d.dedupMu.Unlock()
	cutoff := time.Now().Add(-6 * time.Hour)
	for id, t := range d.dedupSeen {
		if t.Before(cutoff) {
			delete(d.dedupSeen, id)
		}
	}
}

func (d *JITDaemon) serveHealth(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{
			"host":      d.cfg.HostName,
			"orgs":      d.cfg.Orgs,
			"labels":    d.cfg.Labels,
			"in_flight": d.inFlight.Load(),
			"version":   Version,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	// /metrics is a placeholder for a future Prometheus exposition. For
	// now it returns the same JSON snapshot as /healthz so external
	// scrape configurations don't 404.
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "# HELP arcd_jit_in_flight Number of active runner subprocesses.\n")
		fmt.Fprintf(w, "# TYPE arcd_jit_in_flight gauge\n")
		fmt.Fprintf(w, "arcd_jit_in_flight{host=%q} %d\n", d.cfg.HostName, d.inFlight.Load())
		fmt.Fprintf(w, "# HELP arcd_jit_orgs Number of orgs configured.\n")
		fmt.Fprintf(w, "# TYPE arcd_jit_orgs gauge\n")
		fmt.Fprintf(w, "arcd_jit_orgs{host=%q} %d\n", d.cfg.HostName, len(d.cfg.Orgs))
	})
	d.registerV1(mux)
	srv := &http.Server{
		Addr:              d.cfg.HealthAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("jit.health.serve", "err", err.Error())
	}
}
