// Package ossattr is the pure, deterministic attribution core of the
// SBOM-driven OSS-developer payout system. It answers exactly one question:
//
//	Given an org's Hanzo cloud spend and the set of OSS packages present in
//	the SBOM(s) of what that org runs, how much of that spend is owed to each
//	OSS package — under a capped, weighted, pro-rata policy?
//
// It is a LEAF package: it imports only the standard library, so the
// attribution math is independent of commerce server internals, the
// datastore, ZAP, or HTTP. That makes the model the one place the fairness
// rules live, and makes it exhaustively testable as a pure function.
//
// Design (one way, decomplected):
//
//   - The POOL is a fraction of spend, hard-capped at 25%. Hanzo keeps the
//     rest. The cap is the headline promise ("up to 25% of all Hanzo cloud
//     costs go to upstream OSS") and is enforced here, in code, not by
//     convention. Pool = spend * min(PoolFraction, MaxPoolFraction).
//
//   - Each package gets a WEIGHT from the policy. There is exactly one
//     mechanism — pro-rata by weight — and the policy supplies the weights.
//     Direct vs transitive is not a special case baked into the mechanism; it
//     is two numbers in the policy (DirectWeight, TransitiveWeight). A
//     per-package criticality multiplier is also just data. This keeps the
//     mechanism (split a pool pro-rata) orthogonal to the fairness knobs.
//
//   - A package's share = Pool * (weight_i / Σ weight_j).
//
//   - The split is in integer cents and conserves the pool exactly: the sum
//     of allocated cents equals the pool to the last cent (largest-remainder
//     apportionment), so no money is created or lost to rounding.
//
// The function is total and deterministic: identical inputs always produce
// identical outputs, including a stable ordering, so the same accrual is
// reproducible and auditable.
package ossattr

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"sort"
	"strings"
)

// MaxPoolFraction is the absolute ceiling on the OSS pool: at most 25% of an
// org's Hanzo cloud spend is ever attributed to OSS dependencies. Any policy
// requesting more is clamped to this value. This constant IS the "up to 25%"
// promise; it lives in code so it cannot silently drift.
const MaxPoolFraction = 0.25

// Scope classifies how a package entered the dependency graph. A direct
// dependency is declared by a deployed Hanzo service; a transitive dependency
// is pulled in by another dependency. The two carry different default weights
// (a direct dep is, by default, more load-bearing per-package than any one of
// the many transitive deps beneath it), but both are paid.
type Scope string

const (
	ScopeDirect     Scope = "direct"
	ScopeTransitive Scope = "transitive"
)

// Package is one OSS dependency observed in an SBOM. It is identified by its
// Package URL (PURL, https://github.com/package-url/purl-spec), the ecosystem-
// neutral coordinate emitted by CycloneDX/SPDX (e.g.
// "pkg:golang/github.com/gin-gonic/gin@v1.12.0",
// "pkg:npm/react@18.3.1", "pkg:pypi/fastapi@0.110.0"). The PURL is the join
// key across SBOMs, the accrual ledger, and the maintainer resolver.
type Package struct {
	PURL        string  // canonical Package URL — the identity
	Name        string  // human name, e.g. "github.com/gin-gonic/gin"
	Ecosystem   string  // "golang" | "npm" | "pypi" | "apk" | "deb" | ...
	Version     string  // resolved version, e.g. "v1.12.0"
	Scope       Scope   // direct | transitive
	Criticality float64 // policy multiplier; 0 or negative means "use 1.0"
}

// Policy parameterizes attribution. All knobs are data; the mechanism is fixed.
// The zero Policy is NOT valid — callers should start from DefaultPolicy and
// override. PoolFraction is clamped to [0, MaxPoolFraction].
type Policy struct {
	// PoolFraction is the share of spend allocated to the OSS pool, e.g. 0.25
	// for the full 25%. Clamped to MaxPoolFraction. A lower value lets Hanzo
	// ramp the program up (e.g. 0.10) without ever exceeding the promise.
	PoolFraction float64

	// DirectWeight and TransitiveWeight are the base per-package weights by
	// scope. Defaults: direct = 1.0, transitive = 0.25. A package's final
	// weight is base(scope) * criticality.
	DirectWeight     float64
	TransitiveWeight float64
}

