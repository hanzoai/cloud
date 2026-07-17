package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// mirror_control.go implements the cloud.GitMirrorController seam: the universal
// sync engine's git provider ENSURES or REMOVES a native repo's outbound mirror
// target through it, reusing the SAME repo_mirrors store + validateMirrorTarget
// gate the /mirror endpoint and mirror_out reactor use — one outbound target list,
// no second path. Registered in Mount via cloud.RegisterGitMirrorController, so the
// engine drives it with no syncsvc⇆git import cycle (the pushBuilder / GitImporter
// idiom).

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
		id, err := genID("mir")
		if err != nil {
			return err
		}
		if err := store.CreateMirror(ctx, MirrorTarget{
			ID: id, Org: org, Project: project, Repo: name,
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
