// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBenchmarkAllRegimesAndDossierParity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	rep, err := RunAllRegimes(ctx, 1350, 20, 40)
	if err != nil {
		t.Fatalf("RunAllRegimes: %v", err)
	}
	if len(rep.Regimes) != 5 {
		t.Fatalf("expected 5 regimes, got %d", len(rep.Regimes))
	}

	baseline := rep.Regimes[0]
	primary := rep.Regimes[1]
	negUnver := rep.Regimes[2]
	negRegKill := rep.Regimes[3]
	negPart := rep.Regimes[4]

	if baseline.HanzoCalls <= 0 {
		t.Fatalf("expected baseline HanzoCalls > 0, got %d", baseline.HanzoCalls)
	}
	if primary.HanzoCalls != 0 || !primary.HanzoRegistryDisabled {
		t.Fatalf("expected primary HanzoCalls=0 and HanzoRegistryDisabled=true, got calls=%d disabled=%v", primary.HanzoCalls, primary.HanzoRegistryDisabled)
	}
	if primary.PreKillCompletionPct != 100.0 || primary.PostKillCompletionPct != 100.0 {
		t.Fatalf("expected 100%% pre/post-kill completion, got pre=%.2f%% post=%.2f%%", primary.PreKillCompletionPct, primary.PostKillCompletionPct)
	}
	if primary.AdversarialDetectionPct != 100.0 || primary.AdversarialInjected < 90 {
		t.Fatalf("expected 100%% adversarial detection across >=90 vectors, got %d/%d (%.2f%%)", primary.AdversarialDetected, primary.AdversarialInjected, primary.AdversarialDetectionPct)
	}
	if primary.NATRelayedCompleted == 0 {
		t.Fatal("expected at least 1 NAT-relayed inference completion in primary regime")
	}
	if primary.Sovereignty == nil || !primary.Sovereignty.PostForkRouteOK || !primary.Sovereignty.TamperRejected || primary.Sovereignty.PostForkHanzoCalls != 0 {
		t.Fatalf("sovereignty verification failed: %+v", primary.Sovereignty)
	}
	if negUnver.HijackRatePct != 100.0 || negUnver.AdversarialDetectionPct != 0.0 {
		t.Fatalf("expected 100%% hijack & 0%% detection in unverified control, got hijack=%.2f%% det=%.2f%%", negUnver.HijackRatePct, negUnver.AdversarialDetectionPct)
	}
	if negRegKill.PostKillCompletionPct != 0.0 {
		t.Fatalf("expected 0%% post-kill completion when central registry killed, got %.2f%%", negRegKill.PostKillCompletionPct)
	}
	if negPart.PostKillCompletionPct != 0.0 || negPart.AdversarialDetectionPct != 100.0 || negPart.HijackedRequests != 0 {
		t.Fatalf("unexpected total partition boundary result: comp=%.2f%% det=%.2f%% hijack=%d", negPart.PostKillCompletionPct, negPart.AdversarialDetectionPct, negPart.HijackedRequests)
	}

	tmpDir := t.TempDir()
	if err := writeDossier(tmpDir, rep); err != nil {
		t.Fatalf("writeDossier: %v", err)
	}
	for _, fn := range []string{"README.md", "score_table.md", "raw_results.json"} {
		data, err := os.ReadFile(filepath.Join(tmpDir, fn))
		if err != nil || len(data) < 200 {
			t.Fatalf("expected non-empty %s, err=%v len=%d", fn, err, len(data))
		}
		text := string(data)
		if strings.Contains(text, "NaN") || strings.Contains(text, "+Inf") || strings.Contains(text, "-Inf") {
			t.Fatalf("%s contains NaN/Inf", fn)
		}
		if fn == "raw_results.json" {
			if strings.Contains(text, `"ttft_overall"`) {
				t.Fatalf("raw_results.json must not report unlabeled ttft_overall")
			}
			if !strings.Contains(text, `"estimated_client_ttft"`) || !strings.Contains(text, `"provider_local_ttft"`) || !strings.Contains(text, `"ttft_methodology"`) {
				t.Fatalf("raw_results.json must distinguish estimated_client_ttft and provider_local_ttft with methodology")
			}
		}
		if fn == "README.md" {
			if !strings.Contains(text, "estimated client TTFT") || !strings.Contains(text, "measured provider-local first-token") {
				t.Fatalf("README.md must explicitly label estimated client TTFT vs measured provider-local first-token")
			}
		}
	}
}
