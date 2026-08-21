package meet

// meter.go — who pays for a seat in a room.
//
// Almost everything this subsystem serves is free and should be: the lobby is a
// read, the health probe is a probe, and the SPA is static bytes. One act is not.
// Minting a join token is what ADMITS a participant to the media server, and a
// participant on an SFU is a live audio/video pipe for as long as they stay —
// the most expensive thing per head this platform hands out. The surface declared
// cloud.Free, so nothing authorized it and nothing recorded it.
//
// THE UNIT IS THE SEAT, NOT THE MINUTE, and that is a statement about this seam
// rather than a rounding of the bill. Media rides browser-to-SFU directly; this
// process issues a signed token and then never hears from the session again —
// there is no webhook receiver, no room API client, and therefore no duration
// anywhere in this binary to meter. A seat is what we can see, so a seat is what
// is priced. Pricing a minute here would mean inventing a number nobody measured.
//
// A REFUSAL IS A REFUSAL. Unlike search, which drops a paid engine and answers
// from the free ones, there is no cheaper room to join — so a caller who cannot
// cover a seat is told so, in the money wire's own words, before a token exists.

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/zap-proto/zip"
)

// feeEnv is the operator knob for what one seat costs: MEET_FEE_CENTS_SEAT, or
// MEET_FEE_CENTS for every billed act on this surface.
const feeEnv = "MEET_FEE_CENTS"

// seat is the billed act: one participant admitted to one room.
const seat = "seat"

// defaultFeeCents is one cent per seat.
//
// A policy default sized like the compute it is — an SFU pipe on capacity we run,
// the same class as a browser render — and not the platform's $1.00 provision
// fee, which is sized for creating a database and would make joining a standup
// cost more than the standup. Operators move it with the knob above; 0 makes a
// seat free again, and un-gated with it.
const defaultFeeCents int64 = 1

func fee() int64 { return cloud.FeeCents(feeEnv, seat, defaultFeeCents) }

// afford authorizes one seat BEFORE a token is minted, so a caller who cannot
// cover it never receives credentials to a room they cannot be billed for.
//
// It is called after admits, deliberately: a caller who is not admitted to this
// room gets 401 for that reason and not a bill they did not owe, and asking the
// ledger about somebody we are going to refuse anyway is a balance read nobody
// needed.
func afford(s *cloud.Service[state], c *zip.Ctx) (*cloud.Charge, error) {
	return s.Bill.Allow(c.Context(), cloud.PayerOf(c.Context()), seat, fee())
}

// charge debits one seat, after the token exists. A mint that failed handed out
// nothing, so it bills nothing.
func charge(ch *cloud.Charge) {
	// Ref is left unset so the meter mints one: it names an ACT, and rejoining a
	// room is a second seat. Keyed on the room, a whole day's calls would be one.
	ch.Debit(metering.Usage{Model: seat, AmountCents: fee()})
}
