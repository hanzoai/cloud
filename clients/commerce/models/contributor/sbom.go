package contributor

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[SBOMEntry]("sbom-entry") }

// SBOMEntry represents a software component in the platform's bill of materials.
// Each entry tracks the component's identity, its contributors, and the
// revenue attributed to it based on bot/agent usage.
type SBOMEntry struct {
	mixin.Model[SBOMEntry]

	// Component identity
	Component  string `json:"component"`  // Package name (e.g., "@hanzo/bot")
	Repo       string `json:"repo"`       // GitHub repo (e.g., "hanzoai/bot")
	Version    string `json:"version"`    // Current version
	License    string `json:"license"`    // License type (MIT, Apache-2.0, etc.)
	TotalLines int64  `json:"totalLines"` // Total lines of code

	// Contributor breakdown from git blame
	// [{"login":"user","email":"u@e.com","lines":500,"percent":12.5}]
	Authors  []SBOMAuthor `json:"authors,omitempty" datastore:"-"`
	Authors_ string       `json:"-" datastore:",noindex"`

	// Revenue attribution
	UsageCount     int64 `json:"usageCount"`     // Times used in billing period
	RevenuePercent float64 `json:"revenuePercent"` // % of total platform revenue attributed

	// Scan metadata
	LastScanned time.Time `json:"lastScanned"`
	ScanCommit  string    `json:"scanCommit"` // Git commit hash of last scan

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

// SBOMAuthor is a contributor entry from git blame analysis.
type SBOMAuthor struct {
	Login   string  `json:"login"`
	Email   string  `json:"email"`
	Name    string  `json:"name,omitempty"`
	Lines   int64   `json:"lines"`
	Percent float64 `json:"percent"` // Percentage of component authored
}

func (s *SBOMEntry) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}
	if len(s.Authors_) > 0 {
		err = json.DecodeBytes([]byte(s.Authors_), &s.Authors)
		if err != nil {
			return err
		}
	}
	if len(s.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(s.Metadata_), &s.Metadata)
	}
	return err
}

func (s *SBOMEntry) Save() (ps []datastore.Property, err error) {
	s.Authors_ = string(json.EncodeBytes(&s.Authors))
	s.Metadata_ = string(json.EncodeBytes(&s.Metadata))
	return datastore.SaveStruct(s)
}

func (s *SBOMEntry) Validator() *val.Validator {
	return nil
}

func NewSBOM(db *datastore.Datastore) *SBOMEntry {
	s := new(SBOMEntry)
	s.Init(db)
	s.Parent = db.NewKey("synckey", "", 1, nil)
	return s
}

func QuerySBOM(db *datastore.Datastore) datastore.Query {
	return db.Query("sbom-entry")
}
