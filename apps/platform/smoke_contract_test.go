package platform

import (
	"os"
	"path/filepath"
	"regexp"
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
