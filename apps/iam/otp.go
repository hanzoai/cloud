// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/notify"
)

// otpSender carries an IAM verification code to a person over the notify rail,
// IN PROCESS.
//
// This is the grafted host's half of the seam IAM exposes as server.BindSender.
// Everything code-shaped in IAM — sign-in by emailed code, sign-in by texted
// code, and the email and SMS second factors — reads one predicate that reports
// on the BOUND SENDER, so binding this turns all four on together and binding
// nothing leaves all four honestly hidden.
//
// The whole reason it is worth having beside the standalone iam's HTTP client is
// the TENANT. notify's HTTP surface derives the sending org from the validated
// principal and never from a header, so a service credential can only ever send
// as its own org — which is wrong for a process answering for every white-label
// identity host. notify.Send takes the org as an ARGUMENT precisely for callers
// that carry no request principal, so in-process delivery serves every tenant
// correctly with no credential to mint, hold or rotate, and with no cross-tenant
// authorization hole opened in the public surface.
type otpSender struct {
	kms cloud.KMSClient
}

// Send delivers code to dest for org over channel.
//
// channel arrives in IAM's vocabulary ("email" or "phone") and leaves in
// notify's ("email" or "sms"). The provider is left empty so notify picks the
// one whose credentials the ORG actually has configured in KMS.
func (s otpSender) Send(ctx context.Context, org, channel, dest, code string) error {
	if org == "" {
		// notify resolves the provider credential by org. Guessing a default would
		// send this tenant's code through another tenant's account.
		return fmt.Errorf("iam otp: org is required to route a verification code")
	}
	var ch, subject string
	switch channel {
	case "email":
		ch, subject = "email", "Your verification code"
	case "phone", "sms":
		ch = "sms"
	default:
		return fmt.Errorf("iam otp: unknown channel %q", channel)
	}
	_, err := notify.Send(ctx, s.kms, org, ch, "", []string{dest}, subject, otpMessage(code))
	return err
}

// otpMessage is the text a person receives. It names no brand: this process
// answers for every white-label identity host, so a hardcoded name would be the
// wrong one on most of them. The sender identity the recipient actually sees is
// the org's own provider, which is the thing notify resolves per tenant.
func otpMessage(code string) string {
	return "Your verification code is " + code + ". It expires in 10 minutes. If you did not request it, ignore this message and do not share it with anyone."
}
