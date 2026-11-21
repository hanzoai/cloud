// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package tetragonenforcement

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// BenchWorkload selects the microbenchmark workload.
type BenchWorkload string

const (
	// BenchOpenClose measures open()+close() pairs on a temp file: the
	// syscall path every sandbox agent hooks for file monitoring.
	BenchOpenClose BenchWorkload = "open-close"
	// BenchLoopbackConnect measures TCP connect+close against loopback:
	// the path egress enforcement hooks (tcp_connect).
	BenchLoopbackConnect BenchWorkload = "loopback-connect"
)

// BenchSample is one measured run of one workload under one mode.
type BenchSample struct {
	Workload   string  `json:"workload"`
	Mode       string  `json:"mode"`
	Workers    int     `json:"workers"`
	DurationS  float64 `json:"duration_s"`
	Ops        int64   `json:"ops"`
	OpsPerSec  float64 `json:"ops_per_sec"`
	NsPerOp    float64 `json:"ns_per_op"`
	HostKernel string  `json:"host_kernel,omitempty"`
}

// BenchReport merges the three required passes (none/monitor/enforcement)
// for one workload and computes overhead percentages. Overhead is
// (baseline-enforced)/baseline*100: positive means enforcement slowed the
// workload down. A negative raw value is kept and Noise is set instead of
// clamping (measurement noise is reported, not hidden).
type BenchReport struct {
	Workload               string       `json:"workload"`
	Baseline               BenchSample  `json:"baseline"`
	Monitor                *BenchSample `json:"monitor,omitempty"`
	Enforcement            *BenchSample `json:"enforcement,omitempty"`
	OverheadMonitorPct     *float64     `json:"overhead_monitor_pct,omitempty"`
	OverheadEnforcementPct *float64     `json:"overhead_enforcement_pct,omitempty"`
	Noise                  bool         `json:"noise"`
}

// BenchReports is the committed raw-results shape for benchmarks.
type BenchReports struct {
	RunID      string        `json:"run_id"`
	StartedUTC time.Time     `json:"started_utc"`
	Host       HostInfo      `json:"host"`
	Workloads  []BenchReport `json:"workloads"`
}

// OverheadPercent returns the percentage slowdown of other relative to
// base. base must be positive.
func OverheadPercent(base, other float64) (float64, error) {
	if base <= 0 {
		return 0, fmt.Errorf("baseline ops/sec must be > 0, got %g", base)
	}
	return (base - other) / base * 100, nil
}

// BuildBenchReports merges per-workload samples from the three passes.
// Samples are matched by workload; a missing monitor or enforcement sample
// is simply omitted from the report (never guessed).
func BuildBenchReports(baseline, monitor, enforcement []BenchSample) []BenchReport {
	pick := func(samples []BenchSample, workload string) *BenchSample {
		for _, s := range samples {
			if s.Workload == workload {
				c := s
				return &c
			}
		}
		return nil
	}
	var out []BenchReport
	seen := map[string]bool{}
	for _, b := range baseline {
		if seen[b.Workload] {
			continue
		}
		seen[b.Workload] = true
		rep := BenchReport{Workload: b.Workload, Baseline: b}
		rep.Monitor = pick(monitor, b.Workload)
		rep.Enforcement = pick(enforcement, b.Workload)
		if rep.Monitor != nil {
			if v, err := OverheadPercent(b.OpsPerSec, rep.Monitor.OpsPerSec); err == nil {
				rep.OverheadMonitorPct = &v
			}
		}
		if rep.Enforcement != nil {
			if v, err := OverheadPercent(b.OpsPerSec, rep.Enforcement.OpsPerSec); err == nil {
				rep.OverheadEnforcementPct = &v
			}
		}
		if (rep.OverheadMonitorPct != nil && *rep.OverheadMonitorPct < 0) ||
			(rep.OverheadEnforcementPct != nil && *rep.OverheadEnforcementPct < 0) {
			rep.Noise = true
		}
		out = append(out, rep)
	}
	return out
}

// RunBench executes one benchmark pass. Workers hammer the workload until
// the context deadline; ops are counted atomically and normalized by the
// measured wall time (not the configured deadline).
func RunBench(ctx context.Context, cfg BenchConfig) (BenchSample, error) {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	sample := BenchSample{
		Workload:  string(cfg.Workload),
		Mode:      string(cfg.Mode),
		Workers:   cfg.Workers,
		DurationS: cfg.Duration.Seconds(),
	}

	var ops atomic.Int64
	var startWall time.Time
	var stopOnce sync.Once
	done := make(chan struct{})

	switch cfg.Workload {
	case BenchOpenClose:
		f, err := os.CreateTemp("", "tetragon-bench-*.bin")
		if err != nil {
			return sample, fmt.Errorf("create bench file: %w", err)
		}
		f.WriteString("bench") //nolint:errcheck
		f.Close()
		defer os.Remove(f.Name())
		path := f.Name()
		startWall = time.Now()
		for w := 0; w < cfg.Workers; w++ {
			go func() {
				for {
					select {
					case <-done:
						return
					case <-ctx.Done():
						return
					default:
					}
					fh, err := os.Open(path)
					if err == nil {
						fh.Close()
						ops.Add(1)
					}
				}
			}()
		}
	case BenchLoopbackConnect:
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return sample, fmt.Errorf("listen loopback: %w", err)
		}
		defer l.Close()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		addr := l.Addr().String()
		startWall = time.Now()
		for w := 0; w < cfg.Workers; w++ {
			go func() {
				for {
					select {
					case <-done:
						return
					case <-ctx.Done():
						return
					default:
					}
					c, err := net.DialTimeout("tcp", addr, time.Second)
					if err == nil {
						c.Close()
						ops.Add(1)
					}
				}
			}()
		}
	default:
		return sample, fmt.Errorf("unknown workload %q", cfg.Workload)
	}

	<-ctx.Done()
	stopOnce.Do(func() { close(done) })
	wall := time.Since(startWall).Seconds()
	if wall <= 0 {
		wall = 1e-9
	}
	n := ops.Load()
	sample.Ops = n
	sample.DurationS = wall
	sample.OpsPerSec = float64(n) / wall
	if n > 0 {
		sample.NsPerOp = wall * 1e9 / float64(n)
	}
	sample.HostKernel = kernelVersion()
	return sample, nil
}

// BenchConfig parameterizes one bench pass.
type BenchConfig struct {
	Workload BenchWorkload
	Mode     RunMode
	Duration time.Duration
	Workers  int
}
