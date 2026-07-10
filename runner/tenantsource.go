// Copyright (c) Hanzo AI. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// tenantsource.go — controller-side tenant catalog.
//
// The tenant catalog answers "what tenants exist" and is served to
// hosts over the control channel. It is deliberately separated from the
// credential bag (credentials.go): identity is a fact about the cluster,
// credentials are a fact about each operator's secrets.
//
// tenantSource abstracts the catalog. Three implementations:
//
//   - mcTenantSource: serves the static MultiConfig the controller was
//     booted with. Useful as a fallback when the controller-runtime
//     client isn't configured.
//
//   - crTenantSource: lists AutoscalingRunnerSet CRs from the cluster
//     using a non-cached controller-runtime client. The CRs are the
//     authoritative catalog. This is in-cluster-only: on a host with no
//     cluster (the standalone JIT daemon) it is never constructed.
//
//   - combinedTenantSource: try CR first, fall back to MultiConfig.
package runner

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// tenantSource is the controller-side view of "what tenants exist".
// Returns identity only — credentials are local to the host.
type tenantSource interface {
	List(ctx context.Context) ([]Tenant, error)
}

// -----------------------------------------------------------------
// mcTenantSource — static MultiConfig fallback.
// -----------------------------------------------------------------

type mcTenantSource struct {
	mc *MultiConfig
}

func newMCTenantSource(mc *MultiConfig) *mcTenantSource {
	return &mcTenantSource{mc: mc}
}

func (s *mcTenantSource) List(_ context.Context) ([]Tenant, error) {
	if s == nil || s.mc == nil {
		return nil, nil
	}
	out := make([]Tenant, 0, len(s.mc.Tenants))
	for _, t := range s.mc.Tenants {
		if t == nil {
			continue
		}
		out = append(out, Tenant{
			Name:                        t.RunnerScaleSetName,
			Org:                         extractOrg(t.ConfigureURL),
			RunnerScaleSetID:            uint32(t.RunnerScaleSetID),
			MaxRunners:                  uint32(t.MaxRunners),
			MinRunners:                  uint32(t.MinRunners),
			ConfigureURL:                t.ConfigureURL,
			RunnerScaleSetName:          t.RunnerScaleSetName,
			EphemeralRunnerSetName:      t.EphemeralRunnerSetName,
			EphemeralRunnerSetNamespace: t.EphemeralRunnerSetNamespace,
		})
	}
	return out, nil
}

// -----------------------------------------------------------------
// crTenantSource — list AutoscalingRunnerSet CRs (in-cluster only).
// -----------------------------------------------------------------

// crTenantSource lists AutoscalingRunnerSet CRs from the cluster.
// Uses a non-cached client so we always see the latest spec without
// depending on a controller-runtime informer being wired up.
type crTenantSource struct {
	c         client.Client
	namespace string // optional; "" means cluster-wide List
}

// newCRTenantSource constructs the source from an explicit client.
// Tests inject a sigs.k8s.io/controller-runtime/pkg/client/fake
// client here.
func newCRTenantSource(c client.Client, namespace string) *crTenantSource {
	return &crTenantSource{c: c, namespace: namespace}
}

// newCRTenantSourceFromEnv assembles the production source from
// ctrl.GetConfig() + a fresh non-cached client. Namespace defaults to
// "" (cluster-wide).
func newCRTenantSourceFromEnv(namespace string) (*crTenantSource, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("crTenantSource: get k8s config: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("crTenantSource: register clientgo scheme: %w", err)
	}
	if err := AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("crTenantSource: register actions.github.com scheme: %w", err)
	}
	// Defensive: register corev1 so a future ARS spec change that
	// references it can still be Listed without scheme errors.
	_ = corev1.AddToScheme(scheme)

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("crTenantSource: build client: %w", err)
	}
	return newCRTenantSource(c, namespace), nil
}

func (s *crTenantSource) List(ctx context.Context) ([]Tenant, error) {
	var list AutoscalingRunnerSetList
	opts := []client.ListOption{}
	if s.namespace != "" {
		opts = append(opts, client.InNamespace(s.namespace))
	}
	if err := s.c.List(ctx, &list, opts...); err != nil {
		return nil, fmt.Errorf("list AutoscalingRunnerSets: %w", err)
	}
	out := make([]Tenant, 0, len(list.Items))
	for i := range list.Items {
		ars := &list.Items[i]
		out = append(out, arsToTenant(ars))
	}
	return out, nil
}

// arsToTenant projects an AutoscalingRunnerSet into the wire tenant
// struct. The ARS does not embed an ERS name (the controller uses
// GenerateName at reconcile time) — instead the host's scaler resolves
// the live ERS by the `actions.github.com/scale-set-name` label. We
// pass the ARS name + namespace; the host does the rest.
func arsToTenant(ars *AutoscalingRunnerSet) Tenant {
	name := ars.Spec.RunnerScaleSetName
	if name == "" {
		name = ars.Name
	}
	var minRunners, maxRunners uint32
	if ars.Spec.MinRunners != nil && *ars.Spec.MinRunners >= 0 {
		minRunners = uint32(*ars.Spec.MinRunners)
	}
	if ars.Spec.MaxRunners != nil && *ars.Spec.MaxRunners >= 0 {
		maxRunners = uint32(*ars.Spec.MaxRunners)
	}
	return Tenant{
		Name:                        name,
		Org:                         extractOrg(ars.Spec.GitHubConfigUrl),
		RunnerGroup:                 ars.Spec.RunnerGroup,
		RunnerLabels:                append([]string(nil), ars.Spec.RunnerScaleSetLabels...),
		ConfigureURL:                ars.Spec.GitHubConfigUrl,
		RunnerScaleSetName:          name,
		EphemeralRunnerSetName:      ars.Name,
		EphemeralRunnerSetNamespace: ars.Namespace,
		MinRunners:                  minRunners,
		MaxRunners:                  maxRunners,
	}
}

// -----------------------------------------------------------------
// combinedTenantSource — try CR first, fall back to MultiConfig.
// -----------------------------------------------------------------

type combinedTenantSource struct {
	primary  tenantSource
	fallback tenantSource
}

func newCombinedTenantSource(primary, fallback tenantSource) *combinedTenantSource {
	return &combinedTenantSource{primary: primary, fallback: fallback}
}

func (s *combinedTenantSource) List(ctx context.Context) ([]Tenant, error) {
	if s.primary != nil {
		tenants, err := s.primary.List(ctx)
		if err == nil && len(tenants) > 0 {
			return tenants, nil
		}
		// If primary returned no tenants but no error, that may just
		// mean nothing is installed yet — prefer falling back to the
		// static config rather than returning empty.
		if err != nil {
			// Bubble up unless we have a fallback.
			if s.fallback == nil {
				return nil, err
			}
		}
	}
	if s.fallback != nil {
		return s.fallback.List(ctx)
	}
	return nil, nil
}

// extractOrg pulls the GitHub org / repo path from a configure URL like
// "https://github.com/hanzoai". Best-effort; non-GitHub URLs return "".
func extractOrg(u string) string {
	const prefix = "https://github.com/"
	if len(u) <= len(prefix) || u[:len(prefix)] != prefix {
		return ""
	}
	rest := u[len(prefix):]
	for i, r := range rest {
		if r == '/' {
			return rest[:i]
		}
	}
	return rest
}
