// Package appcred is the one ladder for a social network's developer-app
// credentials.
//
// Connect and the publish-readiness check both read it, so a deployment that
// set the name one of them used to look at is still configured. The first pair
// is the canonical name. A later pair is a fallback: it is used only when no
// earlier pair has both values, and an organization's own sealed app is applied
// by the caller before any of these.
package appcred
