// Command notify is the notify app's plugin binary. The host (cmd/cloud) starts it
// and routes the app's prefixes to it; it serves the app through cloud.Listen
// and links only that app's own graph.
package main
