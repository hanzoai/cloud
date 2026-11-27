package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/zap-proto/zip"
)

// leaseTTL is how long a routed run holds its login instance.
const leaseTTL = 15 * time.Minute

var errBusy = errors.New("source: login instance already leased")

// Lease takes the one live lease on (source, instance) for subject, after dropping
// any that has expired. A live one held by anyone is errBusy.
func (s *Store) Lease(ctx context.Context, subject, source, instance string, now time.Time) (string, time.Time, error) {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_lease WHERE expires_at<=$1`, now.Unix()); err != nil {
		return "", time.Time{}, fmt.Errorf("lease: %w", err)
	}
	var held string
	switch err := tx.QueryRowContext(ctx, `SELECT lease FROM source_lease WHERE source=$1 AND instance=$2`, source, instance).Scan(&held); {
	case err == nil:
		return "", time.Time{}, errBusy
	case !errors.Is(err, sql.ErrNoRows):
		return "", time.Time{}, fmt.Errorf("lease: %w", err)
	}
	lease, exp := mint.ID("lse"), now.Add(leaseTTL)
	if _, err := tx.ExecContext(ctx, `INSERT INTO source_lease (source, instance, lease, subject, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		source, instance, lease, subject, exp.Unix()); err != nil {
		return "", time.Time{}, fmt.Errorf("lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, fmt.Errorf("commit: %w", err)
	}
	return lease, exp, nil
}

// Release drops the subject's own lease on source. One that is not theirs, or is
// gone, is errNotFound.
func (s *Store) Release(ctx context.Context, subject, source, lease string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM source_lease WHERE source=$1 AND lease=$2 AND subject=$3`, source, lease, subject)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// SourceDispatch asks to run a session on a login of one of the caller's sources.
type SourceDispatch struct {
	// ID is the source, from the path.
	ID string `json:"id"`
	// Provider is the provider the login is for, for example `claude`.
	Provider string `json:"provider"`
	// Instance is the login's instance id on the source, for example `claude-1`.
	Instance string `json:"instance"`
}

// SourceRoute is where a granted dispatch runs.
type SourceRoute struct {
	// Zap is the source's ZAP endpoint, used exactly as given by the machine runtime.
	Zap string `json:"zap"`
	// Provider is the provider granted.
	Provider string `json:"provider"`
	// Instance is the login instance granted.
	Instance string `json:"instance"`
	// Lease is the hold on the instance for one run; release it when the run ends.
	Lease string `json:"lease"`
	// Expires is when the lease lapses by itself, RFC 3339 UTC. 15 minutes from the grant.
	Expires string `json:"expires"`
}

// SourceLease addresses one lease.
type SourceLease struct {
	// ID is the source, from the path.
	ID string `json:"id"`
	// Lease is the lease to release, from the path.
	Lease string `json:"lease"`
}

// SourceReleased acknowledges a release.
type SourceReleased struct {
	// Lease is the lease that was released.
	Lease string `json:"lease"`
	// Released is true.
	Released bool `json:"released"`
}

// refuse is a dispatch refusal: a status and a machine `code` the picker maps to its
// one-line reason. Codes: revoked, class, no_endpoint, offline, not_ready, not_shared,
// busy.
func refuse(status int, code, msg string) *zip.HTTPError {
	return &zip.HTTPError{Status: status, Code: code, Msg: msg}
}

// DispatchSource looks up where a session may run on one of the caller's own linked
// desktops and takes the lease for it; it is the lookup the /v1/dev execution field
// calls, and frames then flow from the browser to the desktop, not through cloud. It
// grants only when the caller owns the source (another person's is 404, with no
// admin or SuperAdmin path), the source is not revoked, is a desktop with a ZAP
// endpoint and a heartbeat in the last 90 seconds, the login instance is `ready`
// and shared, the provider's classes include `desktop`, and no live lease holds the
// instance (one run per login at a time, 15 minutes). Every grant and refusal is
// audited. A refusal carries `code`: revoked (410), class (403), no_endpoint,
// offline, not_ready, not_shared, busy (409).
//
// Example: {"id": "src_0123456789abcdef0123456789abcdef", "provider": "claude", "instance": "claude-1"}
func (o ops) dispatch(ctx context.Context, in *SourceDispatch) (*SourceRoute, error) {
	sto, _, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	return o.dispatchFor(ctx, sto, subject, in)
}

// dispatchFor enforces the dispatch policy and takes a lease for subject on sto.
// Both the public POST /v1/sources/:id/dispatch endpoint and the internal plane
// SourcesDispatch op call it so every caller obeys the exact same rules and audit.
func (o ops) dispatchFor(ctx context.Context, sto *Store, subject string, in *SourceDispatch) (*SourceRoute, error) {
	r, err := sto.Get(ctx, subject, in.ID)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("source not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	provider, instance := in.Provider, in.Instance
	var login *prov
	if instance == "" && provider != "" {
		// HIP-1350 allows execution {via:"desktop", desktop:"<id>", provider:"<instance id>"}.
		for i := range r.Providers {
			if p := r.Providers[i]; p.Instance == provider && p.Kind == "login" {
				login = &r.Providers[i]
				provider, instance = p.Provider, p.Instance
				break
			}
		}
		if login == nil {
			instance = provider
		}
	} else {
		for i := range r.Providers {
			if p := r.Providers[i]; p.Instance == instance && p.Provider == provider && p.Kind == "login" {
				login = &r.Providers[i]
			}
		}
	}
	now := o.s.State.now()
	deny := func(e *zip.HTTPError) (*SourceRoute, error) {
		o.recordWith(ctx, "source.dispatch", r.ID, "deny", e.Code, map[string]string{"provider": provider, "instance": instance})
		return nil, e
	}
	if r.RevokedAt != 0 {
		return deny(refuse(http.StatusGone, "revoked", "source revoked"))
	}
	if r.Kind != "desktop" || !has(classes(provider, "login", r.Kind), classDesktop) {
		return deny(refuse(http.StatusForbidden, "class", "this provider cannot be run remotely from this source"))
	}
	switch {
	case r.Zap == "":
		return deny(refuse(http.StatusConflict, "no_endpoint", "this source has no ZAP endpoint"))
	case r.LastSeen == 0 || now.Sub(time.Unix(r.LastSeen, 0)) > online:
		return deny(refuse(http.StatusConflict, "offline", "this source is offline"))
	case login == nil || login.State != "ready":
		return deny(refuse(http.StatusConflict, "not_ready", "this login is not ready on the source"))
	case !login.Shared:
		return deny(refuse(http.StatusConflict, "not_shared", "sharing is off for this login on the source"))
	}
	lease, exp, err := sto.Lease(ctx, subject, r.ID, instance, now)
	if errors.Is(err, errBusy) {
		return deny(refuse(http.StatusConflict, "busy", "a run is already using this login"))
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	o.recordWith(ctx, "source.dispatch", r.ID, "success", "", map[string]string{"provider": provider, "instance": instance, "lease": lease})
	return &SourceRoute{Zap: r.Zap, Provider: provider, Instance: instance, Lease: lease, Expires: exp.UTC().Format(time.RFC3339)}, nil
}

// ReleaseDispatch ends a run's hold on a login instance, so the next run can start
// before the lease lapses. Only the person who holds the lease can release it; any
// other lease or source is 404.
//
// Example: {"id": "src_0123456789abcdef0123456789abcdef", "lease": "lse_0123456789abcdef0123456789abcdef"}
func (o ops) release(ctx context.Context, in *SourceLease) (*SourceReleased, error) {
	sto, _, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	return o.releaseFor(ctx, sto, subject, in)
}

func (o ops) releaseFor(ctx context.Context, sto *Store, subject string, in *SourceLease) (*SourceReleased, error) {
	if err := sto.Release(ctx, subject, in.ID, in.Lease); errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("lease not found")
	} else if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	o.recordWith(ctx, "source.release", in.ID, "success", "", map[string]string{"lease": in.Lease})
	return &SourceReleased{Lease: in.Lease, Released: true}, nil
}
