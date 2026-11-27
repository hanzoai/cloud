// Command gen-client-catalog writes what each subsystem serves, so the surface's
// agent MCP server can answer tools/list without a process per subsystem.
//
// THE SOURCE IS EACH APP'S OWN SPEC, plugin/<app>/openapi.json, which that app's
// own binary emits from its own live router. That is the whole reason this can
// exist: an earlier catalogue was hand-kept per app and drifted — one subsystem
// declared 12 tools while it served 365 — because nothing regenerated it from
// the thing it described. These subsets are regenerated from source by
// `make -f mk/fleet.mk check` and held against the composed document by the compose, so a
// catalogue derived from them is red in CI the moment it disagrees.
//
// It carries operation ids and prose, and no schemas. The MCP server publishes one
// tool per subsystem whose `op` enum holds names, and a model fetches the schema
// for the one it picked — that fetch reaches the owning subsystem, which is one
// process rather than a hundred.
//
// THE AUDIENCE COMES FROM openapi.yaml AND NOT FROM THE SUBSET, and the two are
// not the same answer. A subset's x-public is what the app's own binary could
// derive about itself, and one term of that rule is a surface fact the app cannot
// see: its STAGE (HIP-0139 §8, stamped by the compose). Read off the subsets, a
// beta capability's 355 operations stayed in the MCP server — offered to every model
// while the same operations were absent from every generated SDK, which is
// exactly the split the paragraph below says does not exist. openapi.yaml IS the
// public contract — the compose writes the customer projection there and everything
// the surface serves to private.yaml — so reading it is not a second copy of the
// rule; it is the only copy, asked where it has been fully applied.
package main
