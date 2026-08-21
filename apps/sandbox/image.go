package sandbox

// Which images a caller may name, and which runtime may run them.
//
// BOTH of these are caller-supplied fields on create, and both were previously
// taken on trust. The image one is a cross-tenant read: the pull secret on the
// `sandbox` ServiceAccount is OURS and fleet-wide, so a caller naming
// `oci.hanzo.ai/<someone-else>/private` had our credential fetch another org's
// private image for them. Nothing in the request was forged — the field was
// simply never checked.
//
// The rule is the one the registry already uses: our registry is org-namespaced
// as `<host>/<org>/<app>`, so on OUR hosts the first path segment must BE the
// caller's org. Everywhere else needs no credential of ours, so it needs no
// permission from us either — a public image is the caller's own business, and
// the pod's securityContext (non-root uid, no service-account token, caps
// dropped) is what contains it either way.

import (
	"fmt"
	"strings"
)

// ours are the registry hosts whose pull credential we supply. An image on one
// of these is fetched with OUR secret, which is exactly why the namespace has to
// be checked. Hosts not listed here are pulled anonymously or not at all.
var ours = []string{"oci.hanzo.ai", "registry.hanzo.ai"}

// hanzoai is the namespace on those hosts that holds the images WE publish (the
// sandbox classes themselves). Any caller may name one: they are the same bytes
// `imageFor` would have chosen, and refusing them would mean a caller could not
// pin the class image they are already running.
//
// It is named for the value it is, because it is a REGISTRY namespace and not an
// org id — the same company is `hanzo` to IAM (apps/taxonomy) and `platform` is a
// third string again, the deployment's own store partition (cloud.Reserved). A
// name like "the platform org" would read as any of the three, and repointing
// this one at another of them opens somebody else's private images to our pull
// credential.
const hanzoai = "hanzoai"

// checkImage refuses a caller-supplied image that would spend our pull
// credential on somebody else's namespace. An empty image is not a request, so
// it is not an error — the caller gets the class default.
func checkImage(org, image string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil
	}
	host, rest, ok := strings.Cut(image, "/")
	if !ok {
		// No slash means a Docker Hub library image (`node:22`). Public, no
		// credential of ours, nothing to check.
		return nil
	}
	if !isOurs(host) {
		return nil
	}
	ns, _, _ := strings.Cut(rest, "/")
	if ns == hanzoai || ns == slug(org) {
		return nil
	}
	return fmt.Errorf(
		"image %q is in namespace %q on our registry; an org may name only its own images there (or %q)",
		image, ns, hanzoai)
}

func isOurs(host string) bool {
	for _, h := range ours {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}

// The runtime a caller may ask for is checked in runtime.go, beside the table
// that says what each runtime can do — see runtimeFor. It used to be a second
// half-check here (is the name in the set?) with the real decision elsewhere,
// which is how a caller could name a runtime that exists and still get a
// sandbox that silently dropped its volume.
