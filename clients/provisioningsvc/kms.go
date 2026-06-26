package provisioningsvc

import (
	"errors"
	"os"
	"strconv"
	"strings"

	kms "github.com/hanzoai/kms/sdk/go"
	luxlog "github.com/luxfi/log"
)

// secrets wraps the Hanzo KMS client (github.com/hanzoai/kms/sdk/go) for
// storing provisioned-resource passwords. Encryption is client-side: the CEK
// is derived from CLOUD_KMS_PASSPHRASE and never leaves this process; the MPC
// nodes only ever see ciphertext.
//
// SAFE DEGRADE: if KMS is not configured (CLOUD_KMS_NODES or
// CLOUD_KMS_PASSPHRASE empty) or cannot be unlocked, Enabled() is false. In
// that mode we NEVER write a plaintext password anywhere persistent — the
// create handler returns the generated password exactly once in the HTTP
// response and stores only metadata (secret_ref left empty). This honors the
// hard rule: never store a password in plaintext.
type secrets struct {
	client  *kms.Client
	enabled bool
	log     luxlog.Logger
}

// envs read here:
//
//	CLOUD_KMS_NODES       CSV of MPC node URLs (e.g. https://kms-0:9999,https://kms-1:9999). Empty => degraded.
//	CLOUD_KMS_PASSPHRASE  passphrase that derives the client-side CEK.            Empty => degraded.
//	CLOUD_KMS_ORG         KMS org slug for sealed secrets (default: deployment brand, else "hanzo").
//	CLOUD_KMS_THRESHOLD   t-of-n quorum (default: number of nodes; clamped to [1,n]).
func openSecrets(brand string, log luxlog.Logger) *secrets {
	s := &secrets{log: log}

	nodesCSV := os.Getenv("CLOUD_KMS_NODES")
	pass := os.Getenv("CLOUD_KMS_PASSPHRASE")
	if strings.TrimSpace(nodesCSV) == "" || pass == "" {
		log.Warn("provisioning KMS degraded: set CLOUD_KMS_NODES + CLOUD_KMS_PASSPHRASE to persist secrets; passwords are returned once on create and not stored")
		return s
	}

	var nodes []string
	for _, n := range strings.Split(nodesCSV, ",") {
		if t := strings.TrimSpace(n); t != "" {
			nodes = append(nodes, t)
		}
	}

	org := env("CLOUD_KMS_ORG", brand)
	if org == "" {
		org = "hanzo"
	}

	threshold := len(nodes)
	if v := os.Getenv("CLOUD_KMS_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(nodes) {
			threshold = n
		}
	}

	client, err := kms.NewClient(kms.Config{Nodes: nodes, OrgSlug: org, Threshold: threshold})
	if err != nil {
		log.Error("provisioning KMS init failed; degrading", "err", err)
		return s
	}
	// Unlock derives the CEK from the passphrase client-side. Without it Set/Get
	// fail closed ("client is locked"), so a failed unlock means degrade.
	if err := client.Unlock(pass); err != nil {
		log.Error("provisioning KMS unlock failed; degrading", "err", err)
		return s
	}

	s.client = client
	s.enabled = true
	log.Info("provisioning KMS enabled", "org", org, "nodes", len(nodes), "threshold", threshold)
	return s
}

// Enabled reports whether secrets can be persisted to KMS.
func (s *secrets) Enabled() bool { return s != nil && s.enabled }

// Put seals value under ref. Only call when Enabled() is true.
func (s *secrets) Put(ref string, value []byte) error {
	if !s.Enabled() {
		return errors.New("provisioning: KMS disabled")
	}
	return s.client.Set(ref, value)
}

// Get returns the sealed value for ref.
func (s *secrets) Get(ref string) ([]byte, error) {
	if !s.Enabled() {
		return nil, errors.New("provisioning: KMS disabled")
	}
	return s.client.Get(ref)
}

// Delete removes the sealed secret. Best-effort; no-op when degraded.
func (s *secrets) Delete(ref string) error {
	if !s.Enabled() {
		return nil
	}
	return s.client.Delete(ref)
}
