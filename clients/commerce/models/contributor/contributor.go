package contributor

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Contributor]("contributor") }

// Contributor tracks an OSS contributor eligible for revenue share payouts.
// Contributors are identified by their git identity (email/login) and matched
// to SBOM line attribution for each software component used by bots, agents,
// and platform services.
type Contributor struct {
	mixin.Model[Contributor]

	// Identity
	UserId    string `json:"userId,omitempty"`    // Hanzo user ID (if registered)
	GitLogin  string `json:"gitLogin"`            // GitHub/GitLab login
	GitEmail  string `json:"gitEmail"`            // Git commit email
	Name      string `json:"name"`                // Display name
	AvatarURL string `json:"avatarUrl,omitempty"` // Avatar URL

	// Organization (optional: attribute to org instead of individual)
	OrgId   string `json:"orgId,omitempty"`
	OrgName string `json:"orgName,omitempty"`

	// Payout config
	PayoutMethod string        `json:"payoutMethod"` // "stripe", "crypto", "credits"
	PayoutTarget string        `json:"payoutTarget"` // Stripe account ID, wallet address, or user ID
	Currency     currency.Type `json:"currency" orm:"default:usd"`

	// Stats (computed periodically by cron)
	TotalLinesAuthored int64          `json:"totalLinesAuthored"`
	TotalEarned        currency.Cents `json:"totalEarned"`
	TotalPending       currency.Cents `json:"totalPending"`

	// Status
	Verified  bool      `json:"verified"`  // Email/identity verified
	Active    bool      `json:"active"`    // Actively receiving payouts
	JoinedAt  time.Time `json:"joinedAt"`
	LastPaid  time.Time `json:"lastPaid,omitempty"`

	// On-chain payout receipt (PayoutMethod="crypto"): the hash and chain id
	// of the most recent HUSD transfer. Set by the contributor payout cron.
	LastPayoutTx      string `json:"lastPayoutTx,omitempty"`
	LastPayoutChainId int64  `json:"lastPayoutChainId,omitempty"`

	// SBOM attribution: JSON array of component attributions
	// [{"component":"@hanzo/ui","repo":"hanzoai/ui","lines":1234,"percent":5.2}]
	Attributions  []Attribution `json:"attributions,omitempty" datastore:"-"`
	Attributions_ string        `json:"-" datastore:",noindex"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

// Attribution represents a contributor's share in a specific software component.
type Attribution struct {
	Component string  `json:"component"` // Package name (e.g., "@hanzo/ui")
	Repo      string  `json:"repo"`      // GitHub repo (e.g., "hanzoai/ui")
	Lines     int64   `json:"lines"`     // Lines of code authored
	TotalLines int64  `json:"totalLines"` // Total lines in component
	Percent   float64 `json:"percent"`   // Percentage of component authored
}

func (c *Contributor) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(c, ps); err != nil {
		return err
	}
	if len(c.Attributions_) > 0 {
		err = json.DecodeBytes([]byte(c.Attributions_), &c.Attributions)
		if err != nil {
			return err
		}
	}
	if len(c.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(c.Metadata_), &c.Metadata)
	}
	return err
}

func (c *Contributor) Save() (ps []datastore.Property, err error) {
	c.Attributions_ = string(json.EncodeBytes(&c.Attributions))
	c.Metadata_ = string(json.EncodeBytes(&c.Metadata))
	return datastore.SaveStruct(c)
}

func (c *Contributor) Validator() *val.Validator {
	return nil
}

func New(db *datastore.Datastore) *Contributor {
	c := new(Contributor)
	c.Init(db)
	c.Parent = db.NewKey("synckey", "", 1, nil)
	c.Currency = "usd"
	c.Active = true
	return c
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("contributor")
}
