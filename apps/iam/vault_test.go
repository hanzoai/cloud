package iam

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	iamserver "github.com/hanzoai/iam/server"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// An organization's signing key is sealed in KMS under the org, in the iam custody
// subtree, for the org the ref names; a key is never replaced; an absent key is
// ErrNoKey. (The subtree is custodied: apps/kms TestTheIAMSigningSubtreeIsCustodied.)
func TestAnOrgsSigningKeyIsSealedInKMSUnderTheOrg(t *testing.T) {
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	store := planetest.ServeKMS(t)
	ctx := context.Background()
	v := vault{}
	const ref = "orgs/acme/iam/signing/acme-signing"

	if _, err := v.Get(ctx, "acme", "acme-signing"); !errors.Is(err, iamserver.ErrNoKey) {
		t.Fatalf("a key never sealed was not ErrNoKey: %v", err)
	}
	if err := v.Put(ctx, "acme", "acme-signing", "PEM-ONE"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := store.Get(ref); got != "PEM-ONE" {
		t.Fatalf("the key was not sealed at %s: %q", ref, got)
	}
	if err := v.Put(ctx, "acme", "acme-signing", "PEM-TWO"); !errors.Is(err, iamserver.ErrKeyExists) {
		t.Fatalf("a held key was replaced or the refusal is not ErrKeyExists: %v", err)
	}
	if got := store.Get(ref); got != "PEM-ONE" {
		t.Fatalf("a refused put changed the sealed key: %q", got)
	}
	got, err := v.Get(ctx, "acme", "acme-signing")
	if err != nil || got != "PEM-ONE" {
		t.Fatalf("get = %q, %v", got, err)
	}
	// Every call acted for the org the ref names, and for no other.
	for _, asked := range store.Asked() {
		if !strings.HasPrefix(asked, "acme orgs/acme/iam/signing/") {
			t.Errorf("a call acted for another org than the ref's: %q", asked)
		}
	}
	if err := v.Delete(ctx, "acme", "acme-signing"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := v.Delete(ctx, "acme", "acme-signing"); err != nil {
		t.Fatalf("deleting an absent key is an error: %v", err)
	}
	if _, err := v.Get(ctx, "acme", "acme-signing"); !errors.Is(err, iamserver.ErrNoKey) {
		t.Fatalf("a deleted key was not ErrNoKey: %v", err)
	}
}

// A key is sealed for the org the ref names even when IAM is asked by someone else: a
// SuperAdmin creating an org's cert (acting in the admin org) or the anonymous token
// endpoint. The request behind the context must not decide whose call it is.
func TestTheVaultActsForTheOrgWhoeverAsked(t *testing.T) {
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	store := planetest.ServeKMS(t)
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	app.Use(cloud.Bridge())
	app.Use(zip.H(func(c *zip.Ctx) error {
		principal.Mint(c, principal.Principal{Org: "admin", User: "z", Subject: "z", Kind: principal.Person, Stable: true, Holder: "admin/z"})
		return c.Next()
	}))
	var putErr, getErr error
	var got string
	// A typed op, as IAM's create is: the request is behind the context it is handed.
	app.Post("/seal", func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		putErr = vault{}.Put(ctx, "acme", "acme-signing", "PEM")
		got, getErr = vault{}.Get(ctx, "acme", "acme-signing")
		return &struct{}{}, nil
	})
	if _, err := app.Test(httptest.NewRequest(http.MethodPost, "/seal", nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
		t.Fatal(err)
	}
	if putErr != nil || getErr != nil || got != "PEM" {
		t.Fatalf("a SuperAdmin's create was refused or lost: put %v, get %v, %q", putErr, getErr, got)
	}
	for _, asked := range store.Asked() {
		if !strings.HasPrefix(asked, "acme orgs/acme/iam/signing/") {
			t.Errorf("a call acted for the caller's org, not the ref's: %q", asked)
		}
	}
}
