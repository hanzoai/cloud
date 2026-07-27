// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"fmt"

	"github.com/zap-proto/zip"
)

// PluginSpec returns a MountSpec that serves prefixes from a SEPARATE binary
// instead of code linked into this one.
//
// It exists so that where a subsystem runs stops being a property of the source.
// zip.Load returns a zip.Service — the same type a linked-in service is — so the
// only difference between "compiled in" and "its own process" is which MountSpec
// Wire() lists. Moving one out is a one-line edit at the composition root, and
// nothing downstream (routing, health, shutdown ordering) can tell the difference.
//
// The plugin names exactly one of Addr (already listening), Bin (the binary,
// normally go:embed'd), Path, or URL+Sum. For all but Addr, zip starts it as a
// child on a private unix socket and mounts the routes onto it; the child is
// stopped when Shutdown runs, so a plugin subsystem tears down with the rest.
//
// Pass EVERY prefix the subsystem owns. A subsystem routinely owns more than one
// route subtree — o11y answers /v1/o11y AND /v1/sentry — and a prefix left out is
// not an error, it is a silent 404 on that subtree, which is the worst way for
// this to fail. Grep the subsystem's Mount for every path it registers before
// converting it. Naming none at all is refused rather than mounted inert.
//
// Global is set because zip.Load registers under the prefixes it was given.
// Handing it a scoped Router would nest those prefixes under the subsystem name
// and the routes would answer somewhere nobody is asking.
func PluginSpec(name string, p zip.Plugin, prefixes ...string) MountSpec {
	if p.Name == "" {
		p.Name = name
	}
	return MountSpec{
		Name:   name,
		Global: true,
		Plugin: true,
		// The subtrees this subsystem owns, recorded on the spec rather than
		// captured only in the closure. MountAll ignores Prefixes for a Global
		// spec, so this costs nothing at mount — and it means the ONE list can be
		// asked what a plugin serves without starting it.
		Prefixes: prefixes,
		Mount: func(router Router, _ Deps) error {
			if len(prefixes) == 0 {
				return fmt.Errorf("pluginspec %q: no prefix — a plugin that owns nothing serves nothing", name)
			}
			app, ok := router.(*zip.App)
			if !ok {
				return fmt.Errorf("pluginspec %q: needs the root app, got %T — Global must stay set", name, router)
			}
			return zip.Load(p, prefixes...)(app)
		},
	}
}
