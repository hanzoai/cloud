// Package library is the Library: everything a person made, in one place.
//
// Images, posters, documents, slides, type, 3D, video and audio from Studio;
// files and generated images from Chat; patches, builds and reports from Dev;
// what a workflow or a scheduled task produced. One index, one store, one write
// path: every product files its artifacts here — over the plane (library_put),
// or from a browser (POST /v1/library) — so there is never a second asset store.
//
// Surface (all /v1/library, all person-scoped; ✓ typed):
//
//	✓ GET    /v1/library            what you made and what your org shared with you
//	✓ POST   /v1/library            keep one thing (base64, at most 10 MiB)
//	✓ GET    /v1/library/unowned    the SuperAdmin's read-only, audited view of rows no one owns
//	✓ GET    /v1/library/:id        one item, with fresh signed URLs
//	✓ PATCH  /v1/library/:id        rename it, share it with the org, move it to a project
//	✓ DELETE /v1/library/:id        delete it and its bytes
//
// SCOPING. An item belongs to the org AND the person the identity boundary
// vouched for (store.go). Another member of the org sees it only once its owner
// shared it; another org never sees it, and is answered 404 by id — the same
// answer as an id that never existed. Only the owner changes or deletes it.
package library
