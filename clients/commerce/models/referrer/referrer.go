package referrer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/affiliate"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referralprogram"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/client"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/timeutil"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Referrer]("referrer") }

// Is a link that can refer customers to buy products
type Referrer struct {
	mixin.Model[Referrer]

	Code      string                          `json:"code"`
	Program   referralprogram.ReferralProgram `json:"program"`
	ProgramId string                          `json:"programId"`
	OrderId   string                          `json:"orderId"`
	UserId    string                          `json:"userId"`

	AffiliateId     string              `json:"affiliateId,omitempty"`
	Affiliate       affiliate.Affiliate `json:"affiliate,omitempty" datastore:"-"`
	FirstReferredAt time.Time           `json:"firstReferredAt"`

	Client      client.Client `json:"-"`
	Blacklisted bool          `json:"blacklisted,omitempty"`
	Duplicate   bool          `json:"duplicate,omitempty"`

	State  Map    `json:"state,omitempty" datastore:"-"`
	State_ string `json:"-" datastore:",noindex"`
}

type Referrent interface {
	Id() string
	Kind() string
	Total() currency.Cents
}

func (r *Referrer) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	r.State_ = string(json.EncodeBytes(&r.State))

	// Save properties
	return datastore.SaveStruct(r)
}

func (r *Referrer) Load(ps []datastore.Property) (err error) {
	// Ensure we're initialized
	r.Defaults()

	// Load supported properties
	if err = datastore.LoadStruct(r, ps); err != nil {
		return err
	}

	if len(r.State_) > 0 {
		err = json.DecodeBytes([]byte(r.State_), &r.State)
	}

	return err
}

func (r *Referrer) SaveReferral(ctx context.Context, orgId string, event referral.Event, rfn Referrent, userId string, test bool) (*referral.Referral, error) {
	if !test && userId == r.UserId {
		return nil, errors.New("User cannot refer themselves")
	}

	log.Debug("Creating referral")
	// Create new referral
	rfl := referral.New(r.Datastore())
	rfl.Type = event
	rfl.Referrer.Id = r.Id()
	rfl.Referrer.AffiliateId = r.AffiliateId
	rfl.Referrer.UserId = r.UserId

	// Save referrent's id
	switch rfn.Kind() {
	case "order":
		log.Debug("Saving referral for new order")
		rfl.OrderId = rfn.Id()
	case "user":
		log.Debug("Saving referral for new user")
		rfl.UserId = rfn.Id()
	}

	log.JSON("Saving referral", rfl)

	// Try to save referral
	if err := rfl.Create(); err != nil {
		return rfl, err
	}

	// If this is the first referral, update referrer
	if timeutil.IsZero(r.FirstReferredAt) {
		r.FirstReferredAt = time.Now()
		r.Update()
	}

	if err := r.LoadReferralProgram(); err != nil {
		return rfl, err
	}

	// Apply any program actions if applicable
	if err := r.ApplyActions(ctx, orgId, event, &r.Program, rfn, rfl, test); err != nil {
		return rfl, err
	}

	return rfl, nil
}

func (r *Referrer) LoadReferralProgram() error {
	if r.ProgramId == "" {
		return nil
	}

	prog := referralprogram.New(r.Datastore())

	if err := prog.GetById(r.ProgramId); err != nil {
		return err
	}

	r.Program = *prog

	return nil
}

func (r *Referrer) LoadAffiliate() error {
	if r.AffiliateId == "" {
		return nil
	}

	aff := affiliate.New(r.Datastore())

	if err := aff.GetById(r.AffiliateId); err != nil {
		return err
	}

	r.Affiliate = *aff

	return nil
}

func (r *Referrer) Referrals() ([]*referral.Referral, error) {
	referrals := make([]*referral.Referral, 0)
	_, err := referral.Query(r.Datastore()).Filter("ReferrerId=", r.Id()).GetAll(referrals)
	return referrals, err
}

func (r *Referrer) Transactions() ([]*transaction.Transaction, error) {
	transactions := make([]*transaction.Transaction, 0)
	_, err := transaction.Query(r.Datastore()).Filter("ReferrerId=", r.Id()).GetAll(transactions)
	return transactions, err
}

// Referral Program stuff

