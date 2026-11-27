// Copyright © 2026 Hanzo AI. MIT License.

package kms

import (
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zap-proto/zip"
)

func forbidden(t *testing.T, err error, why string) {
	t.Helper()
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != 403 {
		t.Fatalf("%s: want 403, got %v", why, err)
	}
}

// The attestation decides. The custodian acts for any tenant it states and for
// its own brand when it states none; a tenant's statement may only narrow.
func TestAdmitScopesEveryReadByWhoTheKernelSaysIsCalling(t *testing.T) {
	acme := identity{Org: "acme", Account: "acme-api"}
	keeper := identity{Org: "hanzo", Platform: true}

	if err := admit("orgs/acme/db", "", acme, Read); err != nil {
		t.Fatalf("a tenant reads its own: %v", err)
	}
	if err := admit("orgs/acme/db", "acme", acme, Read); err != nil {
		t.Fatalf("a tenant stating itself: %v", err)
	}
	forbidden(t, admit("orgs/zeta/db", "", acme, Read), "a tenant reading another tenant")
	forbidden(t, admit("orgs/zeta/db", "zeta", acme, Read), "a tenant STATING another tenant")
	forbidden(t, admit("ingress/acme-seal", "", acme, Read), "a tenant reading the deployment's")

	if err := admit("ingress/acme-seal", "", keeper, Read); err != nil {
		t.Fatalf("the custodian reads the deployment's: %v", err)
	}
	if err := admit("orgs/hanzo/kms", "", keeper, Read); err != nil {
		t.Fatalf("the custodian reads its own brand's: %v", err)
	}
	// The custodian acting for X — the tenant its request or its job's For
	// names, never the ref (KMSPeer states nothing of its own) — reaches X and
	// nothing else.
	if err := admit("orgs/acme/db", "acme", keeper, Read); err != nil {
		t.Fatalf("the custodian acting for acme reads acme's: %v", err)
	}
	forbidden(t, admit("orgs/zeta/db", "acme", keeper, Read), "the custodian acting for acme reading zeta's")
	forbidden(t, admit("orgs/acme/db", "", keeper, Read), "the custodian reading a tenant's without acting for it")
	forbidden(t, admit("orgs/acme/db", "zeta", keeper, Read), "the custodian acting for zeta reading acme's")
	// The platform's own material is no exception: a read made for a member of
	// another org is refused it, and a subsystem that holds a platform
	// credential reads it acting for the platform (forge.Source.Client).
	forbidden(t, admit("orgs/hanzo/deploy/FORGE_TRACKER_TOKEN@prod", "webby-ai", keeper, Read), "the custodian acting for webby-ai reading the platform's")
	if err := admit("orgs/hanzo/deploy/FORGE_TRACKER_TOKEN@prod", "hanzo", keeper, Read); err != nil {
		t.Fatalf("the custodian acting for the platform reads the platform's: %v", err)
	}

	if err := admit("", "", keeper, Read); err == nil {
		t.Fatal("an empty ref is a bad request")
	}
}

// What the deployment seals on an org's behalf is its custodian's on the plane
// too. A tenant pod — the org's own code, which any member who may deploy can
// place there — reads only what its deployments are sealed for (a sync subtree)
// and writes none of it; the custodian, acting for the org, reads and writes all.
func TestAdmitKeepsACustodiedRefFromTheOrgsPods(t *testing.T) {
	acme := identity{Org: "acme", Account: "acme-api"}
	keeper := identity{Org: "hanzo", Platform: true}

	forbidden(t, admit("orgs/acme/tools-mcp/stripe-com", "", acme, Read), "a tenant pod reading its org's MCP credential")
	forbidden(t, admit("orgs/acme/kms-auth/clientSecret", "acme", acme, Read), "a tenant pod reading its org's sync credential")
	forbidden(t, admit("orgs/acme/environment/web/API_KEY", "", acme, Write), "a tenant pod planting a repository secret")
	if err := admit("orgs/acme/platform/shop/DATABASE_URL", "", acme, Read); err != nil {
		t.Fatalf("a tenant pod reads its own app's secret env: %v", err)
	}
	forbidden(t, admit("orgs/acme/platform/shop/DATABASE_URL", "", acme, Write), "a tenant pod rewriting an app's secret env")

	for _, v := range []Verb{Read, Write} {
		if err := admit("orgs/acme/tools-mcp/stripe-com", "acme", keeper, v); err != nil {
			t.Fatalf("the custodian acting for acme (%s): %v", v, err)
		}
	}
	forbidden(t, admit("orgs/acme/tools-mcp/stripe-com", "zeta", keeper, Read), "the custodian acting for zeta")
}

