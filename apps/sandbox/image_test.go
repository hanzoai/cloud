package sandbox

// What image a class resolves to, pinned — because getting this wrong is silent.
//
// Two real defects motivate every case here. The tag ORDER was reversed: cloud
// asked for `dev-2026.6.7` while the registry held `2026.6.7-dev`, so the
// default path 404'd on an image sitting right there. And a single
// SANDBOX_IMAGE_DIGEST would have handed every class the same image, which
// looks correct in every log line it produces.

import (
	"strings"
	"testing"
)

func TestImageForResolvesTheTagThePublisherWrote(t *testing.T) {
	for _, c := range []struct {
		name, repo, tag, class, want string
		super                        bool
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
	}, {
		// WHO ASKS decides which bytes, for one class and one identity.
		name: "a SuperAdmin's dev sandbox runs the admin image",
		repo: "oci.hanzo.ai/hanzoai/sandbox", tag: "1.1.0", class: "dev", super: true,
		want: "oci.hanzo.ai/hanzoai/sandbox:1.1.0-admin",
	}, {
		// The substitution follows the BUILD: `admin` is layered on dev, so it
		// stands in for dev and for nothing else. Swapping exec would put a
		// bigger image behind every fifteen-minute tool call this identity makes.
		name: "a SuperAdmin's exec sandbox is the ordinary exec image",
		repo: "oci.hanzo.ai/hanzoai/sandbox", tag: "1.1.0", class: "exec", super: true,
		want: "oci.hanzo.ai/hanzoai/sandbox:1.1.0-exec",
	}, {
		// `admin` has no X server; desktop's whole reason for existing is one.
		name: "a SuperAdmin's desktop sandbox keeps its screen",
		repo: "oci.hanzo.ai/hanzoai/sandbox", tag: "1.1.0", class: "desktop", super: true,
		want: "oci.hanzo.ai/hanzoai/sandbox:1.1.0-desktop",
	}, {
		// The admin image is a fourth PUBLISHED image, so it pins like the other
		// three — by its own digest, under its own name. Reading _DEV here would
		// be the neighbour's-digest bug wearing a new class.
		name: "the admin image takes the admin digest, not dev's",
		repo: "oci.hanzo.ai/hanzoai/sandbox", tag: "1.1.0", class: "dev", super: true,
		digest: map[string]string{"DEV": "sha256:2baf7ede", "ADMIN": "sha256:9c1d0f42"},
		want:   "oci.hanzo.ai/hanzoai/sandbox@sha256:9c1d0f42",
	}} {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.digest {
				t.Setenv("SANDBOX_IMAGE_DIGEST_"+k, v)
			}
			r := &runtime{image: c.repo, tag: c.tag}
			if got := r.imageFor(c.class, c.super); got != c.want {
				t.Fatalf("imageFor(%q, super=%v) = %q, want %q", c.class, c.super, got, c.want)
			}
		})
	}
}

// NOBODY BUT A SUPERADMIN IS EVER HANDED THE ADMIN IMAGE, whatever else is set.
//
// The rule is one line in imageFor, which is exactly why it is worth an
// invariant rather than a case: a line that reads `super && class == "dev"` is
// one edit away from `super || class == "dev"`, and the resulting defect is
// invisible in every log — a tenant's sandbox that works, with three binaries in
// it that nobody there asked for and no error anywhere.
//
// The env matrix is the other half. `SANDBOX_IMAGE_TAG_ADMIN` and
// `SANDBOX_IMAGE_DIGEST_ADMIN` are read by name, so a deployment that pins the
// admin image must not thereby serve it to anyone: pinning WHICH bytes and
// deciding WHO gets them are two questions, and only one of them is a setting.
func TestOnlyASuperAdminIsHandedTheAdminImage(t *testing.T) {
	const repo = "oci.hanzo.ai/hanzoai/sandbox"
	t.Setenv("SANDBOX_IMAGE_TAG_ADMIN", "1.1.0")
	t.Setenv("SANDBOX_IMAGE_DIGEST_ADMIN", "sha256:9c1d0f42")
	for _, tag := range []string{"", "1.1.0"} {
		for _, class := range []string{"exec", "dev", "desktop"} {
			r := &runtime{image: repo, tag: tag}
			if got := r.imageFor(class, false); strings.Contains(got, "admin") {
				t.Fatalf("imageFor(%q, super=false) with tag %q = %q — an ordinary "+
					"caller was handed the operator's image", class, tag, got)
			}
		}
	}
}

// The bare tag can never come back, for ANY class, pinned or not.
//
// The table above proves the `dev` case with no tag. This is the same rule
// stated as an invariant over every combination, because the bare form is not
// one bad answer among many — it is the ONE spelling in this whole function
// that resolves to something in the registry which is not ours, and it can be
// reached by three different roads: an empty SANDBOX_IMAGE_TAG, an empty
// SANDBOX_IMAGE_TAG_<CLASS>, or a future edit that reorders the concatenation
// and drops a separator. A table checks the roads someone thought of.
//
// It has to be an assertion rather than a comment because the failure is
// SILENT. `<repo>:<class>` resolves today, so the wrong answer is a running pod
// rather than an ImagePullBackOff: a stock node:22 answers every exec by
// reading EOF and exiting 0, which is byte-for-byte what a command that
// succeeded and printed nothing looks like.
//
// bot's imageFor asserts the same invariant on its own side
// (src/gateway/coding-task.test.ts). Two consumers, one rule, written twice
// because a Go service and a TypeScript one share no code — only a registry.
func TestImageForNeverComposesTheBareClassTag(t *testing.T) {
	const repo = "oci.hanzo.ai/hanzoai/sandbox"
	for _, tag := range []string{"", "2026.6.7", "1.0.0"} {
		for _, class := range []string{"exec", "dev", "desktop"} {
			for _, super := range []bool{false, true} {
				r := &runtime{image: repo, tag: tag}
				got := r.imageFor(class, super)
				if got == repo+":"+class || got == repo+":admin" {
					t.Fatalf("imageFor(%q, super=%v) with tag %q = %q — nothing in the "+
						"fleet publishes that tag, so whatever answers it was written by hand",
						class, super, tag, got)
				}
			}
		}
	}
}
