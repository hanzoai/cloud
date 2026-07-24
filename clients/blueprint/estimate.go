// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Pure core of the blueprint estimator: the compose parser, the SBOM extractor
// (service→image bill of materials), the per-service sizing (declared reservations
// or a default footprint per service type), and the compute cost model (a
// documented rate card → cost per hour). Everything here is I/O-free and
// deterministic, so estimate_test.go drives it with inline compose documents — no
// registry, no network — exactly as clients/sbom/parse.go proves out its assemblers.
// blueprint.go is the thin orchestration that resolves a template id to a compose
// and serves these values.
package blueprint

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// ── Rate card (the cost basis the 20% author royalty is taken from) ──────────
//
// Amounts are MICRODOLLARS (µ$; 1 USD = 1_000_000 µ$), the exact integer metering
// unit — cents are too coarse for a sub-cent per-hour service rate, so we keep the
// canonical figure integral in µ$ and derive cents/month for display. The numbers
// are DigitalOcean droplet retail economics (a 4 vCPU / 8 GB regular droplet is
// $48/mo ≈ $0.066/hr) plus the Hanzo platform margin that the author royalty draws
// its 20% from. This is an ESTIMATE and a POLICY default, not a fabricated market
// price: ops tunes the two knobs per deployment via CLOUD_BLUEPRINT_UCPU_HR /
// CLOUD_BLUEPRINT_UGB_HR (see rateCardFromEnv). One constant block, easy to move.
const (
	defaultMicroUSDPerVCPUHour int64 = 12_000 // $0.012/vCPU-hr ≈ $8.76/mo per vCPU
	defaultMicroUSDPerGBHour   int64 = 6_000  // $0.006/GB-hr   ≈ $4.38/mo per GB
	microUSDPerCent            int64 = 10_000 // 1¢ = $0.01 = 10_000 µ$
	hoursPerMonth              int64 = 730    // 365*24/12 — the DO monthly convention
)

// RateCard is the two-knob compute price the estimator applies, carried on every
// Estimate so the response is self-documenting and the metering path reads the
// SAME numbers it displays. Values are microdollars per resource-hour.
type RateCard struct {
	MicroUSDPerVCPUHour int64  `json:"microUsdPerVcpuHour"`
	MicroUSDPerGBHour   int64  `json:"microUsdPerGbHour"`
	Basis               string `json:"basis"`
}

// DefaultRateCard is the shipped rate card (the constants above). rateCardFromEnv
// overlays operator overrides at Mount; the pure core defaults to this so tests
// are hermetic.
func DefaultRateCard() RateCard {
	return RateCard{
		MicroUSDPerVCPUHour: defaultMicroUSDPerVCPUHour,
		MicroUSDPerGBHour:   defaultMicroUSDPerGBHour,
		Basis:               "DigitalOcean droplet retail + Hanzo platform margin (estimate)",
	}
}

// rates is the process rate card, set once in Mount (env overlay) and read by the
// exported EstimateCompose. Defaults to the shipped card so a call before Mount
// (or in a pure test) still prices correctly.
var rates = DefaultRateCard()

// ── SBOM + estimate wire types ───────────────────────────────────────────────

// Service is one row of the bill of materials: the compose service, its container
// image (the SBOM entry), the inferred class, the per-hour footprint applied, and
// whether that footprint was DECLARED in the compose or a class DEFAULT.
type Service struct {
	Name     string  `json:"service"`
	Image    string  `json:"image"`
	Type     string  `json:"type"`     // db | cache | web | worker | other
	VCPU     float64 `json:"vcpu"`     // vCPU applied (× replicas)
	GB       float64 `json:"gb"`       // GB memory applied (× replicas)
	Replicas int     `json:"replicas"` // deploy.replicas, default 1
	Sized    string  `json:"sized"`    // "declared" | "default"
}

// Estimate is the per-template result: the SBOM (bill of container images), the
// summed resource footprint, and the compute cost the deploying org is metered on.
// It is explicitly an ESTIMATE (Estimate: true) — the honest label the UI shows.
type Estimate struct {
	TemplateID      string    `json:"templateId"`
	SBOM            []Service `json:"sbom"`
	VCPUHours       float64   `json:"vcpuHr"`          // total vCPU per hour
	GBHours         float64   `json:"gbHr"`            // total GB per hour
	MicroUSDPerHour int64     `json:"microUsdPerHour"` // exact integer metering unit
	CentsPerHour    float64   `json:"estCentsPerHour"`
	CentsPerMonth   int64     `json:"estCentsPerMonth"` // for the "~$X/mo to run" label
	RateCard        RateCard  `json:"rateCard"`
	Estimate        bool      `json:"estimate"`
}

