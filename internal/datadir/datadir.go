// Copyright 2026 Hanzo AI, Inc. All rights reserved.

// Package datadir names the on-disk data root, once.
//
// It is a leaf so that the light router in cmd/cloud can agree with the request
// tier about WHERE the volume is without linking it. That agreement is load
// bearing: the router takes the pod's writer lease on a lock file under this
// path and the children open their stores under it, so a second definition that
// drifted by one directory would put the lock somewhere nothing else consults —
// an interlock guarding an empty folder.
package datadir

import "os"

// EnvVar overrides the root.
const EnvVar = "CLOUD_DATA_DIR"

// Default is the on-disk data root when EnvVar is unset.
const Default = "/var/lib/cloud"

// Resolve returns the data root this process must use.
func Resolve() string {
	if v := os.Getenv(EnvVar); v != "" {
		return v
	}
	return Default
}
