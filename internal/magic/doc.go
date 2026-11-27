// Package magic names a file type from the bytes themselves.
//
// This is an ALLOW-LIST, not a detector. It exists because the only safe answer to
// "what type is this upload?" is one derived from the stored bytes: a filename, a
// Content-Type part header and a client's word are all attacker-chosen, and a
// crafted .svg/.html served back under the type its NAME claimed executes script in
// the viewer's origin. So a caller serves what Type() returns and nothing else —
// "" means "not a type I will name", and the caller's job is then to serve it inert
// (application/octet-stream + attachment) or refuse it outright.
//
// Everything named here a browser hands to a viewer of its own, and under
// X-Content-Type-Options: nosniff cannot be talked into treating as a document in
// the serving origin: four raster formats and PDF. SVG is deliberately absent and
// must stay absent: it is XML with <script> in it, so it is a program, not a
// picture. WHICH of these a caller renders inline is the caller's policy and not
// this package's — a profile photo is a picture, a data room holds pictures and
// decks — so a caller that means "a picture" says image/ and means it.
//
// Deterministic and dependency-free — deliberately NOT net/http.DetectContentType,
// whose table sniffs HTML/XML and evolves between Go releases, which is exactly the
// unpinned behavior a security allow-list must not inherit.
package magic
