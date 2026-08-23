// Copyright © 2026 Hanzo AI. MIT License.

package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

// privatePrefixes are the module paths that must never be LINKED into this
// binary.
//
// The boundary is a deployment fact rather than a build-tag convention: a
// private subsystem ships as a plugin the host fetches by digest and speaks ZAP
// to over its own socket, so its code cannot arrive here even by accident —
// there is no import to get wrong. What that mechanism does NOT cover is a
// private import added to a subsystem still linked in, which is the one way the
// boundary erodes quietly. Naming a new private org here is the whole of keeping
// this current.
var privatePrefixes = []string{
	"github.com/hanzo-inc/",
	"github.com/lux-private/",
	"github.com/luxfi/private",
}

// THE SHIPPED BINARY LINKS NOTHING PRIVATE, as an executable fact rather than a
// promise in a README.
//
// This is main, so what this test binary links is what the artifact links —
// there is no separate composition root to drift from. It reads its own build
// info, which needs no `go list`, no network and no exec, and therefore reports
// the graph that was actually built rather than one recomputed from source that
// may resolve differently.
//
// Measured when this was written: 408 dependencies, none of them matching. So
// the property already holds and this is here to keep it holding — it is silent
// until somebody changes the answer, which is the only kind of check worth
// standing between a commit and a licence.
func TestTheBinaryLinksNothingPrivate(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build info in this build mode")
	}
	for _, dep := range info.Deps {
		for _, prefix := range privatePrefixes {
			if strings.HasPrefix(dep.Path, prefix) {
				t.Errorf("the OSS binary links %s, which is private (%s). A private "+
					"subsystem is reached as a PLUGIN over its own socket, never linked: "+
					"the import that pulled this in has to go, or the subsystem holding it "+
					"has to become a plugin.", dep.Path, prefix)
			}
		}
	}
}
