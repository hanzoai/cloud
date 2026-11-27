// Command ai is the ai app's plugin binary, and the one binary that carries the
// co-resident zen app: zen gates zen model requests ahead of ai's /v1. The host
// (cmd/cloud) starts it and routes the app's prefixes to it; it serves both
// through cloud.Listen and links only their own graph.
package main
