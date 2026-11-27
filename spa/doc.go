// Package spa serves a built single-page app out of an embedded filesystem.
//
// It is the ONE way an app in this repo serves its own UI. Three apps had
// byte-identical copies of this handler (tasks, research, and meet as it landed);
// the policy in it is not obvious enough to be worth restating three times, and a
// policy restated three times drifts:
//
//   - a content-addressed asset under assets/ carries an immutable cache hint,
//     because Vite hashes the filename — the bytes at that name never change;
//   - everything else is no-cache, so a freshly deployed shell replaces the stale
//     one on the next request rather than on the next cache expiry;
//   - a path that is not a real file rewrites to index.html, which is what lets a
//     deep link survive a reload under a client-side router — EXCEPT under
//     assets/, where a miss is a 404. That subtree holds only content-addressed
//     build output, so a name that is not there is a stale shell asking for a
//     purged chunk, never a client-side route. Answering the shell there hands a
//     <script> tag a document — "Unexpected token '<'", which reads as a corrupt
//     bundle rather than the cache miss it is;
//   - a DIRECTORY is not a page. http.FileServer lists one, so serving whatever
//     stat succeeds on publishes the whole asset manifest at /<app>/assets/;
//     a directory falls through to the shell (or 404s under assets/) instead;
//   - a MISSING index.html is 503, not a blank 200. A bundle that failed to sync
//     is then loud in staging instead of being a white page in production.
//
// It is NOT the console at "/" (webui). That one is a different handler on
// purpose: it owns the root catch-all, so it has to refuse the API namespaces
// outright and rewrite the document title per white-label host. This one is
// mounted under its app's own prefix and never sees a path that is not its own.
//
// It is a LEAF — stdlib only — so any app can serve its UI without linking the
// fleet.
package spa
