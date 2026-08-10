package cloud_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// recordedVersion is the SEMVER shape in .hanzo/scripts/orphans.sh, kept
// character-for-character: a record the script cannot match is a line that
// excuses no image while looking like it does.
var recordedVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// The release invariant hanzo.yml states — "main push → build → smoke → tag →
// pin → prove live" — had no test, and neither did the script that now enforces
// it. That is the shape of the original bug one level up: image-revision.sh
// verifies a property nothing verifies about IT, so a verifier that silently
// stopped verifying would look exactly like a passing build.
//
// These cases differ ONLY in whether a receipt exists for the published image.
// Same registry listing every time; the tag list and the recorded-orphans file
// are what move. If the script ever stops discriminating between those inputs it
// fails here, which is the one thing a gate can be tested for: that it is still
// capable of saying no.
//
// It runs offline. orphans.sh reads its two sources from ORPHANS_IMAGES and
// ORPHANS_TAGS when they name files, so no registry, no token and no network are
// involved — the comparison is exercised, not the transport.

// theRefusal is the exact text a published image with no receipt must produce.
// Asserted verbatim: a gate whose message drifts is a gate whose output nobody
// can grep for, and "it exited non-zero" does not tell an operator which image
// is unreconstructable or what to do about it.
const theRefusal = "::error::v1.801.480 is published but no git tag names it — an image no commit can be traced to is not a release. Tag it at the commit it was built from, or record it in .hanzo/orphans.txt with the reason it cannot be."

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// runOrphans execs the real script and reports its exit code and combined
// output. It does NOT pipe: a pipeline reports the LAST command's status, so
// `orphans.sh | tail` would report tail's success and read as green no matter
// what the gate decided. cmd.Run's error carries the script's own code.
func runOrphans(t *testing.T, env map[string]string) (int, string) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(".hanzo", "scripts", "orphans.sh"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("orphans.sh not found at %s: %v", script, err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "ORPHANS_IMAGE_PATH=hanzoai/cloud")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run orphans.sh: %v", err)
	}
	return code, string(out)
}

// published is the registry listing used by every case below — three versions,
// one of which (480) is the image that actually served production untagged.
const published = "v1.801.477\nv1.801.479\nv1.801.480\n"

func TestOrphansRefusesAPublishedImageWithNoTag(t *testing.T) {
	dir := t.TempDir()
	images := write(t, dir, "images", published)
	// 480 is missing from the receipts — exactly the state the registry and the
	// forge were in when this was measured.
	tags := write(t, dir, "tags", "v1.801.477\nv1.801.479\n")
	accepted := write(t, dir, "accepted", "# nothing recorded\n")

	code, out := runOrphans(t, map[string]string{
		"ORPHANS_IMAGES": images, "ORPHANS_TAGS": tags, "ORPHANS_ACCEPTED": accepted,
	})
	if code != 1 {
		t.Fatalf("an untagged published image must be RED (exit 1), got exit %d\n%s", code, out)
	}
	if !strings.Contains(out, theRefusal) {
		t.Fatalf("refusal text drifted.\nwant: %s\ngot:\n%s", theRefusal, out)
	}
	// The two images that DO have receipts must not be dragged in with it.
	for _, quiet := range []string{"v1.801.477", "v1.801.479"} {
		if strings.Contains(out, quiet+" is published but no git tag") {
			t.Fatalf("%s has a tag and must not be reported: \n%s", quiet, out)
		}
	}
}

// THE MUTATION. Identical to the case above but for one line in the tag list.
// If this passes while the case above also passes, the gate is reading the thing
// it claims to read; if both pass with the same verdict, it is decoration.
func TestOrphansPassesWhenTheTagExists(t *testing.T) {
	dir := t.TempDir()
	images := write(t, dir, "images", published)
	tags := write(t, dir, "tags", "v1.801.477\nv1.801.479\nv1.801.480\n")
	accepted := write(t, dir, "accepted", "# nothing recorded\n")

	code, out := runOrphans(t, map[string]string{
		"ORPHANS_IMAGES": images, "ORPHANS_TAGS": tags, "ORPHANS_ACCEPTED": accepted,
	})
	if code != 0 {
		t.Fatalf("every published image has a receipt — must be GREEN (exit 0), got exit %d\n%s", code, out)
	}
	if strings.Contains(out, "::error::") {
		t.Fatalf("green run emitted an error line:\n%s", out)
	}
}

