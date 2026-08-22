// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/iam/pkg/model"
	"github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"
)

// A store full of certificates is not the same thing as a store that can SIGN,
// and the difference is exactly one mounted directory.
//
// A Cert row carries the key's IDENTITY — owner, name, algorithm, and the public
// certificate the JWKS publishes. The private half is supplied by the deployment
// and held in memory, so a complete-looking key set can sit in front of a process
// that cannot produce a single token. Published that way, every relying party
// reads the resulting failures as bad tokens rather than as a fault here, which
// is the one outcome worth refusing outright.
//
// So identity answers 503 instead — the same answer an absent store gets, for the
// same reason: a loud refusal is recoverable, and a key set nothing can sign for
// is not.
func TestIdentityRefusesAStoreItCannotSignFor(t *testing.T) {
	// A cert name of its own per case. Material that has been read once is held
	// for the life of the PROCESS — that is what lets a rotation land without a
	// restart — so two cases sharing a name would have the mounted one supplying
	// the absent one's key, and the absent case would pass by accident.
	for _, tc := range []struct {
		name   string
		cert   string
		mount  bool
		status int
		store  bool
	}{
		{name: "key mounted", cert: "cert-mounted", mount: true, status: http.StatusOK, store: true},
		{name: "key absent", cert: "cert-unmounted", mount: false, status: http.StatusServiceUnavailable, store: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := storeWithSigningCert(t, tc.cert)
			if tc.mount {
				mountSigningKey(t, tc.cert)
			} else {
				// A directory that exists and holds nothing: the deployment shape
				// where the Secret is not projected. Pointed somewhere real so this
				// tests an unmounted KEY and not an unset variable.
				t.Setenv("IAM_SIGNING_KEYS", t.TempDir())
			}
			t.Setenv("initDataFile", filepath.Join(dir, "absent.json"))

			app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
			if err := Mount(app, cloud.Deps{DataDir: dir}); err != nil {
				t.Fatalf("Mount: %v", err)
			}

			// Mount always returns nil and always declares IAM's addresses — what
			// changes is whether it was handed a store. DB() is how an in-process
			// reader tells, and it is what decides the answer below.
			if got := DB() != nil; got != tc.store {
				t.Errorf("DB() non-nil = %v, want %v: a store it cannot sign for must not be served", got, tc.store)
			}

			resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/iam/.well-known/jwks", nil))
			if err != nil {
				t.Fatalf("Test: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Errorf("GET /v1/iam/.well-known/jwks = %d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
}

// storeWithSigningCert writes a data dir holding one reserved-owner signing cert
// of the given name and nothing else — identity as a deployment has it, minus the
// key.
func storeWithSigningCert(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(StorePath(dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open("sqlite", StorePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	cert := orm.New[model.Cert](db)
	cert.Owner, cert.Name = "admin", name
	cert.Type, cert.CryptoAlgorithm, cert.BitSize = "x509", "RS256", 2048
	cert.SetId("admin/" + name)
	if err := cert.CreateCtx(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}
