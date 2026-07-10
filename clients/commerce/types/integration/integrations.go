package integration

import (
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

var (
	ErrorNotFound     = errors.New("Integration not found")
	ErrorTooMany      = errors.New("Too many of this Integration found")
	ErrorIdTypeNotSet = errors.New("Integration ID and Type not set")
)

type Integrations []Integration

func Encode(src *Integration, dst *Integration) error {
	switch src.Type {
	case AnalyticsCustomType:
		dst.Data = json.EncodeBytes(src.AnalyticsCustom)
	case AnalyticsFacebookPixelType:
		dst.Data = json.EncodeBytes(src.AnalyticsFacebookPixel)
	case AnalyticsFacebookConversionsType:
		dst.Data = json.EncodeBytes(src.AnalyticsFacebookConversions)
	case AnalyticsGoogleAdwordsType:
		dst.Data = json.EncodeBytes(src.AnalyticsGoogleAdwords)
	case AnalyticsGoogleAnalyticsType:
		dst.Data = json.EncodeBytes(src.AnalyticsGoogleAnalytics)
	case AnalyticsHeapType:
		dst.Data = json.EncodeBytes(src.AnalyticsHeap)
	case AnalyticsSentryType:
		dst.Data = json.EncodeBytes(src.AnalyticsSentry)
	case AuthorizeNetType:
		dst.Data = json.EncodeBytes(src.AuthorizeNet)
	case BitcoinType:
		dst.Data = json.EncodeBytes(src.Bitcoin)
	case EthereumType:
		dst.Data = json.EncodeBytes(src.Ethereum)
	case MailchimpType:
		dst.Data = json.EncodeBytes(src.Mailchimp)
	case MandrillType:
		dst.Data = json.EncodeBytes(src.Mandrill)
	case SendGridType:
		dst.Data = json.EncodeBytes(src.SendGrid)
	case SMTPRelayType:
		dst.Data = json.EncodeBytes(src.SMTPRelay)
	case NetlifyType:
		dst.Data = json.EncodeBytes(src.Netlify)
	case PaypalType:
		dst.Data = json.EncodeBytes(src.Paypal)
	case PlaidType:
		dst.Data = json.EncodeBytes(src.Plaid)
	case ReamazeType:
		dst.Data = json.EncodeBytes(src.Reamaze)
	case RecaptchaType:
		dst.Data = json.EncodeBytes(src.Recaptcha)
	case SalesforceType:
		dst.Data = json.EncodeBytes(src.Salesforce)
	case SecurityTokenType:
		dst.Data = json.EncodeBytes(src.SecurityToken)
	case ShipwireType:
		dst.Data = json.EncodeBytes(src.Shipwire)
	case StripeType:
		dst.Data = json.EncodeBytes(src.Stripe)
	case WoopraType:
		dst.Data = json.EncodeBytes(src.Woopra)
	default:
		return errors.New(fmt.Sprintf("Invalid Type: '%s'", src.Type))
	}

	dst.Enabled = src.Enabled
	dst.Show = src.Show

	dst.Id = src.Id
	dst.Type = src.Type

	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
	return nil
}

func Decode(src *Integration, dst *Integration) error {
	switch src.Type {
	case AnalyticsCustomType:
		dst.AnalyticsCustom = src.AnalyticsCustom
	case AnalyticsFacebookPixelType:
		dst.AnalyticsFacebookPixel = src.AnalyticsFacebookPixel
	case AnalyticsFacebookConversionsType:
		dst.AnalyticsFacebookConversions = src.AnalyticsFacebookConversions
	case AnalyticsGoogleAdwordsType:
		dst.AnalyticsGoogleAdwords = src.AnalyticsGoogleAdwords
	case AnalyticsGoogleAnalyticsType:
		dst.AnalyticsGoogleAnalytics = src.AnalyticsGoogleAnalytics
	case AnalyticsHeapType:
		dst.AnalyticsHeap = src.AnalyticsHeap
	case AnalyticsSentryType:
		dst.AnalyticsSentry = src.AnalyticsSentry
	case AuthorizeNetType:
		dst.AuthorizeNet = src.AuthorizeNet
	case BitcoinType:
		dst.Bitcoin = src.Bitcoin
	case EthereumType:
		dst.Ethereum = src.Ethereum
	case MailchimpType:
		dst.Mailchimp = src.Mailchimp
	case SendGridType:
		dst.SendGrid = src.SendGrid
	case SMTPRelayType:
		dst.SMTPRelay = src.SMTPRelay
	case MandrillType:
		dst.Mandrill = src.Mandrill
	case NetlifyType:
		dst.Netlify = src.Netlify
	case PaypalType:
		dst.Paypal = src.Paypal
	case PlaidType:
		dst.Plaid = src.Plaid
	case ReamazeType:
		dst.Reamaze = src.Reamaze
	case RecaptchaType:
		dst.Recaptcha = src.Recaptcha
		dst.Recaptcha.Enabled = src.Enabled
	case SalesforceType:
		dst.Salesforce = src.Salesforce
	case SecurityTokenType:
		dst.SecurityToken = src.SecurityToken
	case ShipwireType:
		dst.Shipwire = src.Shipwire
	case StripeType:
		dst.Stripe = src.Stripe
	case WoopraType:
		dst.Woopra = src.Woopra
	default:
		return errors.New(fmt.Sprintf("Invalid Type: '%s'", src.Type))
	}

	if len(src.Data) > 0 {
		switch src.Type {
		case AnalyticsCustomType:
			json.DecodeBytes(src.Data, &dst.AnalyticsCustom)
		case AnalyticsFacebookPixelType:
			json.DecodeBytes(src.Data, &dst.AnalyticsFacebookPixel)
		case AnalyticsFacebookConversionsType:
			json.DecodeBytes(src.Data, &dst.AnalyticsFacebookConversions)
		case AnalyticsGoogleAdwordsType:
			json.DecodeBytes(src.Data, &dst.AnalyticsGoogleAdwords)
		case AnalyticsGoogleAnalyticsType:
			json.DecodeBytes(src.Data, &dst.AnalyticsGoogleAnalytics)
		case AnalyticsHeapType:
			json.DecodeBytes(src.Data, &dst.AnalyticsHeap)
		case AnalyticsSentryType:
			json.DecodeBytes(src.Data, &dst.AnalyticsSentry)
		case BitcoinType:
			json.DecodeBytes(src.Data, &dst.Bitcoin)
		case EthereumType:
			json.DecodeBytes(src.Data, &dst.Ethereum)
		case MailchimpType:
			json.DecodeBytes(src.Data, &dst.Mailchimp)
		case MandrillType:
			json.DecodeBytes(src.Data, &dst.Mandrill)
		case SendGridType:
			json.DecodeBytes(src.Data, &dst.SendGrid)
		case SMTPRelayType:
			json.DecodeBytes(src.Data, &dst.SMTPRelay)
		case NetlifyType:
			json.DecodeBytes(src.Data, &dst.Netlify)
		case PaypalType:
			json.DecodeBytes(src.Data, &dst.Paypal)
		case PlaidType:
			json.DecodeBytes(src.Data, &dst.Plaid)
		case ReamazeType:
			json.DecodeBytes(src.Data, &dst.Reamaze)
		case RecaptchaType:
			json.DecodeBytes(src.Data, &dst.Recaptcha)
			dst.Recaptcha.Enabled = src.Enabled
		case SalesforceType:
			json.DecodeBytes(src.Data, &dst.Salesforce)
		case SecurityTokenType:
			json.DecodeBytes(src.Data, &dst.SecurityToken)
		case ShipwireType:
			json.DecodeBytes(src.Data, &dst.Shipwire)
		case StripeType:
			json.DecodeBytes(src.Data, &dst.Stripe)
		case WoopraType:
			json.DecodeBytes(src.Data, &dst.Woopra)
		default:
			return errors.New(fmt.Sprintf("Invalid Type: '%s'", src.Type))
		}
	}

	dst.Enabled = src.Enabled
	dst.Show = src.Show

	dst.Type = src.Type

	dst.UpdatedAt = time.Now()
	return Encode(dst, dst)
}

func (i Integrations) Append(src *Integration) (Integrations, error) {
	switch src.Type {
	case AnalyticsCustomType:
	case AnalyticsFacebookPixelType:
	case AnalyticsFacebookConversionsType:
	case AnalyticsGoogleAdwordsType:
	case AnalyticsGoogleAnalyticsType:
	case AnalyticsHeapType:
	case AnalyticsSentryType:
	default:
		if len(i.FilterByType(src.Type)) > 0 {
			return i, ErrorTooMany
		}
	}

	dst := Integration{}

	dst.Id = rand.ShortId()
	dst.Type = src.Type

	dst.CreatedAt = time.Now()

	if err := Decode(src, &dst); err != nil {
		return i, err
	}

	ins := append(i, dst)
	src.Id = dst.Id
	src.Data = dst.Data
	src.CreatedAt = dst.CreatedAt
	src.UpdatedAt = dst.UpdatedAt
	return ins, nil
}

func (i Integrations) MustAppend(src *Integration) Integrations {
	if ins, err := i.Append(src); err != nil {
		panic(err)
	} else {
		return ins
	}
}

func (i Integrations) Update(src *Integration) (Integrations, error) {
	if src.Id != "" {
		dst, err := i.FindById(src.Id)
		if err != nil {
			return i, err
		}

		ins := Integrations{}
		for _, in := range i {
			if in.Id == src.Id {
				err = Decode(src, dst)
				ins = append(ins, *dst)
			} else {
				ins = append(ins, in)
			}
		}
		src.Data = dst.Data
		src.UpdatedAt = dst.UpdatedAt
		return ins, err
	}

	if src.Type != "" {
		return i.Append(src)
	}

	return i, errors.New(fmt.Sprintf("Missing Id: '%s' and Type: '%s'", src.Id, src.Type))
}

func (i Integrations) MustUpdate(in *Integration) Integrations {
	// if in.Id == "" && in.Type == "" {
	// 	panic(errors.New(fmt.Sprintf("Wut?? '%s'", in)))
	// }

	if ins, err := i.Update(in); err != nil {
		panic(err)
	} else {
		return ins
	}
}

func (i Integrations) Remove(id string) (Integrations, error) {
	ins := Integrations{}
	for _, in := range i {
		// Go is terrible
		// if in.Id == id {
		// 	ins := append(i[:j], i[j+1:]...)
		// 	return ins, nil
		// }
		if in.Id != id {
			ins = append(ins, in)
		}
	}

	if len(ins) != len(i) {
		return ins, nil
	}

	return i, ErrorNotFound
}

func (i Integrations) MustRemove(id string) Integrations {
	if ins, err := i.Remove(id); err != nil {
		panic(err)
	} else {
		return ins
	}
}

func (i Integrations) FilterByType(typ Type) Integrations {
	ins := Integrations{}
	for _, in := range i {
		if in.Type == typ {
			ins = append(ins, in)
		}
	}
	return ins
}

func (i Integrations) FindByType(typ Type) *Integration {
	for _, in := range i {
		if in.Type == typ {
			return &in
		}
	}

	return nil
}

func (i Integrations) FindById(id string) (*Integration, error) {
	for _, in := range i {
		if in.Id == id {
			return &in, nil
		}
	}

	return nil, ErrorNotFound
}

func (i Integrations) EmailProvider() (*Integration, error) {
	for _, in := range i {
		if in.Type == MandrillType || in.Type == SendGridType || in.Type == SMTPRelayType {
			return &in, nil
		}
	}

	return nil, ErrorNotFound
}

func (i Integrations) EmailMarketingProvider() (*Integration, error) {
	for _, in := range i {
		if in.Type == MailchimpType || in.Type == SendGridType {
			return &in, nil
		}
	}

	return nil, ErrorNotFound
}
