package mixin

import "time"

type Salesforce struct {
	PrimarySalesforceId_   string    `json:"-"`
	SecondarySalesforceId_ string    `json:"-"`
	ExternalId_            string    `json:"-"`
	LastSync_              time.Time `json:"-"`
}

func (so *Salesforce) SetSalesforceId(id string) {
	so.PrimarySalesforceId_ = id
}

func (so *Salesforce) SalesforceId() string {
	return so.PrimarySalesforceId_
}

func (so *Salesforce) SetExternalId(id string) {
	so.ExternalId_ = id
}

func (so *Salesforce) ExternalId() string {
	return so.ExternalId_
}

func (so *Salesforce) SetSalesforceId2(id string) {
	so.SecondarySalesforceId_ = id
}

func (so *Salesforce) SalesforceId2() string {
	return so.SecondarySalesforceId_
}

func (so *Salesforce) SetLastSync() {
	// Add 1 more minute to the Last Sync date due to sf resolution being nearest minute
	so.LastSync_ = time.Now().Add(1 * time.Minute)
}

func (so *Salesforce) LastSync() time.Time {
	return so.LastSync_
}
