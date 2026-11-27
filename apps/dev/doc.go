// Package dev is Hanzo Dev in the cloud: a person's Dev server, running in a
// sandbox cell (or backed by the wasm chat runtime or a linked desktop source),
// reachable from a browser, CLI or mobile client with nothing installed.
//
//	GET    /v1/dev/health                  liveness
//	POST   /v1/dev/sessions                lease the caller's cell (or resolve wasm/desktop) and start Dev
//	DELETE /v1/dev/sessions/:id            end the session and release its cell or dispatch lease
//	POST   /v1/dev/sessions/:id/suspend    park the session's cell while keeping its disk
//	POST   /v1/dev/sessions/:id/resume     wake a suspended session and restart Dev in its cell
//	POST   /v1/dev/sessions/:id/interrupt  stop commands running in the session's cell
//	WT     /v1/dev/zap/:id                 bidirectional WebTransport stream of ZAP envelopes
//
// A sandbox session is a sandbox lease. The sandbox owns every part of a cell's
// life that is not Dev's: the pod and its runtime (visor), the org's quota of
// running cells, the lease clock, parking, and the reaper that retires an idle
// cell. A caller's cell is found again by its project, which is a hash of who
// and which thread, so a second POST answers the cell the first one took; when a
// parked cell is resumed, Dev is started again on its ephemeral /tmp ZAP socket.
//
// The reply is the runtime a Dev front end resolves: a stable id and label, the
// WebTransport ZAP endpoint (`/v1/dev/zap/:id`, HIP-0114), the sandbox that backs
// the terminal and browser dock when running in a cloud cell, and any linked
// desktop dispatch lease.
package dev
