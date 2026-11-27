package cloudflare

import (
	"strings"

	"github.com/hanzoai/cloud/sdk/env"
)

// APIBase is the API v4 root, overridable so a test can point at its own server.
func APIBase() string {
	if v := env.Or("CLOUDFLARE_API_BASE", ""); v != "" {
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