// ── Compose shapes (only the fields the estimator consumes) ──────────────────

type composeFile struct {
	Services map[string]composeService `json:"services"`
}

type composeService struct {
	Image          string        `json:"image"`
	Deploy         composeDeploy `json:"deploy"`
	CPUs           scalar        `json:"cpus"`            // legacy top-level fractional cores
	MemReservation scalar        `json:"mem_reservation"` // legacy soft memory floor
	MemLimit       scalar        `json:"mem_limit"`       // legacy hard memory cap
	HanzoType      string        `json:"x-hanzo-type"`    // optional explicit class override
}

type composeDeploy struct {
	Replicas  *int             `json:"replicas"`
	Resources composeResources `json:"resources"`
}

type composeResources struct {
	Reservations composeResSpec `json:"reservations"`
	Limits       composeResSpec `json:"limits"`
}

type composeResSpec struct {
	CPUs   scalar `json:"cpus"`
	Memory scalar `json:"memory"`
}

// scalar accepts a compose value that YAML may render as a string ("0.50",
// "512M") OR a bare number (0.5) — sigs.k8s.io/yaml routes through JSON, so this
// UnmarshalJSON sees a quoted string or a raw number and normalizes both to text.
type scalar struct{ s string }

func (v *scalar) UnmarshalJSON(b []byte) error {
	t := strings.TrimSpace(string(b))
	if t == "" || t == "null" {
		v.s = ""
		return nil
	}
	if t[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		v.s = strings.TrimSpace(s)
		return nil
	}
	v.s = t // number/bool token verbatim
	return nil
}

func (v scalar) String() string { return v.s }

// ── Default footprint per service class ──────────────────────────────────────

type footprint struct {
	vcpu float64
	gb   float64
}

// defaultSize is the class footprint applied when a service declares no
// reservations/limits — a sane, documented sizing per the four canonical classes
// plus an "other" fallback. Deliberately modest: a starter deployment, not a
// production HA fleet. Tunable here in ONE place.
var defaultSize = map[string]footprint{
	"db":     {vcpu: 1.0, gb: 2.0},
	"cache":  {vcpu: 0.5, gb: 1.0},
	"web":    {vcpu: 0.5, gb: 0.5},
	"worker": {vcpu: 0.5, gb: 1.0},
	"other":  {vcpu: 0.25, gb: 0.5},
}

// dbHints/cacheHints/webHints/workerHints classify a service by substrings of its
// image (or, failing that, its service name). First match wins in the order
// checked by serviceClass. Kept as data so the mapping is one obvious list.
var (
	dbHints     = []string{"postgres", "mysql", "mariadb", "mongo", "cockroach", "clickhouse", "cassandra", "influxdb", "timescale", "couchdb", "neo4j", "edgedb", "database"}
	cacheHints  = []string{"redis", "memcached", "valkey", "keydb", "dragonfly"}
	webHints    = []string{"nginx", "caddy", "traefik", "httpd", "apache", "envoy", "haproxy", "node", "next", "web", "frontend", "php", "ghost", "wordpress", "django", "rails", "flask"}
	workerHints = []string{"worker", "celery", "sidekiq", "runner", "queue", "rabbitmq", "kafka", "nats", "beanstalk", "temporal", "n8n"}
)

// serviceClass resolves a service's class: an explicit x-hanzo-type override wins,
// then image inference, then service-name inference, then "other".
func serviceClass(name string, svc composeService) string {
	if t := normalizeClass(svc.HanzoType); t != "" {
		return t
	}
	if t := inferClass(svc.Image); t != "other" {
		return t
	}
	if t := inferClass(name); t != "other" {
		return t
	}
	return "other"
}

func normalizeClass(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "db", "cache", "web", "worker", "other":
		return strings.ToLower(strings.TrimSpace(s))
	}
	return ""
}

