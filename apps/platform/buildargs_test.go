package platform

// The `--build-arg`s an image declares.
//
// These exist because three `images:` entries off ONE Dockerfile, differing only
// by `args: {STAGE: exec|dev|desktop}`, were all built as the stage the
// Dockerfile happens to default to. Nothing failed: three tags were published,
// each a copy of the same image, under three names that said otherwise. An
// `exec` tag — the class that is supposed to be a minimal, volumeless
// interpreter box — carried a whole X server.

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestBuildArgsAreSortedSoOneCommitIsOneCacheKey(t *testing.T) {
	// A map has no order. Emitting in range order makes two builds of the SAME
	// commit two different argv, hence two cache keys and two digests — which is
	// the property the digest-pinned lane is built on.
	got, err := buildArgs(map[string]string{"ZULU": "1", "ALPHA": "2", "MIKE": "3"})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{
		"--opt", "build-arg:ALPHA=2",
		"--opt", "build-arg:MIKE=3",
		"--opt", "build-arg:ZULU=1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d opts, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("opt %d = %v, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildArgsRefusesWhatWouldSplitAnOpt(t *testing.T) {
	for name, args := range map[string]map[string]string{
		"a name carrying = is two opts": {"A=B": "x"},
		"a name that reads as a flag":   {"--opt": "x"},
		"an empty name":                 {"": "x"},
		"a name with a space":           {"A B": "x"},
		"a value carrying a newline":    {"A": "x\ny"},
		"a value carrying a NUL":        {"A": "x\x00y"},
		"a name starting with a digit":  {"1A": "x"},
	} {
		if _, err := buildArgs(args); err == nil {
			t.Errorf("%s: accepted %v, want refusal", name, args)
		}
	}
	// The ordinary case still passes — a refusal that refuses everything is not
	// a check, it is an outage.
	if _, err := buildArgs(map[string]string{"STAGE": "desktop", "NODE_MAJOR": "24"}); err != nil {
		t.Errorf("refused a plain declaration: %v", err)
	}
}

func TestDeclaredArgsCannotForgeTheReceipts(t *testing.T) {
	// VERSION and REVISION are derived from the tag and the commit. If a repo's
	// own hanzo.yml could set them, an image could name a commit it was not built
	// from — and those two values are exactly what "which of these is the
	// release" is answered with.
	cmd, err := buildFrontendCmdArgs(
		"https://github.com/hanzoai/bot.git#refs/heads/main", "Dockerfile.box",
		"oci.hanzo.ai/hanzoai/sandbox:1.0.1-dev", "fedeb0e5d0b3f7a9b171823a4f3ecf33686e5b1a",
		map[string]string{"STAGE": "dev", "VERSION": "9.9.9", "REVISION": "0000000000000000000000000000000000000000"},
	)
	if err != nil {
		t.Fatalf("buildFrontendCmdArgs: %v", err)
	}
	var got []string
	for _, a := range cmd {
		if s, ok := a.(string); ok && strings.HasPrefix(s, "build-arg:") {
			got = append(got, s)
		}
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "build-arg:STAGE=dev") {
		t.Errorf("STAGE was dropped; the class selector never reached the build: %v", got)
	}
	if strings.Contains(joined, "VERSION=9.9.9") {
		t.Errorf("a declared VERSION overwrote the tag-derived one: %v", got)
	}
	if strings.Contains(joined, "REVISION=0000000000000000000000000000000000000000") {
		t.Errorf("a declared REVISION overwrote the commit: %v", got)
	}
	if !strings.Contains(joined, "build-arg:REVISION=fedeb0e5d0b3f7a9b171823a4f3ecf33686e5b1a") {
		t.Errorf("the true commit is missing: %v", got)
	}
}

func TestOurOwnRegistryIsNameableByItsCanonicalHost(t *testing.T) {
	// oci.hanzo.ai and registry.hanzo.ai are ONE store. Refusing the canonical
	// name while accepting the deprecated alias is a rule about spelling, not
	// about reach.
	for _, image := range []string{
		"oci.hanzo.ai/hanzoai/sandbox:1.0.1-dev",
		"registry.hanzo.ai/hanzoai/sandbox:1.0.1-dev",
		"ghcr.io/hanzoai/cloud:v1",
	} {
		if !imageAllowed(image) {
			t.Errorf("imageAllowed(%q) = false, want true", image)
		}
	}
	// And the bound still holds: a host we do not operate, and a namespace we do
	// not own on a host we do.
	for _, image := range []string{
		"docker.io/library/node:22",
		"evil.example.com/hanzoai/sandbox:1",
		"oci.hanzo.ai/globex/private:v1",
	} {
		if imageAllowed(image) {
			t.Errorf("imageAllowed(%q) = true, want false", image)
		}
	}
}

// A build must not silently produce an image that cannot name itself.
//
// The runner stamps REVISION only for a full commit, which is right: a build
// context may name a BRANCH, and stamping "main" would make the label look
// populated while answering a different question. But a value that is hex and
// SHORT is neither — it is a caller who meant to pass a revision and passed a
// prefix, and it used to sail through and ship an image whose /v1/health answers
// `revision: unknown` forever. A fleet that cannot be asked which commit it runs
// is how a rollback goes unnoticed.
func TestABuildRefusesAnAbbreviatedRevision(t *testing.T) {
	const full = "8c8c58108e4b18a4c8b06d4b6a6f91e616a5f6cc"
	for _, c := range []struct {
		name, revision string
		refuse         bool
	}{
		{"a full commit is stamped", full, false},
		{"no revision is honest", "", false},
		{"a branch is a legitimate context", "main", false},
		{"a release branch is not hex", "release/2017", false},
		{"the 12-char tag shape is a mistake", full[:12], true},
		{"a 7-char prefix is a mistake", full[:7], true},
		{"one short of a commit is a mistake", full[:39], true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := buildFrontendCmdArgs("ctx.git#x", "Dockerfile", "ghcr.io/x/y:t", c.revision, nil)
			switch {
			case c.refuse && err == nil:
				t.Fatalf("revision %q was accepted; the image it builds cannot name itself", c.revision)
			case !c.refuse && err != nil:
				t.Fatalf("revision %q was refused: %v", c.revision, err)
			}
			if c.refuse && !strings.Contains(err.Error(), "full 40-character sha") {
				t.Errorf("refusal does not say how to fix it: %v", err)
			}
		})
	}
}

// And the full commit actually REACHES buildkit as a build-arg — the property the
// refusal exists to protect. Asserting the refusal alone would pass even if the
// stamp were dropped on the way.
func TestAFullRevisionIsStamped(t *testing.T) {
	const full = "8c8c58108e4b18a4c8b06d4b6a6f91e616a5f6cc"
	cmd, err := buildFrontendCmdArgs("ctx.git#x", "Dockerfile", "ghcr.io/x/y:t", full, nil)
	if err != nil {
		t.Fatalf("full commit refused: %v", err)
	}
	var flat []string
	for _, a := range cmd {
		flat = append(flat, fmt.Sprint(a))
	}
	if want := "build-arg:REVISION=" + full; !slices.Contains(flat, want) {
		t.Fatalf("the commit never reached buildkit: %v", flat)
	}
}
