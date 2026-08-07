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

// platformOrg owns the images WE publish (the sandbox classes themselves). Any
// caller may name them: they are the same bytes `imageFor` would have chosen,
// and refusing them would mean a caller could not pin the class image they are
// already running.
const platformOrg = "hanzoai"

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
	if ns == platformOrg || ns == slug(org) {
		return nil
	}
	return fmt.Errorf(
		"image %q is in namespace %q on our registry; an org may name only its own images there (or %q)",
		image, ns, platformOrg)
}

func isOurs(host string) bool {
	for _, h := range ours {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}

// runtimes are the isolation boundaries a caller may ask for. It is a CLOSED
// set, not free text, because runtimeClassName is passed to the apiserver and an
// unknown value is a pod that never schedules — a caller typo would become a
// sandbox stuck Pending with no explanation.
//
// The empty string is the deployment's own default (SANDBOX_RUNTIME_CLASS), and
// it is what a caller naming nothing gets.
var runtimes = map[string]bool{"gvisor": true, "kata-fc": true, "kata-clh": true}

// checkRuntime refuses a runtime we do not run. Naming one that is not installed
// on any node is the same failure with a slower clock, so this is only half the
// check — the RuntimeClass has to exist in the cluster too, and the apiserver is
// the one that knows.
func checkRuntime(rc string) error {
	rc = strings.TrimSpace(rc)
	if rc == "" || runtimes[rc] {
		return nil
	}
	return fmt.Errorf("runtime %q is not one we run (gvisor, kata-fc, kata-clh)", rc)
}