// A recorded orphan is green — otherwise the gate is red from the day it lands,
// for images that already exist and cannot be fixed, and a permanently red gate
// is one somebody deletes. Recording is a reviewable line of data, not a code
// path, so the rule above stays a single rule.
func TestOrphansAcceptsARecordedOrphan(t *testing.T) {
	dir := t.TempDir()
	images := write(t, dir, "images", published)
	tags := write(t, dir, "tags", "v1.801.477\nv1.801.479\n")
	accepted := write(t, dir, "accepted",
		"# v1.801.480 served production; its revision label reads \"unknown\".\nv1.801.480  unreconstructable\n")

	code, out := runOrphans(t, map[string]string{
		"ORPHANS_IMAGES": images, "ORPHANS_TAGS": tags, "ORPHANS_ACCEPTED": accepted,
	})
	if code != 0 {
		t.Fatalf("a recorded orphan must be GREEN (exit 0), got exit %d\n%s", code, out)
	}
	if strings.Contains(out, "::error::") {
		t.Fatalf("recorded orphan still reported as an error:\n%s", out)
	}
}

// Recording an orphan must not blind the gate to the NEXT one. This is the
// failure mode a baseline file invites: accept a list, then quietly accept
// everything.
func TestOrphansStillRefusesAnUnrecordedOrphanBesideARecordedOne(t *testing.T) {
	dir := t.TempDir()
	images := write(t, dir, "images", published)
	tags := write(t, dir, "tags", "v1.801.477\n")
	accepted := write(t, dir, "accepted", "v1.801.479  unreconstructable\n")

	code, out := runOrphans(t, map[string]string{
		"ORPHANS_IMAGES": images, "ORPHANS_TAGS": tags, "ORPHANS_ACCEPTED": accepted,
	})
	if code != 1 {
		t.Fatalf("an unrecorded orphan beside a recorded one must be RED, got exit %d\n%s", code, out)
	}
	if !strings.Contains(out, theRefusal) {
		t.Fatalf("want the refusal for 480, got:\n%s", out)
	}
	if strings.Contains(out, "v1.801.479 is published but no git tag") {
		t.Fatalf("v1.801.479 is recorded and must stay quiet:\n%s", out)
	}
}

// "Could not read" is not "nothing was found". They demand opposite handling —
// one is an outage, the other is a clean run — and image-revision.sh's own
// header says collapsing them is how a verifier comes to pass by accident. A
// distinct exit code is what lets a caller tell them apart.
func TestOrphansSeparatesAFailedReadFromACleanRun(t *testing.T) {
	dir := t.TempDir()
	tags := write(t, dir, "tags", "v1.801.477\n")

	code, out := runOrphans(t, map[string]string{
		"ORPHANS_IMAGES": filepath.Join(dir, "does-not-exist"), "ORPHANS_TAGS": tags,
	})
	if code != 2 {
		t.Fatalf("an unreadable source must exit 2, not 0 (green) and not 1 (red), got exit %d\n%s", code, out)
	}

	// An empty registry listing is also a read that answered nothing, not proof
	// that nothing is published.
	empty := write(t, dir, "empty", "")
	code, out = runOrphans(t, map[string]string{
		"ORPHANS_IMAGES": empty, "ORPHANS_TAGS": tags,
	})
	if code != 2 {
		t.Fatalf("an empty published set must exit 2, not report a clean run, got exit %d\n%s", code, out)
	}
}

// The repo's own recorded-orphans file must parse and must name only versions —
// a typo there silently widens what the gate accepts, and it is the one input
// that is edited by hand.
func TestRecordedOrphansFileParses(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(".hanzo", "orphans.txt"))
	if err != nil {
		t.Fatalf("read .hanzo/orphans.txt: %v", err)
	}
	n := 0
	for i, line := range strings.Split(string(b), "\n") {
		if j := strings.Index(line, "#"); j >= 0 {
			line = line[:j]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		v := strings.Fields(line)[0]
		// The SAME shape orphans.sh matches. Anything this file lists that the
		// script would not recognise is a line that silently accepts nothing —
		// the entry looks like a record and excuses no image.
		if !recordedVersion.MatchString(v) {
			t.Errorf("line %d: %q does not name a version orphans.sh would match", i+1, v)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no recorded orphans parsed — the file exists to carry them")
	}
	t.Logf("%d recorded orphans", n)
}
