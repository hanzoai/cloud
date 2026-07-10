// Copyright (c) Hanzo AI. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// credentials.go — local credential map keyed by RunnerScaleSetName.
//
// A host learns its tenant list from the controller via the tenant
// catalog (tenantsource.go). Identity (configure URL, scale set name,
// ERS pointer, min/max runners) flows over the wire. Credentials NEVER
// cross the wire — they are loaded locally from either:
//
//  1. A legacy MultiConfig (supplies GitHub App / PAT auth flattened on
//     each Tenants[i] entry), OR
//  2. A flat credentials JSON at ARC_CREDENTIALS that maps
//     runner_scale_set_name → AppConfig.
//
// Both sources are merged. ARC_CREDENTIALS wins on conflict — the flat
// map is the forward-compatible form. A tenant whose remote identity has
// no local credential entry is SKIPPED with a structured log line, and
// its name is returned to the caller for ops dashboards.
package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
)

// envCredentialsPath names the env var that points at an optional flat
// credentials JSON. The schema is:
//
//	{
//	  "hanzoai-arm64": {"githubToken": "ghp_..."},
//	  "luxfi-amd64":   {"githubAppId": "123", "githubAppInstallationId": "456", "githubAppPrivateKey": "-----BEGIN..."}
//	}
//
// Keys are runner_scale_set_name. Values are the same shape as the
// chart's `auth:` field — one of PAT or GitHub App.
const envCredentialsPath = "ARC_CREDENTIALS"

// tenantCredentialMap maps runner_scale_set_name → AppConfig.
type tenantCredentialMap map[string]*AppConfig

// credentialAuth is the on-disk shape of one entry in the flat
// credentials JSON. Fields use the chart's camelCase names rather than
// the JSON-tag names on AppConfig so operators can hand-author the file.
type credentialAuth struct {
	GitHubToken             string `json:"githubToken,omitempty"`
	GitHubAppID             string `json:"githubAppId,omitempty"`
	GitHubAppInstallationID int64  `json:"githubAppInstallationId,omitempty"`
	GitHubAppPrivateKey     string `json:"githubAppPrivateKey,omitempty"`
}

func (c *credentialAuth) toAppConfig() *AppConfig {
	if c == nil {
		return nil
	}
	return &AppConfig{
		Token:             c.GitHubToken,
		AppID:             c.GitHubAppID,
		AppInstallationID: c.GitHubAppInstallationID,
		AppPrivateKey:     c.GitHubAppPrivateKey,
	}
}

// loadCredentialMap builds the merged credential map. It lifts auth from
// every entry in mc (when non-nil), then overlays the optional flat
// ARC_CREDENTIALS map on top.
//
// Returns a non-nil map even when both sources are empty — that way
// callers can do "if cred, ok := m[name]; ok" without nil-check
// boilerplate. An empty result simply means no tenant has local
// credentials, and mergeTenantsWithCredentials will skip them all.
func loadCredentialMap(mc *MultiConfig) (tenantCredentialMap, error) {
	out := tenantCredentialMap{}

	if mc != nil {
		for _, t := range mc.Tenants {
			if t == nil || t.RunnerScaleSetName == "" {
				continue
			}
			if t.AppConfig == nil {
				continue
			}
			if err := t.AppConfig.Validate(); err != nil {
				// Skip but don't fail — the operator may be staging a
				// no-credential MultiConfig as they migrate. The merge
				// pass will report the missing entry.
				continue
			}
			out[t.RunnerScaleSetName] = copyAppConfig(t.AppConfig)
		}
	}

	if path := os.Getenv(envCredentialsPath); path != "" {
		flat, err := readFlatCredentials(path)
		if err != nil {
			return nil, fmt.Errorf("read %s=%q: %w", envCredentialsPath, path, err)
		}
		for name, auth := range flat {
			ac := auth.toAppConfig()
			if err := ac.Validate(); err != nil {
				return nil, fmt.Errorf("%s[%q]: %w", envCredentialsPath, name, err)
			}
			out[name] = ac
		}
	}

	return out, nil
}

func readFlatCredentials(path string) (map[string]*credentialAuth, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var m map[string]*credentialAuth
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(m) == 0 {
		return nil, errors.New("credentials file is empty")
	}
	return m, nil
}

// mergeTenantsWithCredentials turns a list of remote-identity Tenants
// into the per-tenant *TenantConfig values the supervisor accepts.
// Tenants whose RunnerScaleSetName has no local credential entry are
// SKIPPED — their names are returned via the `skipped` slice so callers
// can surface ops-friendly logs / metrics.
//
// The returned configs share NO state with the input slice; the
// supervisor mutates per-tenant fields (RunnerScaleSetID gets resolved
// at runtime) and must not race other goroutines reading from the
// upstream cache.
func mergeTenantsWithCredentials(remote []Tenant, creds tenantCredentialMap, log *slog.Logger) (configs []*TenantConfig, skipped []string) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	configs = make([]*TenantConfig, 0, len(remote))
	for i := range remote {
		t := remote[i]
		key := t.RunnerScaleSetName
		if key == "" {
			// Defensive: a controller bug could ship empty names. Skip
			// rather than crash; the supervisor would reject the tenant
			// later anyway.
			log.Warn("tenant has empty runner_scale_set_name; skipping",
				"name", t.Name, "configure_url", t.ConfigureURL)
			continue
		}
		ac, ok := creds[key]
		if !ok {
			log.Warn("no local credentials for tenant; skipping",
				"runner_scale_set_name", key)
			skipped = append(skipped, key)
			continue
		}
		cfg := &TenantConfig{
			ConfigureURL:                t.ConfigureURL,
			RunnerScaleSetName:          t.RunnerScaleSetName,
			RunnerScaleSetID:            int(t.RunnerScaleSetID),
			EphemeralRunnerSetName:      t.EphemeralRunnerSetName,
			EphemeralRunnerSetNamespace: t.EphemeralRunnerSetNamespace,
			MaxRunners:                  int(t.MaxRunners),
			MinRunners:                  int(t.MinRunners),
			AppConfig:                   copyAppConfig(ac),
		}
		// EphemeralRunnerSetName defaults to RunnerScaleSetName when the
		// controller doesn't pin one — same convention as the MultiConfig
		// validator.
		if cfg.EphemeralRunnerSetName == "" {
			cfg.EphemeralRunnerSetName = cfg.RunnerScaleSetName
		}
		configs = append(configs, cfg)
	}
	return configs, skipped
}

// copyAppConfig returns a shallow copy of ac so the supervisor can stamp
// Token-or-AppID fields without racing the cache.
func copyAppConfig(ac *AppConfig) *AppConfig {
	if ac == nil {
		return nil
	}
	out := *ac
	return &out
}
