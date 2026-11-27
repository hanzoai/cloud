package forge

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// admitting models the store's rule for a peer (apps/kms attest.go admit): a ref
// under orgs/<org>/ opens only for a call acting for <org>. It records the org
// each read acted for.
type admitting struct{ acted *[]string }

func (k admitting) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	act := zip.CallerOf(ctx).Org
	*k.acted = append(*k.acted, act)
	if !strings.HasPrefix(ref, "orgs/"+act+"/") {
		return nil, errors.New("kms: ref " + ref + " belongs to another org than " + act)
	}
	if ref == HostKeyRef {
		return []byte("git.test ssh-ed25519 AAAAPINNED"), nil
	}
	return []byte("machine"), nil
}

// THE CREDENTIAL IS READ AS THE PLATFORM, WHOEVER ASKED. A tenant's request that
// finds the held client stale must not be the org the read acts for — that made
// the forge reachable only for the platform org, and every other org's coding
// run failed before it had an actor. Both ways a tenant reaches here are covered:
// a context that states it, and a request whose identity names it, which
// outranks anything stated on its context.
func TestClient_ReadsTheCredentialAsThePlatform(t *testing.T) {
	var acted []string
	kms := admitting{&acted}

	if _, err := (&Source{Host: "forge.test"}).Client(plane.For(t.Context(), "rt-verify"), kms, "api.hanzo.ai"); err != nil {
		t.Fatalf("stated tenant: %v", err)
	}

	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Post("/probe", func(ctx context.Context, _ *struct{}) (*struct{ Pinned bool }, error) {
		s := &Source{Host: "forge.test"}
		if _, err := s.Client(ctx, kms, "api.hanzo.ai"); err != nil {
			return nil, err
		}
		ok, _ := s.Pinned()
		return &struct{ Pinned bool }{ok}, nil
	})
	req := httptest.NewRequest("POST", "/probe", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(zip.HeaderOrg, "rt-verify")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("request from a tenant: status %v err %v", resp.StatusCode, err)
	}

	if want := []string{"hanzo", "hanzo", "hanzo", "hanzo"}; strings.Join(acted, ",") != strings.Join(want, ",") {
		t.Fatalf("reads acted for %v, want the platform org for the token and the pin both times", acted)
	}
}

// blocking answers only once its caller gives up, as a read over the plane does
// when the store is slow.
type blocking struct{}

func (blocking) GetSecret(ctx context.Context, _ string) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return []byte("machine"), nil
	}
}

// The read is still the caller's to bound: a caller that goes away takes the
// read with it rather than holding the source's lock for the store's timeout.
func TestClient_ReadStopsWithItsCaller(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := (&Source{Host: "forge.test"}).Client(ctx, blocking{}, "api.hanzo.ai"); err == nil {
		t.Fatal("a caller that went away was handed a client")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the read outlived its caller by %v", d)
	}
}