func (r *Referrer) TestTrigger(action referralprogram.Action, event referral.Event, test bool) (bool, error) {
	trig := action.Trigger

	if trig.Event != "" && event != trig.Event {
		log.Debug("Event mismatch '%s' != '%s'", event, trig.Event)
		return false, nil
	}

	switch trig.Type {
	case referralprogram.CreditGreaterThanOrEquals:
		// Get all transactions
		trans := make([]*transaction.Transaction, 0)
		if _, err := transaction.Query(r.Datastore()).Filter("DestinationId=", r.UserId).Filter("Currency=", trig.Currency).Filter("Test=", test).GetAll(&trans); err != nil {
			return false, err
		}

		// Total balance
		balance := 0
		for _, t := range trans {
			if t.Type == transaction.Withdraw {
				balance -= int(t.Amount)
			} else {
				balance += int(t.Amount)
			}
		}

		log.Debug("CreditGreaterThanOrEquals Trigger, Balance: '%d %s'", balance, trig.Currency, r.Context())

		// 'Forward' any balance increments from this trigger executing
		log.Debug("Looking at actions with credit to forward '%s': '%s' ? '%s'", action.Type, action.Currency, trig.Currency, r.Context())
		if action.Type == referralprogram.StoreCredit && action.Currency == trig.Currency {
			done, ok := r.State[action.Name+"_done"].(bool)
			if action.Once && ok && done {
				log.Debug("Don't forward since this was executed once")
			} else {
				balance += int(action.Amount)
				log.Debug("Balance Amount %s", balance)
			}
		}

		// Check trigger
		if balance >= int(trig.CreditGreaterThanOrEquals) {
			return true, nil
		}
	case referralprogram.ReferralsGreaterThanOrEquals:
		log.Debug("ReferralsGreaterThanOrEquals Trigger")

		// Count number of referrals
		if count, err := referral.Query(r.Datastore()).Filter("Referrer.Id=", r.Id()).Count(); err != nil {
			return false, err
			// Check trigger
		} else if count >= trig.ReferralsGreaterThanOrEquals {
			return true, nil
		}
		return false, nil
	case referralprogram.Always:
		return true, nil
	default:
		log.Error("Unknown Trigger '%s'", trig.Type, r.Context())
		return false, errors.New(fmt.Sprintf("Unknown Trigger '%s'", trig.Type))
	}

	return false, nil
}

func (r *Referrer) ApplyActions(ctx context.Context, orgId string, event referral.Event, p *referralprogram.ReferralProgram, rfn Referrent, rfl *referral.Referral, test bool) error {
	domains := []string{}

	old := len(r.Program.Triggers) > 0
	if old {
		log.Debug("Old Triggers")
	} else {
		log.Debug("New Triggers")
	}

	for _, action := range p.Actions {
		if !old {
			if ok, err := r.TestTrigger(action, event, test); !ok {
				if err != nil {
					return err
				}
				continue
			}
		}

		// Only execute if state isn't done
		done, ok := r.State[action.Name+"_done"].(bool)
		if action.Once && ok && done {
			log.Debug("This was executed once")
			continue
		}

		switch action.Type {
		case referralprogram.StoreCredit:
			log.Info("Applying store credit.", r.Context())
			if !done && action.Once {
				r.State[action.Name+"_done"] = true
				r.MustUpdate()
			}

			amount := action.Amount

			if amount == 0 {
				amount = rfn.Total()
			}

			log.Info("Saving store credit %v", rfn.Total(), r.Context())
			if err := saveStoreCredit(r, amount, action.Currency, test); err != nil {
				return err
			}
		// case referralprogram.Refund:
		// 	return nil
		case referralprogram.SendUserEmail:
			if !done && action.Once {
				r.State[action.Name+"_done"] = true
				r.MustUpdate()
			}

			fn := delay.FuncByKey("referrer-send-user-email")
			log.Debug("Sending Email Template '%s'", action.EmailTemplate, ctx)
			fn.Call(ctx, orgId, action.EmailTemplate, r.UserId)
		case referralprogram.SendWoopra:
			if !done && action.Once {
				r.State[action.Name+"_done"] = true
				r.MustUpdate()
			}

			fn := delay.FuncByKey("referrer-send-woopra-event")
			log.Debug("Sending Woopra Event '%s'", action.Domain, ctx)
			domains = append(domains, action.Domain)
			fn.Call(ctx, orgId, action.Domain, r.UserId, rfn.Id(), rfn.Kind())
		default:
			log.Error("Unknown Action '%s'", action.Type, r.Context())
			return errors.New(fmt.Sprintf("Unknown Action '%s'", action.Type))
		}
	}

	rfl.Referrer.WoopraDomains = strings.Join(domains, ",")
	if err := rfl.Update(); err != nil {
		return err
	}

	// No actions triggered for this referral
	return nil
}

// Credit user with store credit by saving transaction
func saveStoreCredit(r *Referrer, amount currency.Cents, cur currency.Type, test bool) error {
	trans := transaction.New(r.Datastore())
	trans.Type = transaction.Deposit
	trans.Amount = amount
	trans.Currency = cur
	trans.SourceId = r.Id()
	trans.SourceKind = r.Kind()
	trans.DestinationId = r.UserId
	trans.DestinationKind = "user"
	trans.Notes = "Deposit due to referral"
	trans.Tags = "referral"
	trans.Test = test
	log.Debug("Deposit type: %v", trans.Currency)
	log.Debug("Currency amount: %v", trans.Amount)
	log.Debug("Destination ID: %v", trans.DestinationId)
	return trans.Create()
}

func (r *Referrer) Defaults() {
	r.State = make(Map)
}

func New(db *datastore.Datastore) *Referrer {
	r := new(Referrer)
	r.Init(db)
	r.Defaults()
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("referrer")
}