func inferClass(s string) string {
	l := strings.ToLower(s)
	if containsAny(l, dbHints) {
		return "db"
	}
	if containsAny(l, cacheHints) {
		return "cache"
	}
	if containsAny(l, webHints) {
		return "web"
	}
	if containsAny(l, workerHints) {
		return "worker"
	}
	return "other"
}

func containsAny(s string, hints []string) bool {
	for _, h := range hints {
		if strings.Contains(s, h) {
			return true
		}
	}
	return false
}

// ── Sizing ───────────────────────────────────────────────────────────────────

// sizing returns the per-replica footprint and its provenance: DECLARED when the
// compose carries any cpu/memory hint (reservations win over limits win over the
// legacy top-level keys), else the class DEFAULT. A partial declaration (cpu only,
// or memory only) fills the missing axis from the default and still reads as
// "declared" — the org asked for a specific size on at least one axis.
func sizing(class string, svc composeService) (vcpu, gb float64, provenance string) {
	def := defaultSize[class]
	vcpu, gb, provenance = def.vcpu, def.gb, "default"

	cpu, cpuOK := firstCPU(
		svc.Deploy.Resources.Reservations.CPUs,
		svc.Deploy.Resources.Limits.CPUs,
		svc.CPUs,
	)
	mem, memOK := firstMem(
		svc.Deploy.Resources.Reservations.Memory,
		svc.Deploy.Resources.Limits.Memory,
		svc.MemReservation,
		svc.MemLimit,
	)
	if cpuOK || memOK {
		provenance = "declared"
		if cpuOK {
			vcpu = cpu
		}
		if memOK {
			gb = mem
		}
	}
	return
}

func firstCPU(vals ...scalar) (float64, bool) {
	for _, v := range vals {
		if n, ok := parseCPU(v.s); ok {
			return n, true
		}
	}
	return 0, false
}

func firstMem(vals ...scalar) (float64, bool) {
	for _, v := range vals {
		if n, ok := parseMem(v.s); ok {
			return n, true
		}
	}
	return 0, false
}

// parseCPU reads compose CPU as fractional cores ("0.5", "1", "2") or k8s-style
// millicores ("250m" → 0.25). Zero or unparseable → not-ok, so the caller falls
// back to the class default.
func parseCPU(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.HasSuffix(s, "m") {
		if n, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64); err == nil && n > 0 {
			return n / 1000, true
		}
		return 0, false
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil && n > 0 {
		return n, true
	}
	return 0, false
}

