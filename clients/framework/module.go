package framework

import (
	"sort"
	"sync"
)

// Modules — the app-lane fixture registry.
//
// A sibling app lane (cms, erp, helpdesk, …) DECLARES its content model as a set
// of DocType fixtures and registers them here from a package init(), exactly as
// hooks are registered (hook.go). Installing a module into an org ensures those
// DocTypes exist in that org's per-org store — the Frappe "install app / load
// fixtures" step, decomplected to pure data. This is the ONE way an app lane
// seeds its content model: it never forks the engine and never hand-rolls a
// second install path. CMS is the first lane; ERP/CRM/Helpdesk register the same
// way and reuse the SAME generic install below.
//
// The registry is process-global (fixtures are compile-time first-party data,
// like hooks), read under a mutex so a late init() registration is still safe.
// Installation itself is per-org and gated (managerOnly) at the HTTP layer, so a
// module's fixtures only ever land in an org an authorized owner installs them
// into — the registry holds NO tenant state.
var (
	moduleMu       sync.RWMutex
	moduleRegistry = map[string][]DocType{}
)

// RegisterModule declares the DocType fixtures a module installs. Call from a
// package init() so the content model is declared once at build time. The module
// name is stamped onto every fixture at install so a lane's DocTypes are always
// discoverable by `module`. Fixtures are cloned on registration so a caller's
// slice can never mutate the registry.
func RegisterModule(module string, fixtures []DocType) {
	if module == "" || len(fixtures) == 0 {
		return
	}
	cp := make([]DocType, len(fixtures))
	copy(cp, fixtures)
	moduleMu.Lock()
	defer moduleMu.Unlock()
	moduleRegistry[module] = append(moduleRegistry[module], cp...)
}

// moduleFixtures returns the registered fixtures for a module (nil if none).
func moduleFixtures(module string) []DocType {
	moduleMu.RLock()
	defer moduleMu.RUnlock()
	return moduleRegistry[module]
}

// registeredModules returns the sorted set of registered module names.
func registeredModules() []string {
	moduleMu.RLock()
	defer moduleMu.RUnlock()
	out := make([]string, 0, len(moduleRegistry))
	for m := range moduleRegistry {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// RegisteredModules returns the sorted set of registered content-module names.
// Exported so the composition root's link-guard test can assert the app lanes
// (cms/erp/help) are compiled into the binary — their fixtures and hooks register
// from a package init(), so a missing blank import silently empties this set.
func RegisteredModules() []string { return registeredModules() }

// resetModules clears the registry. TEST-ONLY, mirroring resetHooks — keeps
// install tests independent of process-global registrations.
func resetModules() {
	moduleMu.Lock()
	defer moduleMu.Unlock()
	moduleRegistry = map[string][]DocType{}
}
