// Copyright (c) Hanzo AI. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testMultiConfigWithTenants(names ...string) *MultiConfig {
	mc := &MultiConfig{}
	for _, n := range names {
		mc.Tenants = append(mc.Tenants, &TenantConfig{
			RunnerScaleSetName: n,
			ConfigureURL:       "https://github.com/" + n,
			MaxRunners:         4,
		})
	}
	return mc
}

func arsScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sc := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sc); err != nil {
		t.Fatalf("clientgo scheme: %v", err)
	}
	if err := AddToScheme(sc); err != nil {
		t.Fatalf("actions scheme: %v", err)
	}
	return sc
}

func intPtr(v int) *int { return &v }

// TestCRTenantSource_ListsAllNamespaces — cluster-wide listing
// surfaces ARS from any namespace.
func TestCRTenantSource_ListsAllNamespaces(t *testing.T) {
	a := &AutoscalingRunnerSet{
		ObjectMeta: metav1.ObjectMeta{Name: "hanzoai-arm64", Namespace: "ns-a"},
		Spec: AutoscalingRunnerSetSpec{
			GitHubConfigUrl:    "https://github.com/hanzoai",
			RunnerScaleSetName: "hanzoai-arm64",
			RunnerGroup:        "default",
			MinRunners:         intPtr(0),
			MaxRunners:         intPtr(4),
		},
	}
	b := &AutoscalingRunnerSet{
		ObjectMeta: metav1.ObjectMeta{Name: "luxfi-amd64", Namespace: "ns-b"},
		Spec: AutoscalingRunnerSetSpec{
			GitHubConfigUrl: "https://github.com/luxfi/zap",
			RunnerGroup:     "amd64",
			MinRunners:      intPtr(1),
			MaxRunners:      intPtr(8),
		},
	}
	c := fake.NewClientBuilder().WithScheme(arsScheme(t)).WithObjects(a, b).Build()

	src := newCRTenantSource(c, "")
	tenants, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("len(tenants) = %d, want 2", len(tenants))
	}
	// Order is List-stable but namespace-collated. Sort by name for
	// deterministic assertion.
	sort.Slice(tenants, func(i, j int) bool { return tenants[i].Name < tenants[j].Name })

	if tenants[0].Name != "hanzoai-arm64" {
		t.Fatalf("tenant 0 name = %q, want hanzoai-arm64", tenants[0].Name)
	}
	if tenants[0].Org != "hanzoai" {
		t.Fatalf("tenant 0 org = %q", tenants[0].Org)
	}
	if tenants[0].EphemeralRunnerSetNamespace != "ns-a" {
		t.Fatalf("tenant 0 ERS namespace = %q, want ns-a", tenants[0].EphemeralRunnerSetNamespace)
	}
	if tenants[0].MaxRunners != 4 || tenants[0].MinRunners != 0 {
		t.Fatalf("tenant 0 min/max = %d/%d", tenants[0].MinRunners, tenants[0].MaxRunners)
	}

	if tenants[1].Name != "luxfi-amd64" {
		t.Fatalf("tenant 1 name = %q, want luxfi-amd64 (defaulted from object name)", tenants[1].Name)
	}
	if tenants[1].Org != "luxfi" {
		t.Fatalf("tenant 1 org = %q", tenants[1].Org)
	}
	if tenants[1].MaxRunners != 8 || tenants[1].MinRunners != 1 {
		t.Fatalf("tenant 1 min/max = %d/%d", tenants[1].MinRunners, tenants[1].MaxRunners)
	}
	if tenants[1].EphemeralRunnerSetNamespace != "ns-b" {
		t.Fatalf("tenant 1 ERS namespace = %q, want ns-b", tenants[1].EphemeralRunnerSetNamespace)
	}
}