// parseMem reads a compose memory value ("512M", "1g", "2GB", "1073741824") and
// returns GiB. Suffixes are 1024-based (compose convention), case-insensitive, and
// tolerate a trailing "b"/"B". Zero or unparseable → not-ok.
func parseMem(s string) (float64, bool) {
	l := strings.ToLower(strings.TrimSpace(s))
	if l == "" {
		return 0, false
	}
	l = strings.TrimSuffix(l, "b") // "512mb" → "512m"; a bare number is unchanged
	mult := 1.0
	switch {
	case strings.HasSuffix(l, "k"):
		mult, l = 1024, strings.TrimSuffix(l, "k")
	case strings.HasSuffix(l, "m"):
		mult, l = 1024*1024, strings.TrimSuffix(l, "m")
	case strings.HasSuffix(l, "g"):
		mult, l = 1024*1024*1024, strings.TrimSuffix(l, "g")
	case strings.HasSuffix(l, "t"):
		mult, l = 1024*1024*1024*1024, strings.TrimSuffix(l, "t")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(l), 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * mult / (1024 * 1024 * 1024), true // → GiB
}

// ── Estimate assembly ────────────────────────────────────────────────────────

// EstimateCompose parses a compose document and prices it with the process rate
// card. id labels the result (the template id); pass "" for an ad-hoc compose.
// This is the ONE entrypoint the HTTP handlers and the in-process deploy/metering
// seam call.
func EstimateCompose(id string, doc []byte) (Estimate, error) {
	return estimateWith(id, doc, rates)
}

// estimateWith is the pure kernel: parse → per-service SBOM + sizing → summed
// footprint → cost. Deterministic (services sorted by name, floats rounded to a
// fixed precision, cost computed in integer microdollars).
func estimateWith(id string, doc []byte, rc RateCard) (Estimate, error) {
	var cf composeFile
	if err := yaml.Unmarshal(doc, &cf); err != nil {
		return Estimate{}, fmt.Errorf("parse compose: %w", err)
	}
	if len(cf.Services) == 0 {
		return Estimate{}, fmt.Errorf("compose declares no services")
	}

	names := make([]string, 0, len(cf.Services))
	for n := range cf.Services {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic SBOM order

	est := Estimate{
		TemplateID: id,
		SBOM:       make([]Service, 0, len(names)),
		RateCard:   rc,
		Estimate:   true,
	}
	for _, name := range names {
		svc := cf.Services[name]
		class := serviceClass(name, svc)
		vcpu, gb, provenance := sizing(class, svc)

		replicas := 1
		if svc.Deploy.Replicas != nil && *svc.Deploy.Replicas > 0 {
			replicas = *svc.Deploy.Replicas
		}
		vcpu *= float64(replicas)
		gb *= float64(replicas)

		est.SBOM = append(est.SBOM, Service{
			Name:     name,
			Image:    strings.TrimSpace(svc.Image),
			Type:     class,
			VCPU:     round4(vcpu),
			GB:       round4(gb),
			Replicas: replicas,
			Sized:    provenance,
		})
		est.VCPUHours += vcpu
		est.GBHours += gb
	}

	est.VCPUHours = round4(est.VCPUHours)
	est.GBHours = round4(est.GBHours)
	est.MicroUSDPerHour, est.CentsPerHour, est.CentsPerMonth = priceFootprint(est.VCPUHours, est.GBHours, rc)
	return est, nil
}

// priceFootprint applies the rate card to a summed (vCPU-hr, GB-hr) footprint,
// yielding the exact integer microdollars-per-hour plus its derived cents views.
// It is the ONE cost formula, shared by the multi-service template estimator
// (estimateWith) and the single-service running-app estimator (EstimateService),
// so a compose template and a bare deployment of the same footprint are priced
// identically — the metering path reads the same numbers the console displays.
func priceFootprint(vcpuHr, gbHr float64, rc RateCard) (microPerHour int64, centsPerHour float64, centsPerMonth int64) {
	microPerHour = int64(math.Round(vcpuHr*float64(rc.MicroUSDPerVCPUHour) + gbHr*float64(rc.MicroUSDPerGBHour)))
	centsPerHour = float64(microPerHour) / float64(microUSDPerCent)
	centsPerMonth = int64(math.Round(float64(microPerHour) * float64(hoursPerMonth) / float64(microUSDPerCent)))
	return
}

// EstimateService prices ONE running service — a single container image at N
// replicas — with the process rate card. It is the single-container analogue of
// EstimateTemplate: a platform app (clients/platform) is one image at N replicas,
// not a compose stack, so the deploy-compute meter prices a running deployment
// through THIS seam while a multi-service blueprint prices through EstimateTemplate.
// Same kernel — class inferred from the image, class-default footprint × replicas,
// priced by the SAME RateCard via priceFootprint — so a bare deployment and a
// one-service template of the same image cost identically. est.MicroUSDPerHour is
// the exact per-hour figure the deploy path meters the deploying org on (the cost
// basis the 20% author royalty is then taken from). replicas < 1 is treated as 1.
func EstimateService(image string, replicas int) Estimate {
	if replicas < 1 {
		replicas = 1
	}
	image = strings.TrimSpace(image)
	svc := composeService{Image: image}
	class := serviceClass(image, svc) // name=image → the image drives both image- and name-inference
	vcpu, gb, provenance := sizing(class, svc)
	vcpu *= float64(replicas)
	gb *= float64(replicas)
	vcpuHr, gbHr := round4(vcpu), round4(gb)
	micro, centsHr, centsMo := priceFootprint(vcpuHr, gbHr, rates)
	return Estimate{
		TemplateID: image,
		SBOM: []Service{{
			Name: image, Image: image, Type: class,
			VCPU: vcpuHr, GB: gbHr, Replicas: replicas, Sized: provenance,
		}},
		VCPUHours:       vcpuHr,
		GBHours:         gbHr,
		MicroUSDPerHour: micro,
		CentsPerHour:    centsHr,
		CentsPerMonth:   centsMo,
		RateCard:        rates,
		Estimate:        true,
	}
}

// round4 fixes a float to 4 decimals so equality in tests is exact and the JSON is
// clean (a footprint is at most thousandths of a core/GB).
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }
