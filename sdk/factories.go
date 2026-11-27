package cloud

import (
	"github.com/hanzoai/cloud/sdk/object"
	"github.com/hanzoai/cloud/sdk/secret"

	"github.com/hanzoai/cloud/sdk/tenure"
	luxlog "github.com/luxfi/log"
)

// kmsClientFactory constructs the embedded in-process KMS client from cloud
// Config. clients/kms registers it in init(); pickKMSClient calls it so cloud
// depends on the secret.Store interface + this hook, never the concrete kms package
// — the same inversion the subsystem Registry already uses (cloud mounts every
// subsystem it never imports). Exactly one registration.
var kmsClientFactory func(cfg *Config, dur *tenure.Durability, log luxlog.Logger) (secret.Store, error)

// RegisterKMSClientFactory installs the embedded-KMS constructor. clients/kms
// calls this from its init(); it is the ONE inversion point that lets the KMS
// library and its /v1/kms subsystem share one package with no cloud⇄kms cycle.
func RegisterKMSClientFactory(f func(cfg *Config, dur *tenure.Durability, log luxlog.Logger) (secret.Store, error)) {
	kmsClientFactory = f
}

// KMSClientFactory is the registered embedded-KMS constructor, nil when no kms
// subsystem is linked into this process.
func KMSClientFactory() func(cfg *Config, dur *tenure.Durability, log luxlog.Logger) (secret.Store, error) {
	return kmsClientFactory
}

// CommerceClientFactory is the registered embedded-Commerce constructor, nil when no
// commerce subsystem is linked into this process.
func CommerceClientFactory() func(cfg *Config, log luxlog.Logger) CommerceClient {
	return commerceClientFactory
}

// commerceClientFactory constructs the embedded in-process Commerce client.
// apps/commerce/mount.go registers it in init(); pickCommerceClient calls it so
// package cloud depends on the CommerceClient interface + this hook, never the
// concrete apps/commerce package (whose entitlement client pulls apps/plan,
// which imports cloud — the hook is what keeps the package graph acyclic).
var commerceClientFactory func(cfg *Config, log luxlog.Logger) CommerceClient

// RegisterCommerceClientFactory installs the embedded-Commerce client constructor.
// apps/commerce/mount.go calls this from its init(); exactly one registration.
func RegisterCommerceClientFactory(f func(cfg *Config, log luxlog.Logger) CommerceClient) {
	commerceClientFactory = f
}

// S3 is the object store, built where it is needed. (The name is the old one for
// deps.VFS; the store is sdk/object's: local disk, or the S3 driver when configured.)
//
// It used to be a field on Deps, resolved once at boot and copied into every
// subsystem whether or not that subsystem stores a byte. Six do; the rest carried
// it. Worse, resolving it centrally meant resolving it EARLY — the constructor
// dialled, so every process in the fleet waited on an object store to find out
// whether the six would ever want one.
//
// Constructing it is now free (object.NewBlobs touches no network), so the app
// that needs a store builds a store and nothing is threaded through anything.
//
// NEVER NIL. Callers write s3.Put and s3.Get unconditionally, so an absent store
// answers with the fail-closed stub — Put and Get return an error and the caller
// answers 502, rather than a nil dereference that answers 500 and blames itself.
// TeamBlobBucket is the single bucket every team blob lands in. Per-tenant isolation is
// the org-scoped KEY PREFIX (team/blobs/<org>/…) the consumer builds, not a bucket per
// tenant.
const TeamBlobBucket = "team-blobs"

func S3(log luxlog.Logger) object.Blobs {
	if err := object.Check(); err != nil {
		log.Error("object store unavailable; storage is fail-closed", "err", err)
		return object.NoBlobs()
	}
	return object.NewBlobs(object.Default(), TeamBlobBucket)
}
