// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package blueprint

import (
	"testing"
)

// TestEstimateInlineCompose drives the pure extractor + cost model with an inline
// compose that exercises every sizing path in ONE document: a declared reservation
// (web), two class defaults (db, cache), and a replica-scaled worker. The expected
// SBOM and the exact cost are hand-computed from the rate card, so a drift in the
// parser, the defaults, or the pricing math trips here.
func TestEstimateInlineCompose(t *testing.T) {
	doc := []byte(`
services:
  api:
    image: nginx:1.27
    deploy:
      resources:
        reservations:
          cpus: "0.5"
          memory: 512M
  db:
    image: postgres:16
  cache:
    image: redis:7
  jobs:
    image: myco/worker:1
    deploy:
      replicas: 3
`)
	est, err := EstimateCompose("inline", doc)
	if err != nil {
		t.Fatalf("EstimateCompose: %v", err)
	}

	// SBOM is sorted by service name: api, cache, db, jobs.
	want := []Service{
		{Name: "api", Image: "nginx:1.27", Type: "web", VCPU: 0.5, GB: 0.5, Replicas: 1, Sized: "declared"},
		{Name: "cache", Image: "redis:7", Type: "cache", VCPU: 0.5, GB: 1.0, Replicas: 1, Sized: "default"},
		{Name: "db", Image: "postgres:16", Type: "db", VCPU: 1.0, GB: 2.0, Replicas: 1, Sized: "default"},
		{Name: "jobs", Image: "myco/worker:1", Type: "worker", VCPU: 1.5, GB: 3.0, Replicas: 3, Sized: "default"},
	}
	if len(est.SBOM) != len(want) {
		t.Fatalf("SBOM len = %d, want %d (%+v)", len(est.SBOM), len(want), est.SBOM)
	}
	for i, w := range want {
		if est.SBOM[i] != w {
			t.Errorf("SBOM[%d] = %+v, want %+v", i, est.SBOM[i], w)
		}
	}

	// Totals: vCPU 0.5+0.5+1.0+1.5 = 3.5 ; GB 0.5+1.0+2.0+3.0 = 6.5.
	if est.VCPUHours != 3.5 || est.GBHours != 6.5 {
		t.Fatalf("totals vcpu=%v gb=%v, want 3.5 / 6.5", est.VCPUHours, est.GBHours)
	}
	// Cost: 3.5*12000 + 6.5*6000 = 42000 + 39000 = 81000 µ$/hr.
	if est.MicroUSDPerHour != 81000 {
		t.Fatalf("microUSDPerHour = %d, want 81000", est.MicroUSDPerHour)
	}
	if est.CentsPerHour != 8.1 {
		t.Fatalf("centsPerHour = %v, want 8.1", est.CentsPerHour)
	}
	// Monthly: round(81000 * 730 / 10000) = 5913¢ = $59.13/mo.
	if est.CentsPerMonth != 5913 {
		t.Fatalf("centsPerMonth = %d, want 5913", est.CentsPerMonth)
	}
	if !est.Estimate {
		t.Fatal("Estimate flag must be true (it is an estimate)")
	}
	if est.RateCard != DefaultRateCard() {
		t.Fatalf("rate card = %+v, want default", est.RateCard)
	}
}

// TestLegacyAndPartialDeclaration proves the legacy top-level keys (cpus/mem_limit/
// mem_reservation) are honored, and that a memory-only declaration fills cpu from
// the class default while still reading as "declared".
func TestLegacyAndPartialDeclaration(t *testing.T) {
	doc := []byte(`
services:
  web:
    image: wordpress:6
    cpus: "0.5"
    mem_limit: 512m
  data:
    image: mariadb:11
    mem_reservation: 1g
`)
	est, err := EstimateCompose("legacy", doc)
	if err != nil {
		t.Fatalf("EstimateCompose: %v", err)
	}
	byName := map[string]Service{}
	for _, s := range est.SBOM {
		byName[s.Name] = s
	}
	if got := byName["web"]; got.VCPU != 0.5 || got.GB != 0.5 || got.Sized != "declared" {
		t.Errorf("web = %+v, want 0.5/0.5 declared", got)
	}
	// data declares memory only → cpu falls back to db default (1.0), still declared.
	if got := byName["data"]; got.VCPU != 1.0 || got.GB != 1.0 || got.Sized != "declared" || got.Type != "db" {
		t.Errorf("data = %+v, want db 1.0/1.0 declared", got)
	}
}

// TestExplicitTypeOverride proves x-hanzo-type wins over image/name inference.
func TestExplicitTypeOverride(t *testing.T) {
	doc := []byte(`
services:
  thing:
    image: myco/custom:1
    x-hanzo-type: db
`)
	est, err := EstimateCompose("override", doc)
	if err != nil {
		t.Fatalf("EstimateCompose: %v", err)
	}
	if est.SBOM[0].Type != "db" || est.SBOM[0].VCPU != 1.0 || est.SBOM[0].GB != 2.0 {
		t.Fatalf("override = %+v, want db 1.0/2.0", est.SBOM[0])
	}
}

