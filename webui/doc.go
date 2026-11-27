// Package webui serves the Hanzo Cloud console — the single-page app,
// white-labelled per request Host.
//
// It is a LEAF: stdlib + the brand registry + the fleet manifest's addresses +
// zip's net/http bridge, and NOTHING of package cloud (all three of those are
// themselves leaves). That is the whole reason it exists as its own package. The
// console has to be served from TWO places — every per-app plugin mounts it as
// its "/" catch-all through cloud.Listen, and the light host (cmd/cloud) owns "/"
// at the entry point and cannot import package cloud (that would relink the
// fleet it was split to avoid). One console implementation, reachable from both.
//
// IT CARRIES NO BYTES. The console used to be //go:embed'd from webui/dist, which
// the image build overwrote with the real static export — so the frontend's
// lifecycle was welded to this binary's: a CSS fix cost a ~22-minute cloud build
// plus a `strategy: Recreate` single-replica rollout, measured at 2m15s of
// api.hanzo.ai being down. The bytes are a PUBLISHED SITE RELEASE now (the same
// release subsystem that serves cd.hanzo.ai), read into memory by webui/release
// and handed in here as an fs.FS. A console release goes live in under a second
// and rolls back just as fast, with no build and no restart.
//
// What did NOT change is the HOST ROUTING: console.hanzo.ai is still this binary
// and still answers /v1 on the same origin, so the console's session cookie stays
// first-party. Only the source of the bytes moved. The sites edge deliberately
// does not own that host — a site host serves bytes and nothing else.
//
// This package decides how the bytes are SERVED: per-Host white-label <title>,
// static-export route shells, precompressed negotiation, cache policy by asset
// class, and the API-namespace 404 that keeps HTML out of a JSON caller's mouth.
package webui
