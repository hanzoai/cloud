package sandbox

// cred_test.go — THE HALF THAT WOULD BE A P0.
//
// One identity's pod is now handed that identity's own DigitalOcean credentials.
// The whole safety of that rests on a single predicate, and a predicate is one
// character away from being wrong in a way nothing else notices: `super && class
// == "dev"` becomes `super || class == "dev"` and every sandbox in the fleet — a
// tool call running a model's code, a stranger's coding session — boots holding a
// token that can delete every cluster we have. The pod would still run. The tests
// would still pass. Nobody would look.
//
// So the assertion here is not "the admin path works". It is "no other path gets
// ANYTHING", stated against the object that actually reaches Kubernetes, for
// every class and both answers to `super`. Same shape as trust_test.go and for
// the same reason: the interesting case is the one that must never happen.

import (
	"fmt"
	"strings"
	"testing"
)

// podWith renders the object this package would actually send to the apiserver,
// and hands back both halves a reader cares about. Rendering the real thing is
// the point — a test against the map literal in podSpec would pass while the
// container it built carried something else.
func podWith(t *testing.T, class string, cr cred) (spec, container map[string]any) {
	t.Helper()
	r := &runtime{ns: "hanzo-sandboxes", image: "oci.hanzo.ai/hanzoai/sandbox"}
	u := r.podSpec(Sandbox{ID: "m_1", Class: class, Pod: "m-1", Org: "acme", Image: "img"}, cr)
	spec, ok := u.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no spec", class)
	}
	cs, ok := spec["containers"].([]any)
	if !ok || len(cs) != 1 {
		t.Fatalf("%s: want one container, got %v", class, spec["containers"])
	}
	container, ok = cs[0].(map[string]any)
	if !ok {
		t.Fatalf("%s: container is not an object", class)
	}
	return spec, container
}

// adminCred is a stand-in for what credFor returns, with values that are
// unmistakable in a rendered object. It never reaches DigitalOcean: what is under
// test is who the value is GIVEN to, which is decided here and not there.
func adminCred() cred {
	return cred{
		env: map[string]string{
			"DIGITALOCEAN_ACCESS_TOKEN": "dop_v1_TESTTOKEN",
			"DO_API_TOKEN":              "dop_v1_TESTTOKEN",
			"KUBECONFIG":                kubePath,
		},
		kube: []byte("apiVersion: v1\nkind: Config\nusers:\n- user:\n    token: TESTKUBETOKEN\n"),
	}
}

// TestOnlyASuperAdminsDevSandboxIsAdmin is the predicate itself, exhaustively.
// One function answers both which image a lease boots and which credentials it
// holds, so this is the whole authorization decision for the feature, and it is
// eight cases rather than an argument.
func TestOnlyASuperAdminsDevSandboxIsAdmin(t *testing.T) {
	for _, c := range []struct {
		class string
		super bool
		want  bool
	}{
		{"dev", true, true},
		{"dev", false, false},
		{"exec", true, false},
		{"exec", false, false},
		{"desktop", true, false},
		{"desktop", false, false},
		// A class this package does not serve is not admin either. Lease refuses
		// these before the predicate is ever asked, but a predicate that answers
		// true for a word nobody validated is one refactor from being the only
		// check left.
		{"", true, false},
		{"admin", true, false},
	} {
		if got := admin(c.class, c.super); got != c.want {
			t.Fatalf("admin(%q, super=%v) = %v, want %v", c.class, c.super, got, c.want)
		}
	}
}

// TestAnOrdinarySandboxIsHandedNothing is the assertion that matters.
//
// Every lease but a SuperAdmin's own carries the zero cred, and a zero cred must
// produce a container with NO env key at all — absent, not present-and-empty,
// because a present key is a place for a value to appear later without anyone
// editing this test.
func TestAnOrdinarySandboxIsHandedNothing(t *testing.T) {
	for _, class := range []string{"exec", "dev", "desktop", "android"} {
		_, c := podWith(t, class, cred{})
		if env, stated := c["env"]; stated {
			t.Fatalf("%s: an ordinary sandbox states env %v — every sandbox but a "+
				"SuperAdmin's own must be handed nothing", class, env)
		}
	}
}

// TestNoCredentialSurvivesAFalseSuper walks the path a request actually takes —
// predicate, then image, then pod — with super=false, and refuses to find any
// trace of the admin arrangement in any of the three.
//
// The three are checked TOGETHER on purpose. Each is individually covered
// elsewhere; what this states is that they agree, because the failure that would
// hurt is one of them saying yes while the others say no.
func TestNoCredentialSurvivesAFalseSuper(t *testing.T) {
	r := &runtime{ns: "hanzo-sandboxes", image: "oci.hanzo.ai/hanzoai/sandbox", tag: "1.1.0"}
	for _, class := range []string{"exec", "dev", "desktop", "android"} {
		if admin(class, false) {
			t.Fatalf("%s: admin(super=false) is true", class)
		}
		if img := r.imageFor(class, false); strings.Contains(img, "admin") {
			t.Fatalf("%s: a non-super lease resolved the admin image %q", class, img)
		}
		// And the belt-and-braces case: even if a cred were somehow built for a
		// non-super lease, nothing in Lease would pass it — but assert the shape
		// the caller relies on rather than the accident that protects it.
		_, c := podWith(t, class, cred{})
		if _, stated := c["env"]; stated {
			t.Fatalf("%s: non-super pod states env", class)
		}
	}
}