// DefaultPolicy is the canonical starting policy: the full 25% pool, direct
// deps weighted 4x a transitive dep, criticality off (1.0) by default.
func DefaultPolicy() Policy {
	return Policy{
		PoolFraction:     MaxPoolFraction,
		DirectWeight:     1.0,
		TransitiveWeight: 0.25,
	}
}

// effectivePoolFraction returns the policy's pool fraction clamped to the
// permitted range [0, MaxPoolFraction]. This is where the 25% cap bites.
func (p Policy) effectivePoolFraction() float64 {
	f := p.PoolFraction
	if f < 0 {
		return 0
	}
	if f > MaxPoolFraction {
		return MaxPoolFraction
	}
	return f
}

// weightOf returns the final weight for a package under this policy:
// base(scope) * criticality, with criticality defaulting to 1.0 and direct as
// the default scope. Weights are clamped non-negative.
func (p Policy) weightOf(pkg Package) float64 {
	base := p.DirectWeight
	if pkg.Scope == ScopeTransitive {
		base = p.TransitiveWeight
	}
	if base < 0 {
		base = 0
	}
	crit := pkg.Criticality
	if crit <= 0 {
		crit = 1.0
	}
	return base * crit
}

// Allocation is the attribution result for one package: how many cents of the
// pool it earns, plus the inputs that produced it (for a fully auditable
// ledger line — anyone can recompute share = weight/totalWeight * pool).
type Allocation struct {
	PURL       string
	Name       string
	Ecosystem  string
	Version    string
	Scope      Scope
	Weight     float64 // final weight used (base*criticality)
	ShareRatio float64 // weight / totalWeight, in [0,1]
	Cents      int64   // attributed amount, integer cents (apportioned)
}

// Result is the full attribution of one spend amount across a package set.
type Result struct {
	SpendCents   int64        // the input spend
	PoolFraction float64      // the effective (clamped) pool fraction applied
	PoolCents    int64        // spend * poolFraction, floored — the amount split
	TotalWeight  float64      // Σ weights over all attributable packages
	Allocations  []Allocation // per-package, sorted by PURL (stable)
	// UnallocatedCents is pool cents that could not be attributed (no packages
	// or zero total weight). It is held, not lost — the caller routes it to a
	// held pool. Always 0 when there is at least one positively-weighted pkg.
	UnallocatedCents int64
}

// poolCents computes the integer-cent pool: floor(spend * fraction). Uses
// big.Int to avoid float drift on large spends — the pool is exact.
func poolCents(spendCents int64, fraction float64) int64 {
	if spendCents <= 0 || fraction <= 0 {
		return 0
	}
	// fraction has at most a few decimal places in practice, but to stay exact
	// and deterministic we scale by 1e9 and do integer arithmetic.
	const scale = 1_000_000_000
	num := new(big.Int).Mul(big.NewInt(spendCents), big.NewInt(int64(fraction*scale+0.5)))
	num.Div(num, big.NewInt(scale))
	return num.Int64()
}