// TestCRTenantSource_NamespaceScoped — when scoped to a single
// namespace, ARS in other namespaces are invisible.
func TestCRTenantSource_NamespaceScoped(t *testing.T) {
	a := &AutoscalingRunnerSet{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "ns-a"},
		Spec: AutoscalingRunnerSetSpec{
			GitHubConfigUrl:    "https://github.com/hanzoai",
			RunnerScaleSetName: "alpha",
			MinRunners:         intPtr(0),
			MaxRunners:         intPtr(2),
		},
	}
	b := &AutoscalingRunnerSet{
		ObjectMeta: metav1.ObjectMeta{Name: "beta", Namespace: "ns-b"},
		Spec: AutoscalingRunnerSetSpec{
			GitHubConfigUrl:    "https://github.com/luxfi",
			RunnerScaleSetName: "beta",
			MaxRunners:         intPtr(4),
		},
	}
	c := fake.NewClientBuilder().WithScheme(arsScheme(t)).WithObjects(a, b).Build()

	src := newCRTenantSource(c, "ns-a")
	tenants, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tenants) != 1 {
		t.Fatalf("expected 1 tenant from ns-a only, got %d (%+v)", len(tenants), tenants)
	}
	if tenants[0].Name != "alpha" {
		t.Fatalf("tenant = %q, want alpha", tenants[0].Name)
	}
}

// TestCRTenantSource_AddedTenantVisibleOnNextList — proves that adding
// an ARS to the cluster shows up on the next List() without restart.
// This is the property the host depends on for hot-reconfig.
func TestCRTenantSource_AddedTenantVisibleOnNextList(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(arsScheme(t)).Build()
	src := newCRTenantSource(c, "")

	// Initially empty.
	tenants, err := src.List(ctx)
	if err != nil {
		t.Fatalf("initial List: %v", err)
	}
	if len(tenants) != 0 {
		t.Fatalf("initial: expected 0 tenants, got %d", len(tenants))
	}

	// Add one.
	newARS := &AutoscalingRunnerSet{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "ns-c"},
		Spec: AutoscalingRunnerSetSpec{
			GitHubConfigUrl:    "https://github.com/zooai",
			RunnerScaleSetName: "fresh",
			MaxRunners:         intPtr(1),
		},
	}
	if err := c.Create(ctx, newARS); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Second list sees it.
	tenants, err = src.List(ctx)
	if err != nil {
		t.Fatalf("second List: %v", err)
	}
	if len(tenants) != 1 {
		t.Fatalf("after add: expected 1 tenant, got %d", len(tenants))
	}
	if tenants[0].Name != "fresh" {
		t.Fatalf("tenant = %q, want fresh", tenants[0].Name)
	}
}

// TestCombinedTenantSource_FallsBackWhenPrimaryEmpty proves the
// combined source uses the fallback when the CR list is empty.
func TestCombinedTenantSource_FallsBackWhenPrimaryEmpty(t *testing.T) {
	primary := newCRTenantSource(fake.NewClientBuilder().WithScheme(arsScheme(t)).Build(), "")
	fallback := newMCTenantSource(testMultiConfigWithTenants("hanzoai-arm64"))
	src := newCombinedTenantSource(primary, fallback)

	tenants, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tenants) != 1 {
		t.Fatalf("expected 1 tenant from fallback, got %d", len(tenants))
	}
	if tenants[0].Name != "hanzoai-arm64" {
		t.Fatalf("tenant = %q, want hanzoai-arm64", tenants[0].Name)
	}
}

// TestCombinedTenantSource_PreferPrimary proves the combined source
// returns primary's view when it is non-empty.
func TestCombinedTenantSource_PreferPrimary(t *testing.T) {
	ars := &AutoscalingRunnerSet{
		ObjectMeta: metav1.ObjectMeta{Name: "from-cluster", Namespace: "ns"},
		Spec: AutoscalingRunnerSetSpec{
			GitHubConfigUrl:    "https://github.com/hanzoai",
			RunnerScaleSetName: "from-cluster",
			MaxRunners:         intPtr(4),
		},
	}
	primary := newCRTenantSource(fake.NewClientBuilder().WithScheme(arsScheme(t)).WithObjects(ars).Build(), "")
	fallback := newMCTenantSource(testMultiConfigWithTenants("hanzoai-arm64"))
	src := newCombinedTenantSource(primary, fallback)

	tenants, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tenants) != 1 {
		t.Fatalf("expected 1 tenant from primary, got %d", len(tenants))
	}
	if tenants[0].Name != "from-cluster" {
		t.Fatalf("tenant = %q, want from-cluster", tenants[0].Name)
	}
}
