// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/plane"
	iamserver "github.com/hanzoai/iam/server"
	luxlog "github.com/luxfi/log"
)

// The transport that carries a verification code to a person.
//
// IAM declares the client (iamserver.Sender) and composes the message; this carries
// it. The split is not decoration — the two halves need different knowledge and only
// one of them is available on each side. IAM knows how long a code lasts and what to
// say about it; the router knows which apps this deployment runs and how to start one
// that is cold. IAM links no router, so it cannot answer the second question, and it
// used to try: it looked for notify.sock and bound a sender when the FILE was there.
//
// That was wrong twice over. A socket file outlives the process that bound it
// wherever the run directory is a volume, so a file left by a dead pod reported
// delivery that could not happen — the same shape that once left commerce unwoken
// for three days behind a stale socket while every prepaid-balance read refused. And
// notify is one of the fleet's ON-DEMAND apps, so a service one call would have
// started reported no delivery at all, which is self-fulfilling: the method is
// hidden, nothing calls notify, nothing ever wakes it.
//
// plane.Ask answers both. It short-circuits to a function call when notify is
// co-resident, wakes it through the router when it is not, and returns ErrNoPeer —
// and only ErrNoPeer — when this deployment runs no such app. So a cold notify is
// simply started by the send that needed it, and a failing notify reports an outage
// instead of quietly becoming an absence.

// notifyApp is the peer this delivers through. Named once: a process name spelled at
// each call site is a chance to wake the wrong one.
const notifyApp = "notify"

// reachWait bounds how long Mount will wait for notify to come up.
//
// It is a fraction of plane's own 90s wake ceiling on purpose: that ceiling is the
// router's whole plugin-start budget, and identity must not spend it on a service
// nobody has asked for yet. Overrunning costs nothing — the router single-flights the
// start, so notify keeps coming up and the next call finds it, and the branch this
// falls into binds delivery anyway.
const reachWait = 5 * time.Second

// bindDelivery gives IAM a transport when this deployment has a notify to reach.
//
// The three outcomes are three different facts and each gets its own answer:
//
//	ErrNoPeer  no such app here      → bind nothing; the methods stay hidden, honestly
//	other err  here, not up (yet)    → bind; a send reports the outage, and hiding the
//	                                   method instead would hide it forever
//	nil        here and listening    → bind
func bindDelivery(log luxlog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), reachWait)
	defer cancel()

	err := plane.Reach(ctx, notifyApp)
	if errors.Is(err, plane.ErrNoPeer) {
		log.Info("no notify in this deployment — email/SMS codes and their second factors stay off", "err", err)
		return
	}
	if err != nil {
		log.Warn("notify is deployed here but is not up — delivery stays ON so a send reports the outage rather than the method vanishing", "err", err)
	}
	iamserver.BindSender(sender{})
}

// sender delivers one code per call over the internal plane.
//
// It is a VALUE with no fields, and that is deliberate: an interface holding a
// struct value is never nil, so the typed-nil hazard that the old constructor
// worked around with an untyped-nil return is not guarded against here — it is
// unrepresentable. There is nothing to configure and therefore nothing that can be
// configured wrong; the peer is resolved by NAME, and the tenant rides the message.
type sender struct{}

// Send carries m to the person it names.
//
// The channel arrives in IAM's vocabulary ("email" or "phone") and leaves in
// notify's ("email" or "sms"). The two words are for one thing and the translation
// belongs at exactly one boundary — this one — so neither side has to learn the
// other's.
//
// The org travels as an ARGUMENT rather than as the identity of a credential, which
// is the whole reason this call is on the plane. notify's HTTP surface derives the
// sending tenant from the validated principal, so a service calling it could only
// ever send as its own org, while IAM answers for every white-label identity host. A
// peer on a unix socket is trusted to name the tenant it acts for, so one process
// sends for all of them with no secret to mint, mount or rotate.
func (sender) Send(ctx context.Context, m iamserver.Message) error {
	in := plane.Send{Org: m.Org, To: m.To, Subject: m.Subject, Body: m.Body}
	switch m.Channel {
	case "email":
		in.Channel = "email"
	case "phone", "sms":
		in.Channel = "sms"
	default:
		return fmt.Errorf("iam: unknown delivery channel %q", m.Channel)
	}
	if _, err := plane.Ask[plane.Send, plane.Sent](ctx, notifyApp, plane.NotifySend, &in); err != nil {
		return fmt.Errorf("iam: deliver %s to %s: %w", in.Channel, notifyApp, err)
	}
	return nil
}
