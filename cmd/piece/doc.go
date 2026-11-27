// Command piece generates the connector catalog from the ActivePieces pieces
// source into piece/catalog: catalog.json, logo/, and program/ — the bundled
// program of every piece that runs in-process. It is the ONE way those files
// are written.
//
// Usage:
//
//	go run ./cmd/piece -src <checkout> -rev <repo@commit> [-logos]
//
// -src is a checkout of the tree hanzoai/auto was forked from, at the commit
// -rev names; every directory under packages/pieces/{core,community} with a
// src/index.ts is a piece. The catalog records -rev, so a reader can always
// say which source a card came from.
//
// Each piece is bundled twice with esbuild and loaded in goja, never in Node:
//
//   - OPEN, every npm import left external, and described (script.Describe).
//     The module body runs against the framework shim with stand-ins for its
//     SDKs, which is enough to reach createPiece and read the declarations —
//     name, auth, actions, triggers — as the piece itself states them.
//   - CLOSED, only the framework external. A piece that bundles this way needs
//     nothing but the shim, so its program is written to program/ and its
//     actions run in-process. One that imports an SDK does not bundle and
//     is catalogued as not runnable, which is the truth about it here.
//
// -logos fetches each piece's declared logo once, bounds it to 96px PNG (or
// keeps an SVG that carries no script), and writes it to logo/. Without
// it the logos already on disk are kept, so metadata regenerates offline.
package main
