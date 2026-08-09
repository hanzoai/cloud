package platform

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The smoke gate asserts a boot by grepping the binary's own log. That couples it
// to a string the binary emits, so these hold the two ends together — the gate
// waited for `"message":"listening"` while zip has logged "zip listening" since
// v1.1.0, so the pattern could never match and no release could pass smoke.

// smokeNeedle is the literal the smoke script greps for.
func smokeNeedle(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`grep -q '([^']+)' /tmp/boot\.log`).FindStringSubmatch(smokeScript)
	if m == nil {
		t.Fatal("smokeScript no longer greps boot.log for a listening signal")
	}
	return m[1]
}

// The needle has to match a line the zip transport actually writes. zip logs
// `a.logger.Info("zip listening", "transport", …)`, which a JSON handler renders
// as "message":"zip listening".
func TestSmokeWaitsForTheLineTheBinaryLogs(t *testing.T) {
	needle := smokeNeedle(t)
	actual := `{"level":"info","module":"zip","transport":"http","addr":":8080","message":"zip listening"}`
	if !strings.Contains(actual, needle) {
		t.Fatalf("smoke greps %q, which never appears in a real boot line:\n  %s", needle, actual)
	}
}

// The exact shape that shipped: an anchored "listening" excludes the "zip "
// prefix, so it matches nothing.
func TestTheOldNeedleWouldNotHaveMatched(t *testing.T) {
	actual := `{"message":"zip listening"}`
	if strings.Contains(actual, `"message":"listening"`) {
		t.Fatal("fixture wrong: the old needle would have matched after all")
	}
}

// zip is the dependency that owns the string, so the needle must still be found
// in the module the build links. Reading the source keeps this honest when zip is
// upgraded: a renamed log line fails HERE rather than silently at release time.
func TestTheNeedleExistsInTheZipModule(t *testing.T) {
	needle := smokeNeedle(t)
	msg := strings.TrimSuffix(strings.TrimPrefix(needle, `"message":"`), `"`)
	root := filepath.Join(os.Getenv("HOME"), "go/pkg/mod/github.com/zap-proto")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("module cache unavailable: %v", err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(root, e.Name(), "transport.go"))
		if err != nil {
			continue
		}
		if strings.Contains(string(b), `"`+msg+`"`) {
			return
		}
	}
	t.Fatalf("no zip module logs %q — the smoke gate would wait for a line nothing writes", msg)
}

// The boot window must sit above how long a real boot takes. cloud mounts ~28
// subsystems before the listener opens; at 60 iterations the gate sat close
// enough to a healthy boot to fire on one, the same defect the build deadline had.
func TestSmokeWaitsLongEnoughForARealBoot(t *testing.T) {
	m := regexp.MustCompile(`seq 1 (\d+)`).FindStringSubmatch(smokeScript)
	if m == nil {
		t.Fatal("smokeScript no longer bounds its wait")
	}
	if m[1] < "180" {
		t.Errorf("boot window is %ss; a real boot logged listening well after 60s", m[1])
	}
}

// AND THE POD MUST OUTLIVE THE SCRIPT. The test above reads the window the script
// spends; kubelet enforces a different one. They were 180 and 120, so the pod was
// killed a full minute before the script had finished waiting, and any boot
// landing in that gap came back as a smoke failure the script never wrote —
// while the assertion above passed the whole time, because the ceiling that
// actually applied was not in the script it read.
//
// A gate that reports "never reached listening" for a service that was still
// starting is worse than no gate: it fails builds that were fine, and it teaches
// people to re-run it until it passes.
func TestSmokeDeadlineOutlivesTheBootWindow(t *testing.T) {
	m := regexp.MustCompile(`seq 1 (\d+)`).FindStringSubmatch(smokeScript)
	if m == nil {
		t.Fatal("smokeScript no longer bounds its wait")
	}
	window, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("boot window %q is not a number", m[1])
	}
	k := &k8sClient{}
	spec := k.smokeJobSpec("probe", "ghcr.io/hanzoai/cloud:probe", "")
	deadline, ok := dig(spec.Object, "spec", "activeDeadlineSeconds")
	if !ok {
		t.Fatal("the smoke Job no longer bounds its own life — kubelet would let a hung boot run forever")
	}
	d, ok := deadline.(int64)
	if !ok {
		t.Fatalf("activeDeadlineSeconds is %T, want int64", deadline)
	}
	if d <= int64(window) {
		t.Errorf("pod deadline %ds does not outlive the %ds boot window — kubelet kills the script mid-wait and the verdict is the pod's, not the smoke's", d, window)
	}
}

// dig walks the unstructured Job spec.
func dig(m map[string]any, keys ...string) (any, bool) {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// The pull secret has to exist in the namespace the Job runs in, or the smoke
// pulls anonymously and a private image fails for a reason nothing reports.
func TestSmokePullsWithABuildNamespaceSecret(t *testing.T) {
	if buildPullSecret == "kaniko-ghcr" {
		t.Error("kaniko-ghcr does not exist in hanzo-build; the namespace holds ghcr-pull")
	}
	if buildPullSecret == "" {
		t.Error("an empty pull secret means an anonymous pull")
	}
}
