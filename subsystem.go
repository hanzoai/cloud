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

package cloud

// subsystem.go — the ONE map from a request path to the subsystem that serves it.
//
// The binary is a single process mounting ~60 subsystems, so "which subsystem served
// this request" is not recoverable from a span unless something records it. That
// something is here, and only here: MountAll indexes every spec's declared prefixes
// once at boot, and TracingMiddleware stamps the resolved name onto the request span
// as hanzo.subsystem.
//
// This adds a LABEL to the span that already exists — not a second metrics path. The
// span already ships over the ZAP wire into the datastore, so per-subsystem request
// rate, error rate, latency and last-error all fall out of the SAME trace table
// /v1/admin/o11y already reads. Sixty packages stay uninstrumented.

import (
	"sort"
	"strings"
	"sync/atomic"
)

// Subsystem is one entry of the composition root: its name, the route subtrees it
// owns, and whether this deployment has it switched on. A DISABLED subsystem is still
// listed — "it is off" is the answer to "why is that board empty", and dropping it
// from the inventory destroys that answer.
type Subsystem struct {
	Name     string   `json:"name"`
	Prefixes []string `json:"prefixes"`
	Enabled  bool     `json:"enabled"`
}

// subsystemIndex is the immutable boot snapshot: the declared inventory, plus the
// prefix table sorted longest-first so the FIRST match in SubsystemOf is the most
// specific one ("/v1/admin/infra" wins over "/v1/admin").
type subsystemIndex struct {
	all    []Subsystem
	routes []subsystemRoute
}

type subsystemRoute struct{ prefix, name string }

// subsystems is written ONCE by MountAll, before the listener accepts, and read by
// every traced request after. The atomic pointer keeps that read lock-free on the hot
// path; a nil index (an entrypoint or test that never calls MountAll) resolves every
// path to "", so the label is simply absent rather than wrong.
var subsystems atomic.Pointer[subsystemIndex]

// MountPrefixes is the ONE rule for which subtrees a subsystem owns: the prefixes it
// declared, else the /v1/<name> convention. scope's middleware gate and this index
// MUST agree on that rule, so they share this function instead of each spelling the
// fallback — a drift between them would mislabel every span of the subsystem that
// disagreed.
func MountPrefixes(name string, declared []string) []string {
	if len(declared) == 0 {
		return []string{"/v1/" + name}
	}
	return declared
}

// indexSubsystems builds the boot snapshot from the composition root's specs and this
// deployment's enablement. A disabled subsystem is inventoried but claims NO prefix:
// it serves nothing, so letting it own a path would attribute another subsystem's
// requests (or a 404) to it.
func indexSubsystems(specs []MountSpec, cfg *Config) {
	idx := &subsystemIndex{
		all:    make([]Subsystem, 0, len(specs)),
		routes: make([]subsystemRoute, 0, len(specs)),
	}
	for _, spec := range specs {
		prefixes := MountPrefixes(spec.Name, spec.Prefixes)
		enabled := cfg.Enabled(spec.Name)
		idx.all = append(idx.all, Subsystem{Name: spec.Name, Prefixes: prefixes, Enabled: enabled})
		if !enabled {
			continue
		}
		for _, p := range prefixes {
			idx.routes = append(idx.routes, subsystemRoute{prefix: strings.TrimSuffix(p, "/"), name: spec.Name})
		}
	}
	sort.SliceStable(idx.routes, func(i, j int) bool {
		return len(idx.routes[i].prefix) > len(idx.routes[j].prefix)
	})
	sort.Slice(idx.all, func(i, j int) bool { return idx.all[i].Name < idx.all[j].Name })
	subsystems.Store(idx)
}

// Subsystems returns the boot inventory, name-sorted — every configured subsystem and
// whether it is on. The slice is a fresh copy of an immutable snapshot, so a caller
// cannot disturb the index every request reads.
func Subsystems() []Subsystem {
	idx := subsystems.Load()
	if idx == nil {
		return []Subsystem{}
	}
	out := make([]Subsystem, len(idx.all))
	copy(out, idx.all)
	return out
}

// SubsystemOf resolves the subsystem serving path by longest declared prefix, or ""
// when nothing owns it (a non-/v1 path, or a route of a disabled subsystem).
func SubsystemOf(path string) string {
	idx := subsystems.Load()
	if idx == nil {
		return ""
	}
	for _, r := range idx.routes {
		if path == r.prefix || strings.HasPrefix(path, r.prefix+"/") {
			return r.name
		}
	}
	return ""
}
