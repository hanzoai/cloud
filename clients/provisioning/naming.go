// Package provisioning carries the tenant→backend naming convention shared by
// every subsystem that allocates a per-org physical resource on a shared backend
// (clients/storage, clients/do). It is the ONE place the (org,name)→physical-id
// and (org,name)→S3-bucket mappings are defined, so a bucket created by one
// subsystem is found by another under the identical name.
//
// The OSS core ships ONLY this naming surface. The full provisioning control
// plane (dedicated-instance orchestration, per-org KMS-sealed credentials, usage
// metering) is a private-SaaS concern and is not part of the OSS binary.
package provisioning

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/hanzoai/cloud"
)

// orgHash returns a fixed-width, collision-resistant tag for an org slug: the
// first 16 hex chars (64 bits) of SHA-256(org). The FIXED WIDTH makes the
// org→name boundary in physicalName/bucketName unambiguous, so two distinct
// orgs can never fold onto one backend resource.
func orgHash(org string) string {
	sum := sha256.Sum256([]byte(org))
	return hex.EncodeToString(sum[:])[:16]
}

// sanitizeIdent reduces a validated resource name to a [a-z0-9_] identifier by
// folding '-' (the only non-alphanumeric a valid name may contain) to '_'.
func sanitizeIdent(name string) string { return strings.ReplaceAll(name, "-", "_") }

// sanitizeOrg exports the org-slug normalizer via the ONE canonical
// implementation (cloud.SanitizeOrg), so every backend keys on the same reduced
// slug.
func sanitizeOrg(s string) string { return cloud.SanitizeOrg(s) }

// physicalName namespaces a resource on a shared backend as
// "o"<orgHash>_<sanitizedName>. Injective in (org,name) up to a 64-bit SHA-256
// collision.
func physicalName(org, name string) string {
	return "o" + orgHash(org) + "_" + sanitizeIdent(name)
}

// bucketName maps a physical resource id to a DNS-safe S3 bucket name.
func bucketName(physical string) string {
	b := strings.Trim(strings.ToLower(strings.ReplaceAll(physical, "_", "-")), "-")
	if len(b) > 63 { // unreachable for nameRE-bounded input (physical ≤ 58); defensive.
		b = strings.Trim(b[:63], "-")
	}
	return b
}

// BucketName is the tenant→S3-bucket name for (org, friendly-name).
func BucketName(org, name string) string { return bucketName(physicalName(org, name)) }

// BucketPrefix is the S3-bucket-name prefix ALL of an org's buckets share.
func BucketPrefix(org string) string { return bucketName("o"+orgHash(org)) + "-" }

// SanitizeOrg exports the org-slug normalizer so a caller derives the caller's
// org tag from the SAME reduced slug the naming convention keys on.
func SanitizeOrg(org string) string { return sanitizeOrg(org) }
