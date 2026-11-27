// Package cloudflare states what Cloudflare's API v4 looks like: where it is, and
// the envelope every one of its answers arrives in.
//
// It is a LEAF and holds no client. Two subsystems call that API for unrelated
// reasons — one custodies an org's token, the other proxies a zone — and each
// keeps its own client, its own credential and its own timeouts. What they cannot
// each keep is a different idea of where the API is or how it reports failure,
// which is what two byte-identical copies of these were waiting to become.
//
// apps/sites/cloudflare deliberately does NOT read from here: its base is a struct
// field set at construction, so that package imports nothing from this repo. That
// is its design rather than a third copy.
package cloudflare
