package dns

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/internal/environ"
)

// Op names what a /v1/dns request asks for, read off the address once so no adapter
// has to parse a path. The four record and zone reads/writes are the vocabulary every
// DNS control plane has; OpPlane is everything else this surface carries (a zone
// create, the plane's own sync), which only a provider whose API IS this contract can
// answer.
type Op string

const (
	OpListZones    Op = "list_zones"
	OpListRecords  Op = "list_records"
	OpUpsertRecord Op = "upsert_record"
	OpDeleteRecord Op = "delete_record"
	OpPlane        Op = "plane"
)

// Call is one DNS request, normalized: the head has already validated the caller,
// scoped the path and lifted the zone and record out of it, so an adapter reads
// values and never a request. Method, Path, Query and Body are the caller's own,
// carried for a provider that speaks this contract natively.
type Call struct {
	Op          Op     // what the address asks for
	Org         string // the server-validated tenant, never a client header
	Bearer      string // the caller's OWN validated session bearer
	Zone        string // the zone name in the address, "" when the address names none
	Record      string // the record id in the address, "" when the address names none
	Method      string // the caller's HTTP method
	Path        string // the caller's path, normalized, always under /v1/dns
	Query       string // the caller's raw query string
	Body        []byte // the caller's request body, unread; valid for this call only
	ContentType string // the caller's Content-Type, "" when it sent none
}

// Answer is one DNS response, normalized: the status the provider gave, its body,
// and the two headers this surface carries back (its own Content-Type, and Location
// on a redirect that is relayed rather than followed).
type Answer struct {
	Status      int
	ContentType string
	Location    string
	Body        []byte
}

// The two conditions a provider reports rather than answers; the head turns them
// into this surface's 503 and 502. Everything else an adapter returns is its own
// error and reaches the caller as a 502 with no upstream detail.
var (
	// ErrUnconfigured — the provider has no endpoint or no credential to reach one.
	ErrUnconfigured = errors.New("dns: provider is not configured")
	// ErrUnreachable — the provider was called and did not answer.
	ErrUnreachable = errors.New("dns: provider unavailable")
	// errNoAddress — the address is not one this provider has. Only OpPlane can
	// raise it, and only at a provider that does not speak this contract natively.
	errNoAddress = errors.New("dns: provider has no such address")
)

// Provider is one DNS control plane this deployment answers /v1/dns from. Everything
// plane-specific is behind it: the endpoint, the wire shape, the credential. The head
// validates the caller, scopes the path, and hands over a Call — so an adapter reads
// no environment of its own beyond its own credentials, sees exactly one tenant per
// call, and can never widen the surface it was reached through.
//
// A new plane is a NEW FILE — a type, its four methods, and a register() in its
// init(). Nothing in this file, in dns.go, or in the route table changes.
type Provider interface {
	// ID is the stable slug HANZO_DNS_PROVIDER selects on ("hanzo", "cloudflare").
	ID() string
	// ListZones answers the zones the calling org holds.
	ListZones(ctx context.Context, c Call) (Answer, error)
	// ListRecords answers the records in c.Zone.
	ListRecords(ctx context.Context, c Call) (Answer, error)
	// UpsertRecord creates a record in c.Zone, or amends c.Record when the address
	// names one.
	UpsertRecord(ctx context.Context, c Call) (Answer, error)
	// DeleteRecord removes c.Record from c.Zone.
	DeleteRecord(ctx context.Context, c Call) (Answer, error)
}

// Relay is the capability of a provider whose OWN API is this surface's contract:
// it answers any address under /v1/dns, so a call the four operations do not name
// still reaches it. A provider that speaks its own wire shape does not implement
// this, and such a call is 404 — the honest answer, because that address does not
// exist at that plane. Optional, in the sense integrations' nil func fields are:
// declared here once, asked for by type assertion, never a method every adapter
// has to write.
type Relay interface {
	Relay(ctx context.Context, c Call) (Answer, error)
}

// providers is populated by each adapter file's register() from its init(). Go
// initializes this map before any init() runs, so every adapter is present by the
// time Mount reads it. The value is a CONSTRUCTOR, not an instance: an adapter reads
// its endpoint and credentials when the subsystem mounts, so an operator who changes
// them changes them without a rebuild.
var providers = map[string]func() Provider{}

// register adds an adapter. A blank or duplicate id is a programming error and panics
// at init — two planes cannot own one slug.
func register(id string, build func() Provider) {
	if id == "" || build == nil {
		panic("dns: register blank id or nil constructor")
	}
	if _, dup := providers[id]; dup {
		panic("dns: duplicate provider id " + id)
	}
	providers[id] = build
}

// defaultProvider is the plane a deployment gets when it names none: Hanzo's own.
const defaultProvider = "hanzo"

// selected builds the provider this deployment serves /v1/dns from, named by
// HANZO_DNS_PROVIDER and defaulting to Hanzo's own plane. A name no adapter
// registered is refused HERE, at mount, rather than at the first request.
func selected() (Provider, error) {
	id := environ.Or("HANZO_DNS_PROVIDER", defaultProvider)
	build, ok := providers[id]
	if !ok {
		return nil, errors.New("dns: no provider registered as " + id +
			" (have: " + strings.Join(slices.Sorted(maps.Keys(providers)), ", ") + ")")
	}
	return build(), nil
}

// classify reads the address once: which operation it names, and the zone and record
// it names them on. It is pure over (method, path) so the table below is the whole
// truth about how this surface maps onto a provider.
//
//	/zones                          GET    → OpListZones
//	/zones/{zone}/records           GET    → OpListRecords
//	/zones/{zone}/records           POST   → OpUpsertRecord
//	/zones/{zone}/records/{record}  PUT    → OpUpsertRecord
//	                                PATCH  → OpUpsertRecord
//	                                DELETE → OpDeleteRecord
//	anything else                          → OpPlane
func classify(method, path string) Call {
	c := Call{Op: OpPlane, Method: method, Path: path}
	rest := strings.Trim(strings.TrimPrefix(path, "/v1/dns"), "/")
	if rest == "" {
		return c
	}
	seg := strings.Split(rest, "/")
	if seg[0] != "zones" {
		return c
	}
	switch len(seg) {
	case 1: // /zones
		if method == http.MethodGet {
			c.Op = OpListZones
		}
	case 3: // /zones/{zone}/records
		if seg[2] != "records" {
			return c
		}
		c.Zone = seg[1]
		switch method {
		case http.MethodGet:
			c.Op = OpListRecords
		case http.MethodPost:
			c.Op = OpUpsertRecord
		}
	case 4: // /zones/{zone}/records/{record}
		if seg[2] != "records" {
			return c
		}
		c.Zone, c.Record = seg[1], seg[3]
		switch method {
		case http.MethodPut, http.MethodPatch:
			c.Op = OpUpsertRecord
		case http.MethodDelete:
			c.Op = OpDeleteRecord
		}
	}
	return c
}

// answer dispatches one classified Call at the mounted provider. OpPlane reaches a
// provider only when it declares Relay; a plane that speaks its own wire shape has
// no such address, and 404 says so.
func answer(ctx context.Context, p Provider, c Call) (Answer, error) {
	switch c.Op {
	case OpListZones:
		return p.ListZones(ctx, c)
	case OpListRecords:
		return p.ListRecords(ctx, c)
	case OpUpsertRecord:
		return p.UpsertRecord(ctx, c)
	case OpDeleteRecord:
		return p.DeleteRecord(ctx, c)
	}
	if r, ok := p.(Relay); ok {
		return r.Relay(ctx, c)
	}
	return Answer{}, errNoAddress
}
