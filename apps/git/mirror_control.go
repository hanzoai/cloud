package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

// mirror_control.go implements the cloud.GitMirrorController client: the universal
// sync engine's git provider ENSURES or REMOVES a native repo's outbound mirror
// target through it, reusing the SAME repo_mirrors store + validateMirrorTarget
// gate the /mirror endpoint and mirror_out reactor use — one outbound target list,
// no second path. Registered in Mount via cloud.RegisterGitMirrorController, so the
// engine drives it with no sync⇆git import cycle (the pushBuilder / GitImporter
// idiom).
//
// That client assumed co-residence. It is registered in THIS process, and the sync
// engine runs in its own, so over there the controller was nil and every mirror
// the engine decided on came back "git mirror controller not registered" — a sync
// that reconciled inbound forever and pushed nothing back, while the app holding
// the repos was healthy one socket away. exposeMirror below is the same control
// carried across that process boundary, onto the SAME EnsureMirror, so the two
// legs cannot drift apart.

type gitMirrorController struct{}

// EnsureMirror registers (enabled) or removes (disabled) the outbound mirror to url
// for (org, project, repo). Idempotent: enabling an already-present target is a
// no-op, disabling an absent one is a no-op. url is validated + canonicalized
// through validateMirrorTarget (https, no userinfo, outbound-target allowlist), so
// the engine can never register native pushes to an untrusted or internal host.
func (gitMirrorController) EnsureMirror(ctx context.Context, org, project, repo, url string, enabled bool) error {
	s := mounted.Load()
	if s == nil {
		return fmt.Errorf("git: not mounted")
	}
	name := normalizeName(repo)
	if !nameRE.MatchString(name) {
		return fmt.Errorf("git: invalid repo name")
	}
	canonical, host, err := validateMirrorTarget(url)
	if err != nil {
		return err
	}
	store, err := storeFor(s, org)
	if err != nil {
		return err
	}
	mirrors, err := store.ListMirrors(ctx, org, project, name)
	if err != nil {
		return err
	}
	var existing *MirrorTarget
	for i := range mirrors {
		if strings.EqualFold(mirrors[i].Host, host) {
			existing = &mirrors[i]
			break
		}
	}
	switch {
	case enabled && existing == nil:
		if err := store.CreateMirror(ctx, MirrorTarget{
			ID: mint.ID("mir"), Org: org, Project: project, Repo: name,
			Host: host, URL: canonical, CreatedAt: time.Now().Unix(),
		}); err != nil && !errors.Is(err, errConflict) {
			return err
		}
	case !enabled && existing != nil:
		if _, err := store.DeleteMirror(ctx, org, project, name, existing.ID); err != nil {
			return err
		}
	}
	return nil
}

// exposeMirror publishes the mirror declaration on the internal plane. Called
// from Mount, beside the other cross-app clients.
//
// The tenant comes from the CALLER, never the argument — which is why MirrorIn
// has no Org field to read. An argument org would let an engine acting for one
// tenant point another tenant's repo at a remote it controls.
func exposeMirror() {
	zip.Post[client.MirrorIn, client.Mirrored](cloud.Plane(), "/git/mirror", planeMirror,
		zip.WithOperationID(client.GitMirror),
		zip.WithSummary("Declare or remove a repo's outbound mirror target"))
}

// planeMirror registers (Enabled) or removes (!Enabled) one outbound mirror
// target on a repo of the CALLER's org, idempotently either way.
//
// It declares the target and nothing more: the pushing stays with the mirror_out
// reactor on the native push lifecycle, so a mirror that exists is a fact about
// this repo rather than a job somebody has to keep running. EnsureMirror is the
// same func the in-process controller exposes, so the URL crossing the plane
// passes the identical validateMirrorTarget gate — https, no userinfo, host on
// the outbound allowlist — and a remote caller cannot register a push to an
// internal host that a local one could not.
//
// The error is returned as it comes: a rejected URL is already an HTTPError(400)
// and survives the crossing whole, while a store failure carries no status and
// lands as the 500 it is. Wrapping both would turn the caller's own mistake into
// our fault.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeMirror(ctx context.Context, in *client.MirrorIn) (*client.Mirrored, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git mirror: org required")
	}
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(503, "git not mounted")
	}
	if in.Repo == "" {
		return nil, zip.ErrBadRequest("git mirror: repo is required")
	}
	if err := (gitMirrorController{}).EnsureMirror(ctx, who.Org, in.Project, in.Repo, in.URL, in.Enabled); err != nil {
		return nil, err
	}
	return &client.Mirrored{Repo: in.Repo}, nil
}
