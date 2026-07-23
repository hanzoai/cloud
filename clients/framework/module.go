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
	alwaysOn       = map[string]bool{}
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

// resetModules clears the registry AND the always-on set. TEST-ONLY, mirroring
// resetHooks — keeps install tests independent of process-global registrations.
func resetModules() {
	moduleMu.Lock()
	defer moduleMu.Unlock()
	moduleRegistry = map[string][]DocType{}
	alwaysOn = map[string]bool{}
}

// MarkAlwaysOn marks a registered module as ALWAYS-ON: its DocType fixtures resolve
// for EVERY org without a per-org install — GetDocType, ListDocTypes, and the
// Installed predicate all satisfy them for an org that never ran the install step. A
// standard lane (the Guide's marketing content model) calls this from its init() right
// after RegisterModule, the same self-declaration pattern, so a fresh org's journey
// works with no manual setup.
//
// SCOPE — it changes only SCHEMA availability, NEVER tenant data isolation. Every
// document row stays physically org-scoped (the `org` column on every query); a
// resolved always-on DocType only says "this schema exists for you", it exposes no
// other org's records. A per-org stored DocType (a customization) always overrides the
// fixture (see GetDocType). See always_on_isolation_test.go for the cross-tenant proof.
func MarkAlwaysOn(module string) {
	if module == "" {
		return
	}
	moduleMu.Lock()
	defer moduleMu.Unlock()
	alwaysOn[module] = true
}

// AlwaysOnModules returns the sorted set of modules marked always-on — introspection
// for a catalog / link-guard test.
func AlwaysOnModules() []string {
	moduleMu.RLock()
	defer moduleMu.RUnlock()
	return sortedAlwaysOnLocked()
}

// sortedAlwaysOnLocked returns the always-on module names in deterministic order.
// Assumes moduleMu is already held (read or write).
func sortedAlwaysOnLocked() []string {
	out := make([]string, 0, len(alwaysOn))
	for m := range alwaysOn {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// alwaysOnDocType resolves a DocType by name from the always-on fixture set: the first
// always-on module (in deterministic module order) that declares a fixture of that
// name, module-stamped and normalized (secure-by-default perms seeded). ok=false when
// no always-on module provides it. The Store uses this as the fallback when an org has
// no stored row — a fresh org resolves the lane's schema without a per-org install.
func alwaysOnDocType(name string) (DocType, bool) {
	moduleMu.RLock()
	defer moduleMu.RUnlock()
	for _, m := range sortedAlwaysOnLocked() {
		for _, dt := range moduleRegistry[m] {
			if dt.Name == name {
				return stampFixture(dt, m), true
			}
		}
	}
	return DocType{}, false
}

// alwaysOnDocTypes returns every always-on DocType fixture, module-stamped and
// normalized, in deterministic order (module asc, then fixture order). ListDocTypes
// unions these in so a fresh org's listing matches GetDocType (no "resolves but doesn't
// list" asymmetry).
func alwaysOnDocTypes() []DocType {
	moduleMu.RLock()
	defer moduleMu.RUnlock()
	var out []DocType
	for _, m := range sortedAlwaysOnLocked() {
		for _, dt := range moduleRegistry[m] {
			out = append(out, stampFixture(dt, m))
		}
	}
	return out
}

// stampFixture returns an independent copy of a registry fixture, stamped with its
// owning module and normalized. Fields/Perms are cloned so a virtual DocType has the
// same slice independence a stored one gets from scanDocType — a caller can never
// mutate the process-global registry through a resolved fixture.
func stampFixture(dt DocType, module string) DocType {
	dt.Module = module
	dt.Fields = append([]DocField(nil), dt.Fields...)
	dt.Perms = append([]DocPerm(nil), dt.Perms...)
	dt.normalize()
	return dt
}
