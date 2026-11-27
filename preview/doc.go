// Package preview is the address a port in a sandbox is served at, and the
// router's half of serving it.
//
// A port is served at an origin of its own,
//
//	https://sandbox-<24 hex>-preview-<port>.<sites apex>/
//
// so the app behind it owns its whole path space: a dev server's absolute asset
// paths, its router and its HMR socket work as they do on localhost, and it
// shares no origin with the API or the console. The label is longer than a
// project slug may be (40), so no published site can claim one.
//
// The router rewrites a request for such a host onto the sandbox app's route,
// Prefix + <id>/<port>/<path>, and the sandbox app authenticates it and carries
// it into the pod. The sandbox app refuses a request whose Host is not the
// preview host its path names, so the route serves nothing under any other
// origin.
package preview
