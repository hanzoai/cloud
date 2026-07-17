// release.go — the first-party release seam. build.go's RegisterServiceReleaser is
// the inversion that lets a build-completion path (clients/platform/release.go, or
// any package-cloud caller) request a rollout with no cloud⇄paas import cycle.
//
// The App CRs in the platform namespaces are declared in universe git
// (infra/k8s/operator/crs/) and reconciled by Hanzo CD with selfHeal, so a direct
// spec.image patch is reverted on the next sync. releaseService therefore refuses
// and names the one way to roll a tag: commit it to the manifest. The clean-semver
// gate (splitReleaseImage) still validates the request so a caller gets an honest,
// specific error.
package paas

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
)

// splitReleaseImage splits a full image ref into (repository, tag) and enforces
// the clean-semver gate: the tag MUST be strict vX.Y.Z (IsSemverTag) — every
// mutable / sha / suffixed form (latest, main, sha-…, v1.2.3-billing, and even a
// bare 1.2.3 without the v) is refused, so a first-party service is never pinned
// to a non-deterministic tag. This is the exact determinism gate
// image-update.yml enforces at the CR-bump boundary, now native and in ONE place.
func splitReleaseImage(image string) (repository, tag string, err error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return "", "", fmt.Errorf("empty image ref")
	}
	repository = repoFromImageRef(image)
	tag = tagFromImageRef(image)
	if repository == "" || tag == "" {
		return "", "", fmt.Errorf("image %q must be a repository:tag reference", image)
	}
	if !imageRepoRE.MatchString(repository) {
		return "", "", fmt.Errorf("repository %q is not a valid image repository path", repository)
	}
	if !IsSemverTag(tag) {
		return "", "", fmt.Errorf("tag %q is not clean semver (vX.Y.Z) — refusing to pin", tag)
	}
	return repository, tag, nil
}

// releaseService refuses. The App CRs in the platform namespaces are declared in
// universe git (infra/k8s/operator/crs/) and reconciled by Hanzo CD with selfHeal,
// so a direct spec.image patch is reverted on the next sync — a release would look
// applied and then silently roll back. It validates the request (DNS-1123 service
// name, clean-semver image via splitReleaseImage), confirms the App exists across
// the platform namespaces in env order (main first — an honest error when it does
// not), and returns the refusal naming the one way to release it: commit the tag
// to the manifest. Returns the resolved namespace and semver tag for the caller's
// log; changed is always false.
func releaseService(s *cloud.Service[state], ctx context.Context, service, image string) (ns, tag string, changed bool, err error) {
	if e := ready(s); e != nil {
		return "", "", false, e
	}
	service = strings.ToLower(strings.TrimSpace(service))
	if !appNameRE.MatchString(service) {
		return "", "", false, fmt.Errorf("service %q must be a DNS-1123 label (the CR metadata.name)", service)
	}
	if _, tag, err = splitReleaseImage(image); err != nil {
		return "", "", false, err
	}
	if ns, err = resolveTarget(s, ctx, service); err != nil {
		return "", "", false, err
	}
	return ns, tag, false, fmt.Errorf(
		"service %q is declared in git (App/%s, universe infra/k8s/operator/crs/%s.yaml) and reconciled by Hanzo CD with selfHeal — a patch here would be reverted on the next sync; release it by committing the tag to that file",
		service, service, service)
}

// registerReleaser wires the first-party release seam (build.go's
// RegisterServiceReleaser inversion) to this mounted paas service. Called once
// from routes, so a release requested anywhere in the binary (cloud's own
// self-release, or a future in-process first-party builder) reaches the SAME
// refusal with no cloud⇄paas import cycle.
func registerReleaser(s *cloud.Service[state]) {
	cloud.RegisterServiceReleaser(func(ctx context.Context, ev cloud.ServiceReleaseEvent) error {
		_, _, _, err := releaseService(s, ctx, ev.Service, ev.Image)
		return err
	})
}
