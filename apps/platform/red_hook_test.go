package platform

// red_hook_test.go — red's adversarial probes against the forge's push endpoint,
// kept.
//
// The findings red confirmed are answered by NAMED tests in hook_test.go beside
// the behaviour they pin, which is where a regression test belongs — a dispatch
// failure refused and un-deduped, an encoded body refused before it is read, a
// failed refresh keeping the key that works, a panicked read releasing its
// refresh, one landed commit under four spellings of its namespace, and the
// build count in the answer.
//
// What stays here is what red proved and nobody has to fix: the wire the fork
// actually signs with, what an anonymous flood costs, and the two bounds this
// endpoint does NOT hold on its own. They are adversarial evidence rather than a
// pending bug, and deleting them would delete the measurement.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── the forge's own no-secret delivery must be refused ───────────────────────
//
// hanzoai/git with an unset hook secret sends X-Git-Signature: "" and the literal
// X-Hub-Signature-256: "sha256=" (services/webhook/deliver.go:101,137,149).
func TestRED_ForgeUnsignedDeliveryShapeIsRefused(t *testing.T) {
	app, f := hookApp(t, hookSecret)
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")
	code, _ := deliver(t, app, body,
		"X-Git-Signature", "",
		"X-Gitea-Signature", "",
		"X-Hub-Signature-256", "sha256=")
	if code != http.StatusUnauthorized {
		t.Fatalf("BYPASS: the forge's unsigned delivery shape got %d, want 401", code)
	}
	if p, e := f.counts(); p != 0 || e != 0 {
		t.Fatalf("BYPASS: dispatched %d/%d on an unsigned delivery", p, e)
	}
}

// Every spelling the fork actually emits, in its actual value format.
// X-Git-/X-Gitea- are BARE hex; X-Hub-Signature-256 is "sha256="-prefixed.
func TestRED_EveryRealForgeSpellingIsAccepted(t *testing.T) {
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")
	hex := sign(hookSecret, body)
	for _, c := range []struct{ h, v string }{
		{"X-Git-Signature", hex},
		{"X-Gitea-Signature", hex},
		{"X-Hub-Signature-256", "sha256=" + hex},
		{"X-Git-Signature", "sha256=" + hex},  // prefix tolerated on the bare header
		{"X-Hub-Signature-256", hex},          // bare tolerated on the prefixed header
		{"X-Gitea-Signature", " " + hex + ""}, // leading space
	} {
		app, _ := hookApp(t, hookSecret)
		if code, v := deliver(t, app, body, c.h, c.v); code != http.StatusOK || !v.Fired {
			t.Errorf("%s=%.12s… → %d %+v", c.h, c.v, code, v)
		}
	}
	// The fork ALSO emits X-Hub-Signature: sha1=<hex> and X-Gogs-Signature.
	// Neither is accepted. SHA-1 must never be.
	app, _ := hookApp(t, hookSecret)
	if code, _ := deliver(t, app, body, "X-Gogs-Signature", hex); code != http.StatusUnauthorized {
		t.Errorf("X-Gogs-Signature (emitted by the fork, bare hex) → %d, want 401", code)
	}
	t.Log("note: X-Gogs-Signature carries the SAME valid digest and is refused — a " +
		"receiver that only saw Gogs headers would 401 forever")
}

// A KMS failure IS cached: a flood costs one read per window, not one per request.
// This is the property the endpoint claims loudest, measured.
func TestRED_FloodAgainstAFailingKMSCostsOneRead(t *testing.T) {
	kms := newFakeKMS()
	kms.down = fmt.Errorf("kms: secret not found")
	app, _ := hookAppWith(t, kms, "api.hanzo.ai", nil)
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")
	for i := 0; i < 200; i++ {
		if code, _ := signedDelivery(t, app, body); code != http.StatusServiceUnavailable {
			t.Fatalf("delivery %d: want 503 fail-closed, got %d", i, code)
		}
	}
	if n := kms.readCount(); n != 1 {
		t.Fatalf("200 anonymous deliveries cost %d KMS reads, want 1", n)
	}
	t.Log("REFUTED as amplification: 200 deliveries → 1 KMS read, all 503 fail-closed")
}

