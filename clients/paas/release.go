// release.go — the native first-party release seam: roll a proven, clean-semver
// image onto its operator hanzo.ai/v1 Service CR by patching spec.image, so the
// operator reconciles the Deployment rollout.
//
// This is the in-cluster, direct-CR replacement for universe's image-update.yml
// GitOps hop (a service CI fires repository_dispatch → a PR bumps the CR in git →
// ArgoCD syncs → the operator reconciles). Same determinism — clean-semver only,
// resolve the CR by metadata.name — but with NO round-trip through GitHub/ArgoCD:
// cloud patches the live CR directly and the operator does the rest. It is the
// keystone that makes push→build→image→live automatic.
//
// clients/paas already OWNS the first-party Service CR control plane (the drift
// board + the /v1/paas .../deploy patch), so the release primitive lives here;
// build.go's RegisterServiceReleaser is the inversion seam that lets a
// build-completion path (clients/platform/release.go, or any package-cloud caller)
// trigger a rollout with no cloud⇄paas import cycle.
package paas

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
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

// releaseService rolls image onto the first-party Service CR named `service` by
// merge-patching ONLY spec.image (repository + tag + pullPolicy=Always). It:
//
//   - resolves the CR by metadata.name across the platform namespaces in env order
//     (main first — a bare release targets production), the SAME resolution
//     image-update.yml uses (service name ⇒ CR name, no hardcoded map);
//   - enforces the clean-semver gate (splitReleaseImage); and
//   - is IDEMPOTENT: a CR already declaring this exact (repository, tag) is a no-op
//     (changed=false), so re-firing a release never churns the operator.
//
// The operator reconciles the Deployment rollout; cloud never reimplements a
// deployer. Returns the namespace it patched, the resolved semver tag, and whether
// the CR actually changed. A `service` that is not a DNS-1123 label, an image that
// fails the gate, or a service with no operator CR is an honest error (no patch).
func releaseService(s *cloud.Service[state], ctx context.Context, service, image string) (ns, tag string, changed bool, err error) {
	if e := ready(s); e != nil {
		return "", "", false, e
	}
	service = strings.ToLower(strings.TrimSpace(service))
	if !appNameRE.MatchString(service) {
		return "", "", false, fmt.Errorf("service %q must be a DNS-1123 label (the CR metadata.name)", service)
	}
	repository, tag, err := splitReleaseImage(image)
	if err != nil {
		return "", "", false, err
	}
	ns, err = resolveNamespace(s, ctx, service)
	if err != nil {
		return "", "", false, err
	}

	// Idempotency: skip the patch (and the operator churn) when the CR already
	// declares this exact image. A get error other than the terminal states falls
	// through to the patch, which is itself the source of truth.
	if cur, gErr := s.State.dyn.Resource(servicesGVR).Namespace(ns).Get(ctx, service, metav1.GetOptions{}); gErr == nil {
		curRepo, _, _ := unstructured.NestedString(cur.Object, "spec", "image", "repository")
		curTag, _, _ := unstructured.NestedString(cur.Object, "spec", "image", "tag")
		if curRepo == repository && curTag == tag {
			return ns, tag, false, nil
		}
	}

	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"image": map[string]any{
		"repository": repository,
		"tag":        tag,
		"pullPolicy": "Always",
	}}})
	if _, e := s.State.dyn.Resource(servicesGVR).Namespace(ns).Patch(ctx, service, k8stypes.MergePatchType, patch, metav1.PatchOptions{}); e != nil {
		if apierrors.IsNotFound(e) {
			return ns, tag, false, fmt.Errorf("service %q has no operator CR in namespace %s", service, ns)
		}
		return ns, tag, false, k8sErr(s, "patch", e)
	}
	s.Log.Info("released via operator Service CR patch (native, no ArgoCD)",
		"service", service, "namespace", ns, "repository", repository, "tag", tag)
	return ns, tag, true, nil
}

// registerReleaser wires the native first-party release seam (build.go's
// RegisterServiceReleaser inversion) to this mounted paas service. Called once
// from routes, so a proven image produced anywhere in the binary (cloud's own
// self-release, or a future in-process first-party builder) rolls live through the
// SAME CR-patch primitive with no cloud⇄paas import cycle.
func registerReleaser(s *cloud.Service[state]) {
	cloud.RegisterServiceReleaser(func(ctx context.Context, ev cloud.ServiceReleaseEvent) error {
		_, _, _, err := releaseService(s, ctx, ev.Service, ev.Image)
		return err
	})
}
