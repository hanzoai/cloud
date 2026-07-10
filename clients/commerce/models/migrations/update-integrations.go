package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/cloud/clients/commerce/util/json"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("update-integrations",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "")
		return NoArgs
	},
	func(db *ds.Datastore, org *organization.Organization) {
		org.Integrations = integration.Integrations{}
		for i, an := range org.Analytics.Integrations {
			in := integration.Integration{}
			switch an.Type {
			case "custom":
				in.Type = integration.AnalyticsCustomType
			case "facebook-conversions":
				in.Type = integration.AnalyticsFacebookConversionsType
			case "facebook-pixel":
				in.Type = integration.AnalyticsFacebookPixelType
			case "google-adwords":
				in.Type = integration.AnalyticsGoogleAdwordsType
			case "google-analytics":
				in.Type = integration.AnalyticsGoogleAnalyticsType
			case "heap":
				in.Type = integration.AnalyticsHeapType
			default:
				log.Warn("Analytics Type not supported: %s", an.Type, db.Context)
				continue
			}

			in.Id = an.IntegrationId
			in.Enabled = !an.Disabled
			in.Data = json.EncodeBytes(an)

			log.Debug("Updating Integration\nId: '%s'\nType: '%s'\nData: '%s'", in.Id, in.Type, in.Data, db.Context)
			if ins, err := org.Integrations.Update(&in); err == integration.ErrorNotFound {
				in.Id = ""
				org.Integrations = org.Integrations.MustUpdate(&in)
			} else if err != nil {
				panic(err)
			} else {
				org.Integrations = ins
			}

			org.Analytics.Integrations[i].IntegrationId = in.Id
		}

		if mailchimps := org.Integrations.FilterByType(integration.MailchimpType); len(mailchimps) > 0 {
			m := mailchimps[0]
			m.Mailchimp = org.Mailchimp
			org.Integrations = org.Integrations.MustUpdate(&m)
		} else {
			m := integration.Integration{
				Type:      integration.MailchimpType,
				Enabled:   org.Mailchimp.APIKey != "",
				Mailchimp: org.Mailchimp,
			}
			org.Integrations = org.Integrations.MustUpdate(&m)
		}

		if mandrills := org.Integrations.FilterByType(integration.MandrillType); len(mandrills) > 0 {
			m := mandrills[0]
			m.Mandrill = org.Mandrill
			org.Integrations = org.Integrations.MustUpdate(&m)
		} else {
			m := integration.Integration{
				Type:     integration.MandrillType,
				Enabled:  org.Mandrill.APIKey != "",
				Mandrill: org.Mandrill,
			}
			org.Integrations = org.Integrations.MustUpdate(&m)
		}

		if netlifies := org.Integrations.FilterByType(integration.NetlifyType); len(netlifies) > 0 {
			n := netlifies[0]
			n.Netlify = org.Netlify
			org.Integrations = org.Integrations.MustUpdate(&n)
		} else {
			n := integration.Integration{
				Type:    integration.NetlifyType,
				Enabled: org.Netlify.AccessToken != "",
				Netlify: org.Netlify,
			}
			org.Integrations = org.Integrations.MustUpdate(&n)
		}

		if reamazes := org.Integrations.FilterByType(integration.ReamazeType); len(reamazes) > 0 {
			r := reamazes[0]
			r.Reamaze = org.Reamaze
			org.Integrations = org.Integrations.MustUpdate(&r)
		} else {
			r := integration.Integration{
				Type:    integration.ReamazeType,
				Enabled: org.Reamaze.Secret != "",
				Reamaze: org.Reamaze,
			}
			org.Integrations = org.Integrations.MustUpdate(&r)
		}

		if recaptchas := org.Integrations.FilterByType(integration.RecaptchaType); len(recaptchas) > 0 {
			r := recaptchas[0]
			r.Recaptcha = org.Recaptcha
			org.Integrations = org.Integrations.MustUpdate(&r)
		} else {
			r := integration.Integration{
				Type:      integration.RecaptchaType,
				Enabled:   org.Recaptcha.Enabled,
				Recaptcha: org.Recaptcha,
			}
			org.Integrations = org.Integrations.MustUpdate(&r)
		}

		if shipwires := org.Integrations.FilterByType(integration.ShipwireType); len(shipwires) > 0 {
			s := shipwires[0]
			s.Shipwire = org.Shipwire
			org.Integrations = org.Integrations.MustUpdate(&s)
		} else {
			s := integration.Integration{
				Type:     integration.ShipwireType,
				Enabled:  org.Shipwire.Username != "",
				Shipwire: org.Shipwire,
			}
			org.Integrations = org.Integrations.MustUpdate(&s)
		}

		// Stripe has third party structs where json is not set to omit empty,
		// therefore we have to use Data instead
		if stripes := org.Integrations.FilterByType(integration.StripeType); len(stripes) > 0 {
			s := stripes[0]
			s.Data = json.EncodeBytes(org.Stripe)
			org.Integrations = org.Integrations.MustUpdate(&s)
			// log.Warn("Updating Stripe1 '%s'", string(json.EncodeBytes(org.Stripe)), db.Context)
			// log.Warn("Updating Stripe2 '%s'", string(json.EncodeBytes(s.Stripe)), db.Context)
		} else {
			s := integration.Integration{
				Type:    integration.StripeType,
				Enabled: org.Stripe.AccessToken != "",
				Data:    json.EncodeBytes(org.Stripe),
			}
			org.Integrations = org.Integrations.MustUpdate(&s)
			// log.Warn("Updating Stripe3 '%s'", string(json.EncodeBytes(org.Stripe)), db.Context)
			// log.Warn("Updating Stripe4 '%s'", string(json.EncodeBytes(s.Stripe)), db.Context)
		}

		// log.Warn("Updating Integrations '%s'", string(json.Encode(org.Integrations)), db.Context)
		org.MustUpdate()
	},
)