// place is the whole attestation: a process in this PID namespace running as
// this user or root, and a process whose cgroup names this pod, are the
// custodian; a tenant pod is its tenant; and everything else — another pod,
// including one in the custodian's own namespace, and any process the kernel
// cannot name — has no standing at all.
func TestPlaceNamesTheCustodianByItsProcessesNotItsNamespace(t *testing.T) {
	pod := func(uid, ns, sa string) corev1.Pod {
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns}, Spec: corev1.PodSpec{ServiceAccountName: sa}}
	}
	a := &attest{uid: 1000, self: "self-pod", pods: map[string]corev1.Pod{
		"self-pod":   pod("self-pod", "hanzo", "cloud"),
		"other-pod":  pod("other-pod", "hanzo", "cloud"),
		"tenant-pod": pod("tenant-pod", "tenant-acme", "acme-api"),
	}}

	for _, tc := range []struct {
		what string
		uid  int
		pid  int
		pod  string
	}{
		{"a plugin this process started, as this user", 1000, 4242, ""},
		{"a process in this PID namespace as root", 0, 4242, ""},
		{"a process whose cgroup names this pod", 4242, 4242, "self-pod"},
	} {
		who, err := a.place(tc.uid, tc.pid, tc.pod)
		if err != nil || !who.Platform {
			t.Fatalf("%s: want the custodian, got %+v %v", tc.what, who, err)
		}
	}

	if who, err := a.place(4242, 4242, "tenant-pod"); err != nil || who.Platform || who.Org != "acme" || who.Account != "acme-api" {
		t.Fatalf("a tenant pod: %+v %v", who, err)
	}

	_, err := a.place(4242, 4242, "other-pod")
	forbidden(t, err, "another pod in the custodian's namespace")
	if !strings.Contains(err.Error(), "pod hanzo/cloud has no standing on the internal plane") {
		t.Fatalf("the refusal names the pod: %v", err)
	}
	_, err = a.place(4242, 4242, "unknown-pod")
	forbidden(t, err, "a pod this node has not learned")
	_, err = a.place(1001, 4242, "")
	forbidden(t, err, "a process under another user")

	// Inside a container no cgroup names a pod, so the PID namespace is the
	// boundary. SO_PEERCRED reports pid 0 for a peer outside it — another pod on
	// this node that reached the socket through a shared volume, or a host
	// process — and neither is this process's, whatever user it runs as.
	_, err = a.place(0, 0, "")
	forbidden(t, err, "a root process outside this PID namespace")
	_, err = a.place(1000, 0, "")
	forbidden(t, err, "a same-uid process outside this PID namespace")
}

// Outside a pod, self is "" — and a pod UID never matches an empty self.
func TestAnEmptySelfMatchesNoPod(t *testing.T) {
	a := &attest{uid: 1000, pods: map[string]corev1.Pod{}}
	if _, err := a.place(1000, 4242, "some-pod"); err == nil {
		t.Fatal("a pod must not be the custodian because this process runs outside one")
	}
}

func TestFromPodReadsTenancyOffTheNamespace(t *testing.T) {
	if got, err := fromPod("tenant-acme", "acme-api"); err != nil || got.Org != "acme" || got.Platform || got.Account != "acme-api" {
		t.Fatalf("tenant pod: %+v %v", got, err)
	}
	_, err := fromPod("hanzo", "cloud")
	forbidden(t, err, "a pod in the deployment's namespace that is not this pod")
	_, err = fromPod("tenant-", "x")
	forbidden(t, err, "an empty tenant is not a tenant, and not the custodian either")
}

// The license signing seed lives under the brand org, and a pod in tenant-hanzo
// is the brand org's own code: it neither reads nor replaces the seed. The
// licensing plugin, a process of the custodian, reads it acting for hanzo.
func TestAdmitKeepsTheLicenseSigningKeyFromTheBrandsPods(t *testing.T) {
	const ref = "orgs/hanzo/engine/LICENSE_SIGNING_KEY@prod"
	pod := identity{Org: "hanzo", Account: "default"}
	keeper := identity{Org: "hanzo", Platform: true}

	for _, v := range []Verb{Read, Write} {
		forbidden(t, admit(ref, "", pod, v), "a tenant-hanzo pod on the license signing key ("+string(v)+")")
		forbidden(t, admit(ref, "hanzo", pod, v), "a tenant-hanzo pod stating hanzo ("+string(v)+")")
	}
	if err := admit(ref, "hanzo", keeper, Read); err != nil {
		t.Fatalf("the licensing plugin reads the key acting for hanzo: %v", err)
	}
}

// The platform's search-engine keys live under the reserved platform org. The
// websearch app — a process of the custodian — reads, replaces and removes them
// acting for admin, from a fresh context. On the request it is serving it would
// act for the searcher's org and be refused, and stating nothing acts for the
// brand org, which is refused too: that is why it states admin. No tenant pod
// reaches them, not even one in tenant-admin.
func TestAdmitKeepsTheSearchKeysForThePlatform(t *testing.T) {
	const ref = "orgs/admin/websearch/MOJEEK_KEY@prod"
	keeper := identity{Org: "hanzo", Platform: true}
	pod := identity{Org: "admin", Account: "default"}

	for _, v := range []Verb{Read, Write} {
		if err := admit(ref, "admin", keeper, v); err != nil {
			t.Fatalf("websearch acting for admin (%s): %v", v, err)
		}
		forbidden(t, admit(ref, "acme", keeper, v), "the custodian acting for the searcher's org ("+string(v)+")")
		forbidden(t, admit(ref, "", keeper, v), "the custodian stating nothing, which acts for the brand ("+string(v)+")")
		forbidden(t, admit(ref, "admin", pod, v), "a tenant-admin pod ("+string(v)+")")
	}
	if !Custodied(ref) {
		t.Fatal("the search keys are not custodied: an org member would read them over REST")
	}
}