// TestEstimateService proves the single-service seam the deploy-compute meter
// uses: a running platform app is one image at N replicas, priced by the SAME
// rate card + class inference as a compose template, so EstimateService and a
// one-service EstimateCompose of the same image agree exactly. The per-hour
// microdollar figures are hand-computed from the default rate card (12000 µ$/vCPU-hr,
// 6000 µ$/GB-hr) — the exact number the org is metered per running hour.
func TestEstimateService(t *testing.T) {
	for _, tc := range []struct {
		name     string
		image    string
		replicas int
		class    string
		micro    int64 // µ$/hr
		month    int64 // ¢/mo
	}{
		// db default 1.0 vCPU / 2.0 GB → 1*12000 + 2*6000 = 24000 µ$/hr; round(24000*730/10000)=1752¢.
		{"postgres single", "postgres:16", 1, "db", 24000, 1752},
		// cache default 0.5 / 1.0 → 0.5*12000 + 1*6000 = 12000 µ$/hr; round(12000*730/10000)=876¢.
		{"redis single", "redis:7", 1, "cache", 12000, 876},
		// unknown image → "other" 0.25 / 0.5 → 0.25*12000 + 0.5*6000 = 6000 µ$/hr.
		{"generic app", "ghcr.io/acme/api:v1", 1, "other", 6000, 438},
		// replicas scale linearly: db × 3 = 3.0 / 6.0 → 72000 µ$/hr.
		{"postgres x3", "postgres:16", 3, "db", 72000, 5256},
		// replicas < 1 is clamped to 1 (never free by a zero-replica record).
		{"zero replicas clamps to 1", "postgres:16", 0, "db", 24000, 1752},
	} {
		t.Run(tc.name, func(t *testing.T) {
			est := EstimateService(tc.image, tc.replicas)
			if est.MicroUSDPerHour != tc.micro {
				t.Fatalf("MicroUSDPerHour = %d, want %d", est.MicroUSDPerHour, tc.micro)
			}
			if est.CentsPerMonth != tc.month {
				t.Fatalf("CentsPerMonth = %d, want %d", est.CentsPerMonth, tc.month)
			}
			if len(est.SBOM) != 1 {
				t.Fatalf("SBOM len = %d, want 1", len(est.SBOM))
			}
			if est.SBOM[0].Type != tc.class || est.SBOM[0].Image != tc.image {
				t.Fatalf("SBOM[0] = %+v, want type %q image %q", est.SBOM[0], tc.class, tc.image)
			}
			if !est.Estimate {
				t.Fatal("Estimate flag must be true")
			}
		})
	}

	// Parity: EstimateService(image,1) equals a one-service compose of the same image.
	svc := EstimateService("postgres:16", 1)
	comp, err := EstimateCompose("one", []byte("services:\n  postgres:16:\n    image: postgres:16\n"))
	if err != nil {
		t.Fatalf("EstimateCompose: %v", err)
	}
	if svc.MicroUSDPerHour != comp.MicroUSDPerHour {
		t.Fatalf("EstimateService µ$/hr %d != one-service compose %d", svc.MicroUSDPerHour, comp.MicroUSDPerHour)
	}
}

func TestEmptyAndMalformed(t *testing.T) {
	if _, err := EstimateCompose("empty", []byte("volumes:\n  x:\n")); err == nil {
		t.Fatal("compose with no services must error")
	}
	if _, err := EstimateCompose("bad", []byte("::: not yaml :::")); err == nil {
		t.Fatal("malformed yaml must error")
	}
}

func TestParseCPU(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"0.5", 0.5, true},
		{"1", 1, true},
		{"2.0", 2, true},
		{"250m", 0.25, true},
		{"1000m", 1, true},
		{"", 0, false},
		{"0", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, ok := parseCPU(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseCPU(%q) = %v,%v want %v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseMem(t *testing.T) {
	cases := []struct {
		in   string
		want float64 // GiB
		ok   bool
	}{
		{"512M", 0.5, true},
		{"512m", 0.5, true},
		{"1g", 1, true},
		{"2G", 2, true},
		{"2GB", 2, true},
		{"1024M", 1, true},
		{"1073741824", 1, true}, // bytes
		{"", 0, false},
		{"0", 0, false},
		{"xyz", 0, false},
	}
	for _, c := range cases {
		got, ok := parseMem(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseMem(%q) = %v,%v want %v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestInferClass(t *testing.T) {
	cases := map[string]string{
		"postgres:16-alpine": "db",
		"mysql:8":            "db",
		"redis:7":            "cache",
		"valkey/valkey:8":    "cache",
		"nginx:1.27":         "web",
		"ghost:5":            "web",
		"n8nio/n8n:1.60.1":   "worker",
		"myco/thing:1":       "other",
	}
	for img, want := range cases {
		if got := inferClass(img); got != want {
			t.Errorf("inferClass(%q) = %q, want %q", img, got, want)
		}
	}
}
