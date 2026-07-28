// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package wallet embeds the built hanzo.team usage/wallet page — a SMALL
// @hanzo/ui@8 React static export (app/, Vite) — into the cloud binary, the
// same one-binary/one-origin precedent as the console (webui.go) and the tasks
// SPA (clients/tasks/ui). clients/team serves it session-gated at
// /v1/team/billing/ui/ (billing.go).
//
// dist/ is the committed Vite build. To refresh it:
//
//	cd clients/team/wallet/app && npm install && npm run build
//
// (build output lands in ../dist; commit it).
package wallet

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded built-page filesystem rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Impossible at runtime: embed.FS entries are validated at compile time.
		return distFS
	}
	return sub
}
