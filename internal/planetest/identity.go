// Copyright © 2026 Hanzo AI. MIT License.

package planetest

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

var (
	servedMu sync.Mutex
	served   = map[string]*Identity{}
)

// Identity is an in-memory IAM peer that REMEMBERS what it is granted.
//
// The stateless peers above answer a fixed question. This one is for a caller
// that writes a grant and then reads it back — creating a space and finding
// its owner, inviting somebody and listing the roster — which a fixed answer
// cannot express without deciding the outcome in advance.
type Identity struct {
	mu sync.Mutex
	// Keyed by ORG, because a grant belongs to one tenant and every read is
	// scoped to the caller's. A flat list answers one org's question with
	// another's rows, which is the failure a roster read must never have.
	grants map[string][]plane.Membership
	names  map[string]string
}

// Named states the display name IAM holds for a person, which the roster reads
// back. Without it a member renders as their id — the name is IAM's, so a test
// that expects one has to say what IAM knows.
func (i *Identity) Named(user, name string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.names[user] = name
}

// Grants is every grant recorded, oldest first.
func (i *Identity) Grants() []plane.Membership {
	i.mu.Lock()
	defer i.mu.Unlock()
	var out []plane.Membership
	for _, g := range i.grants {
		out = append(out, g...)
	}
	return out
}

// ServeIdentity publishes the identity reads and the grant write on the plane.
//
// ONE peer per test, however many times it is called. A test that both seeds a
// store and mounts the subsystem reaches this twice, and a second listener takes
// the socket from the first — so the grants written before the mount vanish, and
// the read that follows reports an empty roster rather than an error.
func ServeIdentity(t *testing.T) *Identity {
	t.Helper()
	servedMu.Lock()
	defer servedMu.Unlock()
	if i, ok := served[t.Name()]; ok {
		return i
	}
	runtimeDir(t)
	i := &Identity{grants: map[string][]plane.Membership{}, names: map[string]string{}}
	served[t.Name()] = i
	t.Cleanup(func() {
		servedMu.Lock()
		delete(served, t.Name())
		servedMu.Unlock()
	})

	app := zip.New(zip.Config{AppName: "iam"})
	zip.Post[plane.GrantIn, struct{}](app, "/iam/grant",
		func(ctx context.Context, in *plane.GrantIn) (*struct{}, error) {
			org := zip.CallerOf(ctx).Org
			if org == "" {
				return nil, zip.ErrUnauthorized("grant: no org on the call")
			}
			i.mu.Lock()
			defer i.mu.Unlock()
			for _, g := range i.grants[org] {
				if g.User == in.User && g.Space == in.Space && g.Project == in.Project {
					return &struct{}{}, nil // never downgrade, matching EnsureMembershipIn
				}
			}
			i.grants[org] = append(i.grants[org], plane.Membership{
				User: in.User, Role: in.Role, Name: in.User,
				Space: in.Space, Project: in.Project,
			})
			return &struct{}{}, nil
		}, zip.WithOperationID(plane.IAMGrant))

	zip.Post[plane.Scope, plane.Memberships](app, "/iam/members",
		func(ctx context.Context, in *plane.Scope) (*plane.Memberships, error) {
			org := zip.CallerOf(ctx).Org
			if org == "" {
				return nil, zip.ErrUnauthorized("members: no org on the call")
			}
			i.mu.Lock()
			defer i.mu.Unlock()
			out := []plane.Membership{}
			for _, g := range i.grants[org] {
				if in != nil && in.User != "" && g.User != in.User {
					continue
				}
				if in != nil && !in.Any && (g.Space != in.Space || g.Project != in.Project) {
					continue
				}
				if n := i.names[g.User]; n != "" {
					g.Name = n
				}
				out = append(out, g)
			}
			sort.SliceStable(out, func(a, b int) bool { return out[a].User < out[b].User })
			return &plane.Memberships{Memberships: out}, nil
		}, zip.WithOperationID(plane.IAMMembers))

	zip.Post[struct{}, plane.Seats](app, "/iam/seats",
		func(ctx context.Context, _ *struct{}) (*plane.Seats, error) {
			org := zip.CallerOf(ctx).Org
			if org == "" {
				// Refusing matches the real handler. Answering 0 would let a caller
				// that lost its tenant read as an org with nobody in it, which bills
				// nothing and looks correct.
				return nil, zip.ErrUnauthorized("seats: no org on the call")
			}
			i.mu.Lock()
			defer i.mu.Unlock()
			full, guest := map[string]bool{}, map[string]bool{}
			for _, g := range i.grants[org] {
				if g.Role == "guest" {
					guest[g.User] = true
					continue
				}
				full[g.User] = true
			}
			for u := range guest {
				if full[u] {
					delete(guest, u)
				}
			}
			return &plane.Seats{Seats: len(full) + len(guest), Guests: len(guest)}, nil
		}, zip.WithOperationID(plane.IAMSeats))

	zip.Post[struct{}, plane.Roles](app, "/iam/roles",
		func(context.Context, *struct{}) (*plane.Roles, error) {
			return &plane.Roles{Roles: []string{"System Manager"}}, nil
		}, zip.WithOperationID(plane.IAMRoles))

	listen(t, app, "iam")
	return i
}
