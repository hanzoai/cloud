// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package admin

// plugins — GET /v1/admin/plugins, what this host is ACTUALLY running.
//
// Every other board here aggregates an upstream. This one does not: it asks the
// process itself (zip's App.Plugins()) which subsystems it loaded out-of-binary, at
// which artifact digest, whether they are still up, and what each costs — pid, uptime,
// reloads, restarts, and CPU/RSS/threads/FDs read from /proc.
//
// It exists because a deployment manifest answers what was INTENDED. Only the process
// knows what is TRUE, and during a rolling upgrade the two disagree BY DESIGN. The
// version here is the artifact's SHA-256 — the one version identifier that cannot drift
// from the bits running, because it IS the bits.
//
// SUPERADMIN ONLY (core.Guard, like every platform /v1/admin/* read). Artifact digests,
// pids and resource usage are operational detail: they name what is deployed and where
// the memory went, which is not a customer-visible fact.
//
// HONEST BY CONSTRUCTION, in two ways that matter:
//
//   - THIS REPLICA, NOT THE FLEET. App.Plugins() is one process's answer. The board
//     stamps Host (Deps.Self — the same id the durability ring elects on) so a reader
//     knows which pod answered. A board that presented one pod's restarts as the
//     fleet's would be worse than no board.
//   - A HOST WITH NO PLUGINS IS AN HONEST EMPTY LIST, never an error. "Everything is
//     linked into this binary" is a true and expected answer — cloud composes a
//     linked-in service and a plugin as the SAME type, so which one a subsystem is
//     today is a Wire() decision, not a property of the code.
//
// Restarts is the field this board exists for. Reloads are deliberate (an operator
// swapped the binary); Restarts is the supervisor bringing a plugin back after it died
// on its own. Nonzero Restarts is a CRASH, and a climbing one is a crash loop. The
// verdict is computed HERE, once, so no reader has to re-derive the policy — and no
// two readers can disagree about what "unhealthy" means.

import (
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// pluginRow is one plugin as this host currently sees it — zip.PluginStatus projected
// onto the operator's wire contract, plus the two derived facts every reader would
// otherwise re-derive (uptime, crashed).
type pluginRow struct {
	Name string `json:"name"`
	// Prefixes is EVERY route subtree this plugin answers. It is the blast radius:
	// what stops answering when this plugin does.
	Prefixes []string `json:"prefixes"`
	// Source is where the binary came from: embedded | path | url | remote.
	Source string `json:"source"`
	// Version is the artifact's SHA-256 when installed from a URL — empty for the
	// other sources, which have no digest to report. Empty means "no version to
	// report", NEVER "version unknown but probably fine".
	Version string `json:"version,omitempty"`
	Addr    string `json:"addr,omitempty"`
	PID     int    `json:"pid,omitempty"`
	// Running is false after an Unload, or after a child died with no Reload to
	// replace it. Its routes stay registered and answer 503 — so false here is the
	// difference between "not deployed" and "deployed but down".
	Running bool `json:"running"`
	// Since is when the CURRENT instance started; it resets on Reload, so UptimeSec
	// is the age of what is running, not of the mount.
	Since     time.Time `json:"since,omitzero"`
	UptimeSec int64     `json:"uptimeSec"`
	// Reloads are DELIBERATE swaps. Restarts are the supervisor resurrecting a plugin
	// that died. Never fold them: one is a deploy, the other is an outage.
	Reloads  int `json:"reloads"`
	Restarts int `json:"restarts"`
	// Crashed is the ONE derived verdict: Restarts > 0. Computed server-side so the
	// rule lives in exactly one place and no two readers can disagree about it.
	Crashed bool `json:"crashed"`

	// Kernel-measured cost. Zero means NOT MEASURED (no /proc, or nothing running),
	// not measured-as-zero — which is why Running is the field to read first.
	CPUSec   float64 `json:"cpuSec,omitzero"`
	RSSBytes int64   `json:"rssBytes,omitzero"`
	Threads  int     `json:"threads,omitzero"`
	FDs      int     `json:"fds,omitzero"`
}

// pluginBoard is the whole read: which replica answered, when, the rollup the operator's
// KPI band renders, and the rows.
type pluginBoard struct {
	// Host is THIS replica's id (Deps.Self). The rows below are its answer alone.
	Host string    `json:"host"`
	At   time.Time `json:"at"`
	// Total/Running/Down/Crashed are the KPI band. Down counts loaded-but-not-running
	// (routes answering 503); Crashed counts plugins the supervisor has had to
	// resurrect at least once, whether or not they are up right now.
	Total   int         `json:"total"`
	Running int         `json:"running"`
	Down    int         `json:"down"`
	Crashed int         `json:"crashed"`
	Plugins []pluginRow `json:"plugins"`
}

// pluginsBoard binds the handler to the host it reports on. Router is the ONLY thing a
// subsystem is handed, and Plugins() is its read-only window onto the process — so the
// board closes over the router rather than admin holding a second reference to the app.
// Deps.Self is captured at mount: it is fixed for the life of the process.
func pluginsBoard(app cloud.Router, self string) core.Handler {
	return func(_ *cloud.Service[core.State], c *zip.Ctx) error {
		return core.OK(c, readPlugins(app.Plugins(), self, time.Now().UTC()))
	}
}

// readPlugins is the pure projection: zip's status → the board. Pure so the rollup and
// the crash verdict are testable without a process to supervise.
func readPlugins(ps []zip.PluginStatus, self string, now time.Time) pluginBoard {
	b := pluginBoard{Host: self, At: now, Total: len(ps), Plugins: make([]pluginRow, 0, len(ps))}
	for _, p := range ps {
		r := pluginRow{
			Name:     p.Name,
			Prefixes: prefixesOf(p),
			Source:   p.Source,
			Version:  p.Version,
			Addr:     p.Addr,
			PID:      p.PID,
			Running:  p.Running,
			Since:    p.Since,
			Reloads:  p.Reloads,
			Restarts: p.Restarts,
			Crashed:  p.Restarts > 0,
			CPUSec:   p.Usage.CPU.Seconds(),
			RSSBytes: p.Usage.RSS,
			Threads:  p.Usage.Threads,
			FDs:      p.Usage.FDs,
		}
		// Uptime is derived from Since, which resets on Reload. A zero/future Since
		// yields 0 rather than a negative or absurd age — an unknown uptime reads as
		// unknown, never as a number someone might trust.
		if !p.Since.IsZero() && now.After(p.Since) {
			r.UptimeSec = int64(now.Sub(p.Since) / time.Second)
		}
		if r.Running {
			b.Running++
		} else {
			b.Down++
		}
		if r.Crashed {
			b.Crashed++
		}
		b.Plugins = append(b.Plugins, r)
	}
	return b
}

// prefixesOf returns every subtree a plugin answers. zip reports Prefixes plus Prefix
// (the first, the one log lines name it by); older hosts carry only Prefix, so fall back
// to it rather than reporting an empty blast radius.
func prefixesOf(p zip.PluginStatus) []string {
	if len(p.Prefixes) > 0 {
		return p.Prefixes
	}
	if p.Prefix != "" {
		return []string{p.Prefix}
	}
	return []string{}
}
