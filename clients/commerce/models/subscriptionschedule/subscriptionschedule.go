package subscriptionschedule

import (
	"github.com/hanzoai/orm"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// Status represents the schedule lifecycle state.
type Status string

const (
	NotStarted Status = "not_started"
	Active     Status = "active"
	Completed  Status = "completed"
	Released   Status = "released"
	SSCanceled Status = "canceled"
)

// PhaseItem represents a single item within a schedule phase.
type PhaseItem struct {
	PriceId  string `json:"priceId"`
	Quantity int64  `json:"quantity"`
}

// Phase represents a time-bounded billing phase within the schedule.
type Phase struct {
	PlanId            string      `json:"planId"`
	Items             []PhaseItem `json:"items,omitempty"`
	StartDate         time.Time   `json:"startDate"`
	EndDate           time.Time   `json:"endDate"`
	TrialEnd          time.Time   `json:"trialEnd,omitempty"`
	ProrationBehavior string      `json:"prorationBehavior,omitempty"` // "create_prorations" | "none"
}

var kind = "subscription-schedule"

// SubscriptionSchedule represents a scheduled set of subscription phases.

func init() { orm.Register[SubscriptionSchedule](kind) }

type SubscriptionSchedule struct {
	mixin.Model[SubscriptionSchedule]

	CustomerId     string    `json:"customerId"`
	SubscriptionId string    `json:"subscriptionId,omitempty"`
	Status         Status    `json:"status"`
	StartDate      time.Time `json:"startDate"`
	EndBehavior    string    `json:"endBehavior,omitempty"` // "release" | "cancel"

	Phases  []Phase `json:"phases,omitempty" datastore:"-"`
	Phases_ string  `json:"-" datastore:",noindex"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (s *SubscriptionSchedule) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}

	if len(s.Phases_) > 0 {
		err = json.DecodeBytes([]byte(s.Phases_), &s.Phases)
		if err != nil {
			return err
		}
	}

	if len(s.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(s.Metadata_), &s.Metadata)
	}

	return err
}

func (s *SubscriptionSchedule) Save() (ps []datastore.Property, err error) {
	s.Phases_ = string(json.EncodeBytes(&s.Phases))
	s.Metadata_ = string(json.EncodeBytes(&s.Metadata))
	return datastore.SaveStruct(s)
}

func (s *SubscriptionSchedule) Validator() *val.Validator {
	return nil
}

// Release transitions the schedule to released, detaching from the subscription.
func (s *SubscriptionSchedule) Release() error {
	if s.Status != Active && s.Status != NotStarted {
		return fmt.Errorf("can only release active or not_started schedules, current: %s", s.Status)
	}
	s.Status = Released
	return nil
}

// Cancel transitions the schedule to canceled.
func (s *SubscriptionSchedule) Cancel() error {
	if s.Status != Active && s.Status != NotStarted {
		return fmt.Errorf("can only cancel active or not_started schedules, current: %s", s.Status)
	}
	s.Status = SSCanceled
	return nil
}

// Complete transitions the schedule to completed after all phases finish.
func (s *SubscriptionSchedule) Complete() {
	s.Status = Completed
}

// Start activates the schedule and links it to a subscription.
func (s *SubscriptionSchedule) Start(subscriptionId string) {
	s.Status = Active
	s.SubscriptionId = subscriptionId
}

func New(db *datastore.Datastore) *SubscriptionSchedule {
	s := new(SubscriptionSchedule)
	s.Init(db)
	s.Parent = db.NewKey("synckey", "", 1, nil)
	if s.Status == "" {
		s.Status = NotStarted
	}
	if s.EndBehavior == "" {
		s.EndBehavior = "release"
	}
	return s
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
