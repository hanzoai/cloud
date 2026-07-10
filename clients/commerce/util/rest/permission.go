package rest

import (
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	. "github.com/hanzoai/cloud/clients/commerce/util/permission"
)

type Permissions map[string][]bit.Mask

func masks(masks ...bit.Mask) []bit.Mask {
	return masks
}

var DefaultPermissions = map[string]Permissions{
	"bundle": Permissions{
		"create": masks(Admin, WriteBundle),
		"delete": masks(Admin, WriteBundle),
		"patch":  masks(Admin, ReadBundle|WriteBundle),
		"update": masks(Admin, ReadBundle|WriteBundle),
		"get":    masks(Admin, ReadBundle),
		"list":   masks(Admin, Bundle),
	},

	"campaign": Permissions{
		"create": masks(Admin, WriteCampaign),
		"delete": masks(Admin, WriteCampaign),
		"patch":  masks(Admin, ReadCampaign|WriteCampaign),
		"update": masks(Admin, ReadCampaign|WriteCampaign),
		"get":    masks(Admin, ReadCampaign),
		"list":   masks(Admin, Campaign),
	},

	"cart": Permissions{
		"create": masks(Admin, Published),
		"delete": masks(Admin),
		"patch":  masks(Admin, Published),
		"update": masks(Admin, Published),
		"get":    masks(Admin),
		"list":   masks(Admin),
	},

	"collection": Permissions{
		"create": masks(Admin, WriteCollection),
		"delete": masks(Admin, WriteCollection),
		"patch":  masks(Admin, ReadCollection|WriteCollection),
		"update": masks(Admin, ReadCollection|WriteCollection),
		"get":    masks(Admin, ReadCollection),
		"list":   masks(Admin, Collection),
	},

	"coupon": Permissions{
		"create": masks(Admin, WriteCoupon),
		"delete": masks(Admin, WriteCoupon),
		"patch":  masks(Admin, ReadCoupon|WriteCoupon),
		"update": masks(Admin, ReadCoupon|WriteCoupon),
		"get":    masks(Admin, ReadCoupon),
		"list":   masks(Admin, Coupon),
	},

	"form": Permissions{
		"create": masks(Admin, WriteForm),
		"delete": masks(Admin, WriteForm),
		"patch":  masks(Admin, ReadForm|WriteForm),
		"update": masks(Admin, ReadForm|WriteForm),
		"get":    masks(Admin, ReadForm),
		"list":   masks(Admin, Form),
	},

	"order": Permissions{
		"create": masks(Admin, WriteOrder),
		"delete": masks(Admin, WriteOrder),
		"patch":  masks(Admin, ReadOrder|WriteOrder),
		"update": masks(Admin, ReadOrder|WriteOrder),
		"get":    masks(Admin, ReadOrder),
		"list":   masks(Admin, Order),
	},

	"organization": Permissions{
		"create": masks(Admin, WriteOrganization),
		"delete": masks(Admin, WriteOrganization),
		"patch":  masks(Admin, ReadOrganization|WriteOrganization),
		"update": masks(Admin, ReadOrganization|WriteOrganization),
		"get":    masks(Admin, ReadOrganization),
		"list":   masks(Admin, Organization),
	},

	"payment": Permissions{
		"create": masks(Admin, WritePayment),
		"delete": masks(Admin, WritePayment),
		"patch":  masks(Admin, ReadPayment|WritePayment),
		"update": masks(Admin, ReadPayment|WritePayment),
		"get":    masks(Admin, ReadPayment),
		"list":   masks(Admin, Payment),
	},

	"plan": Permissions{
		"create": masks(Admin, WritePlan),
		"delete": masks(Admin, WritePlan),
		"patch":  masks(Admin, ReadPlan|WritePlan),
		"update": masks(Admin, ReadPlan|WritePlan),
		"get":    masks(Admin, ReadPlan),
		"list":   masks(Admin, Plan),
	},

	"product": Permissions{
		"create": masks(Admin, WriteProduct),
		"delete": masks(Admin, WriteProduct),
		"patch":  masks(Admin, ReadProduct|WriteProduct),
		"update": masks(Admin, ReadProduct|WriteProduct),
		"get":    masks(Admin, ReadProduct),
		"list":   masks(Admin, Product),
	},

	"referral": Permissions{
		"create": masks(Admin, WriteReferral|Published),
		"delete": masks(Admin, WriteReferral),
		"patch":  masks(Admin, ReadReferral|WriteReferral|Published),
		"update": masks(Admin, ReadReferral|WriteReferral|Published),
		"get":    masks(Admin, ReadReferral),
		"list":   masks(Admin, Referral),
	},

	"referralprogram": Permissions{
		"create": masks(Admin, WriteReferrer),
		"delete": masks(Admin, WriteReferrer),
		"patch":  masks(Admin, ReadReferrer|WriteReferrer),
		"update": masks(Admin, ReadReferrer|WriteReferrer),
		"get":    masks(Admin, ReadReferrer),
		"list":   masks(Admin, Referrer),
	},

	"referrer": Permissions{
		"create": masks(Admin, WriteReferrer),
		"delete": masks(Admin, WriteReferrer),
		"patch":  masks(Admin, ReadReferrer|WriteReferrer),
		"update": masks(Admin, ReadReferrer|WriteReferrer),
		"get":    masks(Admin, ReadReferrer),
		"list":   masks(Admin, Referrer),
	},

	"return": Permissions{
		"create": masks(Admin, WriteReturn),
		"delete": masks(Admin, WriteReturn),
		"patch":  masks(Admin, ReadReturn|WriteReturn),
		"update": masks(Admin, ReadReturn|WriteReturn),
		"get":    masks(Admin, ReadReturn),
		"list":   masks(Admin, Return),
	},

	"store": Permissions{
		"create": masks(Admin, WriteStore),
		"delete": masks(Admin, WriteStore),
		"patch":  masks(Admin, ReadStore|WriteStore),
		"update": masks(Admin, ReadStore|WriteStore),
		// Published is the anonymous storefront read key (see permission.go doc:
		// "Anonymous users (via published keys)"). A per-org storefront Published
		// token reads the published catalog (store + embedded listings) so a
		// logged-out shopper can browse — the same intent cart.create already
		// grants Published. Write paths stay Admin/WriteStore only.
		"get":  masks(Admin, ReadStore, Published),
		"list": masks(Admin, Store, Published),
	},

	"subscriber": Permissions{
		"create": masks(Admin, WriteSubscriber),
		"delete": masks(Admin, WriteSubscriber),
		"get":    masks(Admin, ReadSubscriber),
		"list":   masks(Admin, Subscriber),
		"patch":  masks(Admin, ReadSubscriber|WriteSubscriber),
		"update": masks(Admin, ReadSubscriber|WriteSubscriber),
	},

	"user": Permissions{
		"create": masks(Admin, WriteUser),
		"delete": masks(Admin, WriteUser),
		"patch":  masks(Admin, ReadUser|WriteUser),
		"update": masks(Admin, ReadUser|WriteUser),
		"get":    masks(Admin, ReadUser),
		"list":   masks(Admin, User),
	},

	"variant": Permissions{
		"create": masks(Admin, WriteVariant),
		"delete": masks(Admin, WriteVariant),
		"patch":  masks(Admin, ReadVariant|WriteVariant),
		"update": masks(Admin, ReadVariant|WriteVariant),
		"get":    masks(Admin, ReadVariant),
		"list":   masks(Admin, Variant),
	},
}