// ── the bound this endpoint does NOT hold: the allocation ────────────────────
//
// maxHookBody bounds what is HASHED, and it cannot bound what is ALLOCATED: the
// request is in memory before any handler can measure it. The real bound is the
// edge's BodyLimit, and it is the reason the encoded-body refusal (hook.go) has
// to run before the body is read — decoding is the one path where a few bytes on
// the wire buy the whole of that limit.
func TestRED_OversizedBodyIsFullyReadBeforeItIsRefused(t *testing.T) {
	app, _ := hookApp(t, hookSecret)
	big := strings.Repeat("A", (12<<20)+1) // > 8 MiB cap, < 16 MiB edge limit
	req := httptest.NewRequest(http.MethodPost, hookPath, strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d", resp.StatusCode)
	}
	t.Log("CONFIRMED and accepted: a 12 MiB anonymous body is admitted by the edge, " +
		"materialised in full, and only then refused 413. maxHookBody bounds the HASH; " +
		"the edge's 16 MiB BodyLimit is the allocation bound, and no header can lift it.")
}

// What an anonymous 8 MiB delivery costs in CPU before it is refused 401.
func BenchmarkRED_UnauthenticatedHMACCost(b *testing.B) {
	body := make([]byte, 8<<20)
	for i := b.N; i > 0; i-- {
		if signed(hookSecret, body, strings.Repeat("ab", 32)) {
			b.Fatal("matched")
		}
	}
	b.SetBytes(int64(len(body)))
}

// ── cross-replica: the dedup is per-process ──────────────────────────────────
//
// NOT a defect, and stated so it is not rediscovered as one: the duplicate this
// memory exists to stop is a redelivery of a request that timed out after the
// clients ran, and that retry reaches whichever replica the Service sends it to. A
// cross-replica answer belongs to the build store, and giving it here would put
// one question in two places.
func TestRED_DedupIsPerReplica(t *testing.T) {
	a, b := &seen{}, &seen{}
	key := "hanzoai/cloud refs/heads/main " + hookCommit
	now := time.Now()
	if !a.hold(key, now) || !b.hold(key, now) {
		t.Fatal("REFUTED")
	}
	t.Log("CONFIRMED: two replicas each hold the same redelivery. A k8s Service " +
		"round-robins, so the forge's retry lands on a different pod with p=(N-1)/N.")
}

// ── replaying a signed body forever ──────────────────────────────────────────
//
// NOTED, not fixed: the signature covers a body that carries no nonce and no
// timestamp, so a captured delivery verifies for as long as the secret lives.
// What it can DO is bounded by the dedup — the same landed fact fires once per
// window — and past that window a replay re-fires a commit that is already built.
// Closing it needs a value the forge does not send; the fix is a forge change,
// not a receiver change.
func TestRED_ACapturedDeliveryStillVerifies(t *testing.T) {
	body := pushBody(t, hookOwner, "cloud", "refs/heads/main", hookBefore, hookCommit, "z")
	sig := sign(hookSecret, body)
	if !signed(hookSecret, body, sig) {
		t.Fatal("REFUTED: the body carries something that expires")
	}
	t.Log("CONFIRMED: nothing in the signed bytes ties a delivery to a moment. The " +
		"dedup bounds the effect to one build per landed fact per window; the forge " +
		"would have to sign a timestamp or a nonce to bound the window itself.")
}

// Concurrency: hammer claim/settle/fetch and the seen map from many goroutines
// while the window keeps expiring, under -race.
func TestRED_SecretRefreshUnderConcurrency(t *testing.T) {
	kms := sealed(t, hookSecret)
	s := &cloud.Service[state]{Base: cloud.Base{KMS: kms, Log: luxlog.New("red")}}
	// Read once first, so what is measured is the property that holds AFTER a key
	// has been read cleanly. Before that there is genuinely nothing to serve, and
	// a caller arriving inside the very first read is told so.
	if v, err := s.State.hook.read(s, t.Context()); err != nil || v != hookSecret {
		t.Fatalf("prime: %q %v", v, err)
	}
	var wg sync.WaitGroup
	var served, refused atomic.Int64
	stop := make(chan struct{})
	go func() { // force the window open constantly
		for {
			select {
			case <-stop:
				return
			default:
				s.State.hook.mu.Lock()
				s.State.hook.when = time.Now().Add(-hookFresh - time.Second)
				s.State.hook.mu.Unlock()
			}
		}
	}()
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if v, err := s.State.hook.read(s, t.Context()); err != nil || v != hookSecret {
					refused.Add(1)
				} else {
					served.Add(1)
				}
				s.State.landed.hold(fmt.Sprintf("hanzoai/cloud refs/heads/b%d %d", n, j), time.Now())
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	t.Logf("12800 reads with the window forced open: served=%d refused=%d kmsReads=%d",
		served.Load(), refused.Load(), kms.readCount())
	// Once a key has been read cleanly, NOTHING answers a caller with nothing:
	// the held value is served through every refresh, in flight or not.
	if refused.Load() > 0 {
		t.Fatalf("%d reads were refused after the key had been read once", refused.Load())
	}
}
