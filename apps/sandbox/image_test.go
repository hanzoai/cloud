package sandbox

// What image a class resolves to, pinned — because getting this wrong is silent.
//
// Two real defects motivate every case here. The tag ORDER was reversed: cloud
// asked for `dev-2026.6.7` while the registry held `2026.6.7-dev`, so the
// default path 404'd on an image sitting right there. And a single
// SANDBOX_IMAGE_DIGEST would have handed every class the same image, which
// looks correct in every log line it produces.

import "testing"

func TestImageForResolvesTheTagThePublisherWrote(t *testing.T) {
	for _, c := range []struct {
		name, repo, tag, class, want string
		digest                       map[string]string
	}{{
		name: "version first, class second — the order CI publishes",
		repo: "oci.hanzo.ai/hanzoai/sandbox", tag: "2026.6.7", class: "dev",
		want: "oci.hanzo.ai/hanzoai/sandbox:2026.6.7-dev",
	}, {

		// A digest names BYTES. A version tag can be republished — hanzoai/bot
		// ships this image under bot's own package.json version, so a rebuild
		// today re-wrote 2026.6.7-dev from a commit two months newer.
		name: "a digest wins over any tag",
		repo: "oci.hanzo.ai/hanzoai/sandbox", tag: "2026.6.7", class: "dev",
		digest: map[string]string{"DEV": "sha256:2baf7ede"},
		want:   "oci.hanzo.ai/hanzoai/sandbox@sha256:2baf7ede",
	}, {
		// An unset tag must NOT resolve to a name that exists. The bare
		// `exec`/`dev`/`desktop` tags are real in the registry and all three
		// point at stock node:22 — no toolchain, no agent, running as ROOT — so
		// the old fallback turned one empty env var into a silent downgrade from
		// a hardened sandbox to a root shell. Nothing publishes `-unset`, so the
		// pull fails with a name that explains itself.
		name: "an unset tag fails loudly instead of booting the wrong image",
		repo: "oci.hanzo.ai/hanzoai/sandbox", class: "dev",
		want: "oci.hanzo.ai/hanzoai/sandbox:dev-unset",
	}, {
		// The bug this file caught before it shipped.
		name: "each class takes ITS OWN digest, never a neighbour's",
		repo: "oci.hanzo.ai/hanzoai/sandbox", tag: "2026.6.7", class: "exec",
		digest: map[string]string{"DEV": "sha256:2baf7ede"},
		want:   "oci.hanzo.ai/hanzoai/sandbox:2026.6.7-exec",
	}} {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.digest {
				t.Setenv("SANDBOX_IMAGE_DIGEST_"+k, v)
			}
			r := &runtime{image: c.repo, tag: c.tag}
			if got := r.imageFor(c.class); got != c.want {
				t.Fatalf("imageFor(%q) = %q, want %q", c.class, got, c.want)
			}
		})
	}
}
