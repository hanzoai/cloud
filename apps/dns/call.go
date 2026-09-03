package dns

import "errors"

// Call is one DNS request, normalized: the head has already validated the caller
// and scoped the path, so the plane reads values and never a request.
//
// It carries the caller's own method, path, query and body because the plane's API
// IS this surface's contract — what the caller asked for is what goes upstream, so
// an address the route table names and one it does not travel the same way.
type Call struct {
	Org         string // the server-validated tenant, never a client header
	Bearer      string // the caller's OWN validated session bearer
	Method      string // the caller's HTTP method
	Path        string // the caller's path, normalized, always under /v1/dns
	Query       string // the caller's raw query string
	Body        []byte // the caller's request body, unread; valid for this call only
	ContentType string // the caller's Content-Type, "" when it sent none
}

// Answer is one DNS response, normalized: the status the plane gave, its body, and
// the two headers this surface carries back (its own Content-Type, and Location on
// a redirect that is relayed rather than followed).
type Answer struct {
	Status      int
	ContentType string
	Location    string
	Body        []byte
}

// The two conditions the plane reports rather than answers; the head turns them
// into this surface's 503 and 502. Everything else reaching the head is the plane's
// own error and answers 502 with no upstream detail.
var (
	// ErrUnconfigured — the plane has no endpoint to reach.
	ErrUnconfigured = errors.New("dns: plane is not configured")
	// ErrUnreachable — the plane was called and did not answer.
	ErrUnreachable = errors.New("dns: plane unavailable")
)
