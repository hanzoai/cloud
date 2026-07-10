package permission

import (
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
)

// There are three types of users that permissions commonly accomodate:
//		1. Anonymous users (via published keys)
//		2. Clients using API on their server (via secret keys)
//		3. Hanzo (Complete access)

const (
	All  bit.Mask = ^0
	None bit.Mask = 0

	Live bit.Mask = 1 << iota // 1 << 0 which is 00000001
	Test                      // 1 << 1 which is 00000010

	// Three main use cases
	Admin
	Published
	Secret

	// Payment specific
	Authorize
	Capture

	// List permissions
	Bundle
	Campaign
	Collection
	Coupon
	Form
	Order
	Organization
	Payment
	Plan
	Product
	Referral
	Referrer
	Store
	Subscriber
	User
	Variant

	// Read permissions
	ReadBundle
	ReadCampaign
	ReadCollection
	ReadCoupon
	ReadForm
	ReadOrder
	ReadOrganization
	ReadPayment
	ReadPlan
	ReadProduct
	ReadReferral
	ReadReferrer
	ReadStore
	ReadSubscriber
	ReadUser
	ReadVariant

	// Write permissions
	WriteBundle
	WriteCampaign
	WriteCollection
	WriteCoupon
	WriteForm
	WriteOrder
	WriteOrganization
	WritePayment
	WritePlan
	WriteProduct
	WriteReferral
	WriteReferrer
	WriteStore
	WriteSubscriber
	WriteUser
	WriteVariant

	Return
	ReadReturn
	WriteReturn
)

// Composite permissions, (both required)
var Charge = Authorize | Capture
