package manifest

// A prefix has ONE owner. Two apps naming the same one is not a routing
// preference the host resolves — it is a program with no routing table, and zip
// says so by panicking at Start:
//
//	panic: zip: this program does not compose, so it has no projection
//
// That is the right answer. What was wrong is that nothing said it until a pod
// was already crash-looping in production: `/v1/tags` was added to `projects`
// while `destinations` still named it, every unit test passed, the image built
// green, and the failure arrived as a startup probe timeout on a live rollout.
//
// The existing compose test does not catch this. It composes the HOST, and a
// duplicated prefix only bites when Start reaches for the plugin that owns it —
// so a program that cannot boot passed the test that exists to prove it can.
// This one reads the manifest directly, which is where the fact lives.

import "testing"

func TestOnePrefixOneOwner(t *testing.T) {
	owner := map[string]string{}
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			if first, taken := owner[p]; taken {
				t.Errorf("%q is claimed by both %s and %s — a prefix has one owner, "+
					"and a host that cannot route it panics at Start rather than picking",
					p, first, a.Name)
				continue
			}
			owner[p] = a.Name
		}
	}
}
