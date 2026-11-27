package platform

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeForgeBuildLendsOnlyReaderAndKeepsAssertionOutOfSolve(t *testing.T) {
	t.Setenv("CLOUD_FORGE_BUILD_AUTH", "iam")
	t.Setenv("CLOUD_FORGE_TOKEN_IMAGE", "ghcr.io/hanzo-inc/cloud:v1@sha256:"+strings.Repeat("a", 64))
	was := forgeHost
	forgeHost = "git.hanzo.ai"
	t.Cleanup(func() { forgeHost = was })
	k := fakeK8s()
	job := k.buildJobSpec(estate, "test", "bld_test", "hanzo", "git", "push-hanzoai", "", []any{"buildctl-daemonless.sh"}, defaultArch, solveSecrets(estate, "https://git.hanzo.ai/hanzoai/git.git#main", false))
	pod := podSpec(job.Object)
	if pod["serviceAccountName"] != "forge-reader" || pod["automountServiceAccountToken"] != false {
		t.Fatal("wrong workload identity")
	}
	inits := pod["initContainers"].([]any)
	if len(inits) != 1 {
		t.Fatal("reader must be minted before the solve")
	}
	solve := pod["containers"].([]any)[0].(map[string]any)
	raw, _ := json.Marshal(solve)
	for _, forbidden := range []string{"forge-assertion", "forge-token", "forge-registry-token", "FORGE_TRACKER"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("solve leaked %s", forbidden)
		}
	}
	for _, required := range []string{"id=GIT_AUTH_TOKEN,src=" + readerTokenPath, "id=REGISTRY_TOKEN,src=" + readerTokenPath} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("no native reader secret %s: command=%v", required, solve["command"])
		}
	}
	customer := k.buildJobSpec(customer, "customer", "bld_customer", "tenant", "app", "push-hanzoai", "ghcr.io/hanzoai/test:1", []any{"buildctl-daemonless.sh"}, defaultArch, nil)
	raw, _ = json.Marshal(customer.Object)
	if strings.Contains(string(raw), "forge-reader") || strings.Contains(string(raw), "forge-assertion") {
		t.Fatal("customer acquired estate identity")
	}
}