// Attribute is the heart of the system: a pure, total, deterministic function
// from (spend, packages, policy) to per-package cent allocations.
//
// Contract:
//   - Pool = floor(spendCents * clamp(policy.PoolFraction, 0, 25%)).
//   - Each package weight = base(scope) * criticality.
//   - share_i = pool * weight_i / Σ weight_j, apportioned to integer cents by
//     largest remainder so Σ allocated cents == pool exactly.
//   - Packages are de-duplicated by PURL (the highest-weight instance wins;
//     ties keep the first seen) so the same dep across multiple SBOMs is paid
//     once. Duplicate handling is deterministic.
//   - With no packages, or all weights zero, the whole pool is Unallocated
//     (held), nothing is invented.
//
// Allocations are returned sorted by PURL for stable, reproducible output.
func Attribute(spendCents int64, pkgs []Package, policy Policy) Result {
	frac := policy.effectivePoolFraction()
	pool := poolCents(spendCents, frac)

	res := Result{
		SpendCents:   spendCents,
		PoolFraction: frac,
		PoolCents:    pool,
	}
	if pool <= 0 {
		return res
	}

	// De-duplicate by PURL, keeping the highest final weight. This makes the
	// union of SBOMs (an org may run several services) pay each package once.
	type acc struct {
		pkg    Package
		weight float64
	}
	byPURL := make(map[string]acc, len(pkgs))
	for _, p := range pkgs {
		key := canonicalPURL(p.PURL)
		if key == "" {
			continue
		}
		w := policy.weightOf(p)
		if cur, ok := byPURL[key]; ok {
			if w > cur.weight {
				byPURL[key] = acc{pkg: p, weight: w}
			}
			continue
		}
		byPURL[key] = acc{pkg: p, weight: w}
	}

	// Stable order: sort keys.
	keys := make([]string, 0, len(byPURL))
	var total float64
	for k, a := range byPURL {
		keys = append(keys, k)
		total += a.weight
	}
	sort.Strings(keys)
	res.TotalWeight = total

	if total <= 0 {
		// No positive weight anywhere — hold the whole pool.
		res.UnallocatedCents = pool
		return res
	}

	// Largest-remainder apportionment of `pool` cents across weights, so the
	// sum of integer allocations equals `pool` exactly (no rounding leak).
	type rem struct {
		idx       int
		remainder float64
	}
	res.Allocations = make([]Allocation, len(keys))
	allocated := int64(0)
	remainders := make([]rem, len(keys))
	for i, k := range keys {
		a := byPURL[k]
		exact := float64(pool) * a.weight / total
		floorC := int64(exact)
		remainders[i] = rem{idx: i, remainder: exact - float64(floorC)}
		allocated += floorC
		res.Allocations[i] = Allocation{
			PURL:       k,
			Name:       a.pkg.Name,
			Ecosystem:  a.pkg.Ecosystem,
			Version:    a.pkg.Version,
			Scope:      scopeOr(a.pkg.Scope),
			Weight:     a.weight,
			ShareRatio: a.weight / total,
			Cents:      floorC,
		}
	}

	// Distribute the leftover cents (pool - allocated) to the largest
	// remainders, deterministically (PURL as tiebreak via stable idx order).
	leftover := pool - allocated
	if leftover > 0 {
		sort.SliceStable(remainders, func(a, b int) bool {
			if remainders[a].remainder != remainders[b].remainder {
				return remainders[a].remainder > remainders[b].remainder
			}
			return remainders[a].idx < remainders[b].idx
		})
		for i := int64(0); i < leftover; i++ {
			res.Allocations[remainders[i].idx].Cents++
		}
	}

	return res
}

// canonicalPURL normalizes a PURL for use as a join key: trims whitespace and
// lowercases the scheme/type portion. PURLs are case-sensitive in the name but
// the "pkg:" scheme and type are lowercase by spec; we trim only.
func canonicalPURL(purl string) string {
	return strings.TrimSpace(purl)
}

func scopeOr(s Scope) Scope {
	if s == ScopeTransitive {
		return ScopeTransitive
	}
	return ScopeDirect
}

// AccrualID derives a stable, deterministic idempotency key for an accrual
// line so the same (org, spend transaction, package) is never double-accrued.
// It hashes the tuple; callers store it as the accrual's idempotency key.
func AccrualID(orgID, txnID, purl string) string {
	h := sha256.New()
	for _, s := range []string{orgID, "\x00", txnID, "\x00", canonicalPURL(purl)} {
		h.Write([]byte(s))
	}
	sum := h.Sum(nil)
	// 128-bit hex is plenty for an idempotency key.
	return "ossac_" + encodeHex(sum[:16])
}

const hexdigits = "0123456789abcdef"

func encodeHex(b []byte) string {
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}

// ensure binary import is used (defensive: keeps the import meaningful if the
// hash encoding is later changed to a numeric form).
var _ = binary.BigEndian
