// Package manifesttest is the shared gate for one production bug class: an app
// SERVES a path the fleet does not ROUTE to it.
//
// The fleet is one process per app behind a prefix router (manifest.Apps). A
// request reaches an app only if some prefix in that app's row owns the path, so
// a route an app registers and its row does not claim is served by nobody —
// worse than a 404, because the request still lands somewhere: on whichever app
// owns "/" (the console), which answers its HTML shell with a 200. The product
// looks up and is not there.
//
// WHY A SECOND GATE. manifest/router_test.go already asks the real router where
// a published path lands, and it is the right check for everything it can see —
// but it reads each app's plugin/<app>/openapi.json, so it sees only TYPED,
// documented routes. An embedded SPA is served by an untyped All() route. It
// appears in no document, so that gate had nothing to compare and a whole
// product surface went unrouted with every test green.
//
// So this one does not read a document at all. It reads the app's LIVE ROUTER
// DECLARATION (zip's App.Routes, which is "complete by construction — a route
// the plugin serves and does not publish is still declared"), which is the same
// source the process itself answers from. Typed or untyped, published or not: if
// the app will answer it, this sees it.
//
// It lives in the APP's package rather than in manifest's, because only the app
// can Mount itself and manifest must not import the fleet it routes. The
// assertion lives here, once, so every app shares ONE correctness check.
package manifesttest
