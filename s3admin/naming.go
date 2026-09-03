// Copyright (c) 2026 Hanzo AI Inc.

package s3admin

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// How a tenant's resource becomes a physical name on a shared backend.
//
// This lives in the LEAF package because three callers need the same answer and
// a second derivation is how two orgs fold onto one bucket. apps/provisioning
// had it first; apps/s3 reads it from there; apps/space needs it too and cannot
// take it the same way — importing provisioning costs 105 extra packages,
// including the whole client-go serializer tree, for six lines of string
// handling.
//
// The fixed-width hash is the part that matters. Joining org and name with a
// separator folds the boundary: "acme"+"my-db" and "acme-my"+"db" produced the
// SAME identifier, which is a cross-tenant collision. A 64-bit hash of the org,
// always 16 characters, cannot be confused with the name that follows it.

// OrgHash is the fixed-width tenant discriminator: 64 bits of SHA-256, hex.
func OrgHash(org string) string {
	sum := sha256.Sum256([]byte(org))
	return hex.EncodeToString(sum[:])[:16]
}

// Ident folds '-' to '_', the only non-alphanumeric a validated name may carry.
// Valid names never contain '_', so the fold round-trips and stays injective.
func Ident(name string) string { return strings.ReplaceAll(name, "-", "_") }

// PhysicalName namespaces a resource as "o"<OrgHash>_<Ident>. The leading 'o'
// keeps it alpha-initial, which every backend accepts as an identifier. With a
// name of 40 characters or fewer the result is at most 58 — inside both S3's
// 63-character bucket limit and Postgres's 63-character identifier limit.
func PhysicalName(org, name string) string {
	return "o" + OrgHash(org) + "_" + Ident(name)
}

// BucketName is the physical bucket a tenant's named resource lives in.
func BucketName(org, name string) string { return bucket(PhysicalName(org, name)) }

// BucketPrefix is the string every one of a tenant's buckets begins with, which
// is what makes a listing filterable back to one org.
func BucketPrefix(org string) string { return bucket("o"+OrgHash(org)) + "-" }

// bucket renders a physical name in the alphabet S3 accepts.
func bucket(physical string) string {
	b := strings.Trim(strings.ToLower(strings.ReplaceAll(physical, "_", "-")), "-")
	if len(b) > 63 { // unreachable for a bounded name; defensive.
		b = strings.Trim(b[:63], "-")
	}
	return b
}
