// Copyright (c) Hanzo AI. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// tenantconfig.go — the per-tenant runtime config and the multi-tenant
// bag the controller-side tenant catalog and credential merge operate
// on.
//
// These are minimal projections of arc's cmd/ghalistener/config.Config
// (one tenant) and cmd/multighalistener/config.MultiConfig (the bag).
// The upstream single-tenant config also carries Vault / Azure Key
// Vault / metrics / proxy / scaleset-client machinery that the host
// role's tenant catalog and credential merge never touch — only the
// identity + sizing + credential fields below are needed, so only those
// are copied.
package runner

// TenantConfig is one tenant's runtime configuration: GitHub identity,
// scale-set naming, min/max sizing, and (locally-sourced) credentials.
// AppConfig is embedded so a TenantConfig carries its credential bag the
// same way the upstream listener config does.
type TenantConfig struct {
	ConfigureURL string `json:"configure_url"`
	// AppConfig contains the GitHub credentials. Loaded locally, never
	// over the wire.
	*AppConfig
	EphemeralRunnerSetNamespace string `json:"ephemeral_runner_set_namespace"`
	EphemeralRunnerSetName      string `json:"ephemeral_runner_set_name"`
	MaxRunners                  int    `json:"max_runners"`
	MinRunners                  int    `json:"min_runners"`
	RunnerScaleSetID            int    `json:"runner_scale_set_id"`
	RunnerScaleSetName          string `json:"runner_scale_set_name"`
}

// MultiConfig is the multi-tenant bag: N TenantConfig triples the
// controller was booted with. It is the static fallback catalog served
// by mcTenantSource when no cluster CRs are available.
type MultiConfig struct {
	// Tenants is the list of per-org/per-scale-set configurations.
	Tenants []*TenantConfig `json:"tenants"`
}
