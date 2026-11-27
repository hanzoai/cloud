// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package manifest

import (
	"runtime"

	"github.com/hanzoai/cloud/internal/pin"
	"github.com/zap-proto/zip"
)

// remote resolves this app to the artifact plugins.lock pins for the running
// platform. False when the lock has no stanza for it, none for this os/arch, or
// the lock does not parse; the caller then falls through to the on-disk failure.
// A plugin the lock does not name is the default deployment: its binary ships
// beside the host.
func (a App) remote() (zip.Plugin, bool) {
	f, err := pin.Current()
	if err != nil {
		return zip.Plugin{}, false
	}
	return a.Locked(f)
}

// Locked resolves this app against a lock the caller already holds.
func (a App) Locked(f pin.File) (zip.Plugin, bool) {
	e, ok := f.Find(a.Name)
	if !ok {
		return zip.Plugin{}, false
	}
	b, ok := e.Binaries[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return zip.Plugin{}, false
	}
	// Sum is what makes fetching code safe to execute: the lock parser refuses a
	// binary without one, zip verifies before chmod, and zip caches by digest, so
	// restart and rollback touch no network.
	return zip.Plugin{
		Name:      a.Name,
		URL:       b.URL,
		Sum:       b.Sum,
		Lazy:      !a.Pinned(),
		IdleAfter: a.idleAfter(),
	}, true
}
