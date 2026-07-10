// Copyright (c) Hanzo AI. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadCredentialMap_FromMultiConfig lifts PAT auth out of the
// legacy MultiConfig and keys it by RunnerScaleSetName.
func TestLoadCredentialMap_FromMultiConfig(t *testing.T) {
	mc := &MultiConfig{
		Tenants: []*TenantConfig{
			{
				RunnerScaleSetName: "hanzoai-arm64",
				AppConfig:          &AppConfig{Token: "ghp_aaa"},
			},
			{
				RunnerScaleSetName: "luxfi-amd64",
				AppConfig: &AppConfig{
					AppID:             "123",
					AppInstallationID: 456,
					AppPrivateKey:     "key-bytes",
				},
			},
		},
	}
	m, err := loadCredentialMap(mc)
	if err != nil {
		t.Fatalf("loadCredentialMap: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("len(m) = %d, want 2", len(m))
	}
	if m["hanzoai-arm64"].Token != "ghp_aaa" {
		t.Fatalf("hanzoai-arm64 PAT not lifted: %+v", m["hanzoai-arm64"])
	}
	if m["luxfi-amd64"].AppID != "123" {
		t.Fatalf("luxfi-amd64 AppID not lifted: %+v", m["luxfi-amd64"])
	}
}

// TestLoadCredentialMap_OverlayFlatFile proves the flat file at
// ARC_CREDENTIALS overlays MultiConfig credentials of the same key.
func TestLoadCredentialMap_OverlayFlatFile(t *testing.T) {
	mc := &MultiConfig{
		Tenants: []*TenantConfig{
			{
				RunnerScaleSetName: "hanzoai-arm64",
				AppConfig:          &AppConfig{Token: "ghp_old"},
			},
		},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	flat := `{
		"hanzoai-arm64": {"githubToken": "ghp_new"},
		"luxfi-amd64":   {"githubAppId": "123", "githubAppInstallationId": 456, "githubAppPrivateKey": "key-bytes"}
	}`
	if err := os.WriteFile(path, []byte(flat), 0o600); err != nil {
		t.Fatalf("write flat: %v", err)
	}
	t.Setenv(envCredentialsPath, path)

	m, err := loadCredentialMap(mc)
	if err != nil {
		t.Fatalf("loadCredentialMap: %v", err)
	}
	if m["hanzoai-arm64"].Token != "ghp_new" {
		t.Fatalf("flat did not override mc: got %q, want ghp_new", m["hanzoai-arm64"].Token)
	}
	if m["luxfi-amd64"].AppID != "123" {
		t.Fatalf("flat did not add new entry: %+v", m["luxfi-amd64"])
	}
}

// TestMergeTenantsWithCredentials_SkipsMissingCreds proves a remote
// tenant whose key has no local credential entry is dropped from the
// runtime configs and recorded in the skipped slice.
func TestMergeTenantsWithCredentials_SkipsMissingCreds(t *testing.T) {
	remote := []Tenant{
		{
			RunnerScaleSetName:          "have-creds",
			ConfigureURL:                "https://github.com/hanzoai",
			EphemeralRunnerSetNamespace: "arc-runners",
			MaxRunners:                  4,
		},
		{
			RunnerScaleSetName:          "no-creds",
			ConfigureURL:                "https://github.com/luxfi",
			EphemeralRunnerSetNamespace: "arc-runners",
			MaxRunners:                  8,
		},
	}
	creds := tenantCredentialMap{
		"have-creds": &AppConfig{Token: "ghp_present"},
	}
	configs, skipped := mergeTenantsWithCredentials(remote, creds, slog.New(slog.DiscardHandler))
	if len(configs) != 1 {
		t.Fatalf("len(configs) = %d, want 1", len(configs))
	}
	if configs[0].RunnerScaleSetName != "have-creds" {
		t.Fatalf("kept wrong tenant: %s", configs[0].RunnerScaleSetName)
	}
	if configs[0].AppConfig.Token != "ghp_present" {
		t.Fatalf("credentials not bound to runtime config: %+v", configs[0].AppConfig)
	}
	if configs[0].EphemeralRunnerSetName != "have-creds" {
		t.Fatalf("ERS name should default to scale-set name when remote is empty, got %q", configs[0].EphemeralRunnerSetName)
	}
	if len(skipped) != 1 || skipped[0] != "no-creds" {
		t.Fatalf("skipped = %v, want [no-creds]", skipped)
	}
}
