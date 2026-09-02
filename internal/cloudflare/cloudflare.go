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

import (
	"strings"

	"github.com/hanzoai/cloud/internal/environ"
)

// APIBase is the API v4 root, overridable so a test can point at its own server.
func APIBase() string {
	if v := environ.Or("CLOUDFLARE_API_BASE", ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.cloudflare.com/client/v4"
}

// Envelope is the shape every v4 answer carries. Success is the field to read:
// Cloudflare answers 200 with success:false, so an HTTP status alone reports a
// failed call as a successful one.
//
// The errors are Cloudflare's own words and carry no credential, which is what
// makes them safe to pass back to a caller verbatim.
type Envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}
