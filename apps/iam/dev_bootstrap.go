// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/iam/pkg/model"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"
	luxlog "github.com/luxfi/log"
)

// DevBootstrapEnv opts an ephemeral local dev deployment (such as `hanzo up`)
// into initializing a fresh encrypted identity store + RS256 signing key when
// `{dir}/iam/iam.db` does not exist yet.
//
// Production safety: unset by default, and unconditionally refused whenever
// CLOUD_ENV names a production/mainnet environment so a missing volume mount
// in production can never silently mint an empty identity database.
const DevBootstrapEnv = "IAM_DEV_BOOTSTRAP"

func devBootstrapAllowed() bool {
	if strings.TrimSpace(env.Or(DevBootstrapEnv, "")) != "1" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(env.Or("CLOUD_ENV", ""))) {
	case "mainnet", "prod", "production":
		return false
	}
	return true
}

func devKeysDir(dir string) string {
	return filepath.Join(filepath.Dir(StorePath(dir)), "keys")
}

func ensureDevSigningKeysEnv(dir string) {
	if !devBootstrapAllowed() || strings.TrimSpace(os.Getenv("IAM_SIGNING_KEYS")) != "" {
		return
	}
	if exists(devKeysDir(dir)) {
		_ = os.Setenv("IAM_SIGNING_KEYS", devKeysDir(dir))
	}
}

// bootstrapDevStore creates a minimal local identity store with a generated
// RS256 signing certificate and key file so local clusters (`hanzo up`) boot
// with a working, encrypted-at-rest embedded IAM instead of failing closed.
func bootstrapDevStore(dir string) error {
	path := StorePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("iam dev bootstrap: mkdir %s: %w", filepath.Dir(path), err)
	}

	keysDir := devKeysDir(dir)
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		return fmt.Errorf("iam dev bootstrap: mkdir keys %s: %w", keysDir, err)
	}

	const certName = "cert-local"
	keyPath := filepath.Join(keysDir, certName)
	if !exists(keyPath) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return fmt.Errorf("iam dev bootstrap: generate rsa key: %w", err)
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		})
		if err := writeSynced(keyPath, pemBytes); err != nil {
			return fmt.Errorf("iam dev bootstrap: write signing key: %w", err)
		}
	}
	if strings.TrimSpace(os.Getenv("IAM_SIGNING_KEYS")) == "" {
		_ = os.Setenv("IAM_SIGNING_KEYS", keysDir)
	}

	db, err := iamstore.Open("sqlite", path, "")
	if err != nil {
		return fmt.Errorf("iam dev bootstrap: open sqlite store: %w", err)
	}
	defer db.Close()

	cert := orm.New[model.Cert](db)
	cert.Owner, cert.Name = "admin", certName
	cert.Type, cert.CryptoAlgorithm, cert.BitSize = "x509", "RS256", 2048
	cert.SetId("admin/" + certName)
	if err := cert.CreateCtx(context.Background()); err != nil {
		return fmt.Errorf("iam dev bootstrap: seed local cert: %w", err)
	}

	luxlog.Default().Info("iam dev bootstrap initialized local identity store", "store", path, "keys", keysDir)
	return nil
}