// TestASuperAdminsSandboxCarriesItsOwnCredentials is the other direction: the
// feature has to actually work, and it has to put each value where its tool looks
// — the token in the environment because that is where doctl reads it, KUBECONFIG
// naming a path because kubectl reads a file and no env var carries one.
func TestASuperAdminsSandboxCarriesItsOwnCredentials(t *testing.T) {
	_, c := podWith(t, "dev", adminCred())
	env, ok := c["env"].([]any)
	if !ok {
		t.Fatalf("a SuperAdmin's dev sandbox states no env: %v", c["env"])
	}
	got := map[string]string{}
	for _, e := range env {
		kv, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("env entry is not an object: %v", e)
		}
		got[kv["name"].(string)] = kv["value"].(string)
	}
	for k, want := range adminCred().env {
		if got[k] != want {
			t.Fatalf("env %s = %q, want %q", k, got[k], want)
		}
	}
	if got["KUBECONFIG"] != kubePath {
		t.Fatalf("KUBECONFIG = %q, want the path start writes to (%q)", got["KUBECONFIG"], kubePath)
	}
	// Sorted, so the spec of one sandbox is the same bytes every time it is built
	// and a diff between two pods means something.
	var names []string
	for _, e := range env {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("env is unordered: %v", names)
		}
	}
}

// TestTheKubeconfigIsNeverInAKubernetesObject states the design decision as a
// property. The DOKS kubeconfig is cluster-admin; it reaches the pod through the
// exec channel and must appear NOWHERE in the object the apiserver stores — not
// in env, not in a volume, not in a command line.
func TestTheKubeconfigIsNeverInAKubernetesObject(t *testing.T) {
	cr := adminCred()
	spec, _ := podWith(t, "dev", cr)
	rendered := renderedText(t, spec)
	if strings.Contains(rendered, "TESTKUBETOKEN") {
		t.Fatal("the kubeconfig appears in the pod object — it must reach the pod " +
			"through the exec channel and live in no Kubernetes object")
	}
	// It is the file start writes, so it must be reachable through the same
	// mechanism the fs verb uses, at a path outside the project volume (a PVC
	// outlives the lease; the credential must not).
	if strings.HasPrefix(kubePath, workdir) || strings.HasPrefix(kubePath, execdir) {
		t.Fatalf("kubePath %q is under a mounted workdir — a project PVC outlives "+
			"the lease and would keep the credential", kubePath)
	}
}

// TestAdminSandboxKeepsEveryOtherRefusal is the reconciliation. Handing one pod a
// credential must not have relaxed anything else about it: no Kubernetes-minted
// service-account token, no ambient service map, non-root, no privilege
// escalation, no capabilities.
func TestAdminSandboxKeepsEveryOtherRefusal(t *testing.T) {
	for _, cr := range []cred{{}, adminCred()} {
		spec, c := podWith(t, "dev", cr)
		if spec["automountServiceAccountToken"] != false {
			t.Fatalf("automountServiceAccountToken = %v, want false — a token minted "+
				"by Kubernetes is refused for every pod, including this one",
				spec["automountServiceAccountToken"])
		}
		if spec["enableServiceLinks"] != false {
			t.Fatalf("enableServiceLinks = %v, want false", spec["enableServiceLinks"])
		}
		sc, ok := spec["securityContext"].(map[string]any)
		if !ok || sc["runAsNonRoot"] != true || sc["runAsUser"] != int64(1000) {
			t.Fatalf("pod securityContext relaxed: %v", spec["securityContext"])
		}
		csc, ok := c["securityContext"].(map[string]any)
		if !ok || csc["allowPrivilegeEscalation"] != false {
			t.Fatalf("container securityContext relaxed: %v", c["securityContext"])
		}
		// No projected token by another name, either.
		if strings.Contains(renderedText(t, spec), "serviceAccountToken") {
			t.Fatal("the pod projects a service-account token")
		}
	}
}

// renderedText flattens the object to text so a test can ask whether a value
// appears ANYWHERE in it, rather than in the one field the author thought of.
func renderedText(t *testing.T, v any) string {
	t.Helper()
	var b strings.Builder
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			for k, vv := range t {
				b.WriteString(k)
				b.WriteByte('\n')
				walk(vv)
			}
		case []any:
			for _, vv := range t {
				walk(vv)
			}
		default:
			b.WriteString(fmt.Sprint(x))
			b.WriteByte('\n')
		}
	}
	walk(v)
	return b.String()
}
