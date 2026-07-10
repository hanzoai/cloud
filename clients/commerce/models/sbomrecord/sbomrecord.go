// Package sbomrecord stores the Software Bill of Materials emitted for a
// deployed image. One SBOMRecord is the normalized OSS-dependency tree of a
// single image digest — the queryable, attributable artifact the arcd build
// pipeline produces on every deploy.
//
// The full CycloneDX/SPDX document is provenance and lives as an OCI referrer
// on the image in GHCR; commerce stores only what attribution needs: the flat
// list of packages (PURL + scope), keyed by image digest. The digest is
// immutable and content-addressed, so re-ingesting the same image is a no-op
// (idempotent on ImageDigest).
//
// This is a global (cross-namespace) record like Organization/User: an image
// is built once and used by many orgs. It is registered with
// DefaultNamespace via mixin so it is not scoped to a tenant.
package sbomrecord

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var kind = "sbom-record"

func init() { orm.Register[SBOMRecord]("sbom-record") }

// Component is one OSS dependency in the SBOM, normalized from the CycloneDX
// "components"/"dependencies" graph. Scope is "direct" if the component is a
// declared dependency of the image's service, else "transitive". The PURL is
// the join key to accrual + maintainer resolution.
type Component struct {
	PURL      string `json:"purl"`
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"` // golang | npm | pypi | apk | deb | ...
	Version   string `json:"version"`
	Scope     string `json:"scope"` // direct | transitive
}

// SBOMRecord is the normalized SBOM for one built image.
type SBOMRecord struct {
	mixin.Model[SBOMRecord]

	// ImageRef is the full image reference, e.g.
	// "ghcr.io/hanzoai/commerce:1.785.15". ImageDigest is the immutable
	// content digest "sha256:..." — the de-dup / idempotency key.
	ImageRef    string `json:"imageRef"`
	ImageDigest string `json:"imageDigest"`

	// Service is the logical Hanzo service this image is (e.g. "commerce",
	// "cloud", "gateway"). Org-to-service usage maps an org's spend to the
	// SBOMs it should be attributed against.
	Service string `json:"service,omitempty"`

	// Format is the source SBOM format ("cyclonedx" | "spdx") and Tool the
	// generator ("syft"). Recorded for provenance/audit.
	Format string `json:"format,omitempty"`
	Tool   string `json:"tool,omitempty"`

	// ComponentCount is len(Components), denormalized for cheap listing.
	ComponentCount int `json:"componentCount"`

	// Components is the flat dependency list. Stored as a noindex JSON blob
	// (Components_) — it can be large; we never filter on individual entries.
	Components  []Component `json:"components" datastore:"-"`
	Components_ string      `json:"-" datastore:",noindex"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (s *SBOMRecord) Defaults() {
	s.Parent = s.Datastore().NewKey("synckey", "", 1, nil)
	if s.Format == "" {
		s.Format = "cyclonedx"
	}
}

func (s *SBOMRecord) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}
	if len(s.Components_) > 0 {
		if err = json.DecodeBytes([]byte(s.Components_), &s.Components); err != nil {
			return err
		}
	}
	if len(s.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(s.Metadata_), &s.Metadata)
	}
	return err
}

func (s *SBOMRecord) Save() (ps []datastore.Property, err error) {
	s.ComponentCount = len(s.Components)
	s.Components_ = string(json.EncodeBytes(&s.Components))
	s.Metadata_ = string(json.EncodeBytes(&s.Metadata))
	return datastore.SaveStruct(s)
}

func (s *SBOMRecord) Validator() *val.Validator { return nil }

func New(db *datastore.Datastore) *SBOMRecord {
	s := new(SBOMRecord)
	s.Init(db)
	s.Defaults()
	return s
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}

// Ingest is the ONE persistence path for an SBOM, shared by every transport
// (HTTP POST /v1/billing/sbom and the ZAP OpSBOMIngest handler). It upserts the
// record idempotently on ImageDigest: re-ingesting the same image updates the
// existing record in place rather than creating a duplicate. db must be scoped
// to the global "system" namespace by the caller. Returns the stored record.
func Ingest(db *datastore.Datastore, in *SBOMRecord) (*SBOMRecord, error) {
	rec := New(db)
	existing := make([]*SBOMRecord, 0, 1)
	if _, err := Query(db).
		Filter("ImageDigest=", in.ImageDigest).
		Limit(1).
		GetAll(&existing); err == nil && len(existing) > 0 {
		rec = existing[0]
	}

	rec.ImageRef = in.ImageRef
	rec.ImageDigest = in.ImageDigest
	rec.Service = in.Service
	if in.Format != "" {
		rec.Format = in.Format
	}
	rec.Tool = in.Tool
	rec.Components = in.Components
	if in.Metadata != nil {
		rec.Metadata = in.Metadata
	}

	if len(existing) > 0 {
		return rec, rec.Update()
	}
	return rec, rec.Create()
}
