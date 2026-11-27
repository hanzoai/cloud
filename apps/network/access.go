package network

// access.go — what the anonymous operations of this package share.
//
// THE ANONYMOUS EXCEPTION. Everything else this package serves is org-scoped by the
// validated principal and gated by gate(): it is a tenant's view of its own
// overlay. Two operations are a stranger's and take no principal at all, for the
// reason the newsletter's do (apps/marketing newsletter.go): the person asking is
// not signed in yet, because what they ask for is how they come to be.
//
//	POST /v1/network/invite/verify   is this invite code live? (invite.go)
//	POST /v1/network/waitlist        ask to be told when there is room (waitlist.go)
//
// Neither calls gate(), which needs the ZT controller and an org, and neither reads
// an org from the request: the records are the BRAND's (cloud.Brand), in the brand
// org's Base, and a header or a bearer naming another org names nothing. Each is
// declared open (spec.Open), so the document asks no credential of them.
//
// The operator's half, the invites a SuperAdmin issues, reads and revokes, is
// invites_admin.go, under /v1/admin/network.
//
// NOTHING ENFORCES AN INVITE YET. Verifying says whether a code is live and spends
// nothing; the operations that will need one (redeeming it, and what follows) do not
// exist, so until they do no part of the server refuses a person for lacking a code.
// They will answer with the problem type invite_required.
//
// WHERE THEY LIVE. They belong to this capability by address, not to marketing: HIP-0139
// §3 files every route under /v1/<app>, and openapi/misfiled.txt only shrinks, so
// /v1/network/waitlist served by marketing would be a new line in it. The records
// are written the way marketing writes its own intake (apps/crm base.go): into the
// brand org's Base over the plane, under ids derived from what a record is, so a
// replay is one record.

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/secret"
	addr "github.com/hanzoai/ai/address"
	"github.com/zap-proto/zip"
)

// inviteRef is where KMS holds the one key this package seals under: the HMAC of an
// invite code is taken under it, and so is every count of an address or an
// identifier, under a label of its own.
const inviteRef = "network/invite-hmac"

// The limits. Each says what it stops and the arithmetic it rests on.
const (
	// maxAccessBody bounds an anonymous body: a code, an address, a handle, a role and
	// a short source are far under it, so a larger body is abuse.
	maxAccessBody = 4 * 1024
	// maxEmail bounds an address as RFC 5321 does, and maxLine a source or a note.
	maxEmail = 320
	maxLine  = 200

	// verifyRate is how many verifications one caller makes in verifyWindow, hit or
	// miss: a person types a code a few times, and ten an hour is more than any does.
	verifyRate   = 10
	verifyWindow = time.Hour
	// missRate is how many MISSES one caller makes in missWindow before every
	// verification it asks is refused 429. A code is HNZ- and ten characters of a
	// 32-character alphabet, 32^10 = 1,125,899,906,842,624 of them (1.1e15). One IPv4
	// address reaches at most 5 x 24 = 120 guesses a day, one in 9.4e12 of the space
	// against one live code; an IPv6 /64 the same, and the /48 around it, which is
	// counted at 16 times the limit (address.Buckets), 80 an hour or 1,920 a day for all
	// its /64s together. A botnet of 10,000 IPv4 addresses makes 1.2 million guesses a
	// day, and against 10,000 live codes each guess hits with probability 8.9e-12, so
	// one hit is expected in about 94,000 days, roughly 260 years. The limits are per
	// network address: clients behind one address share them, and so do callers whose
	// address is unknown (one bucket for all of them).
	missRate   = 5
	missWindow = time.Hour
	// missBudget is how many misses the whole deployment takes in missWindow before it
	// says so, once, in the log. It refuses no one: a count that shut verification at a
	// threshold would let any crowd of addresses close the door on everyone, and the
	// per-caller count above is what bounds guessing. 2,000 an hour is far above what
	// typing mistakes make, so past it a person is meant to look.
	missBudget = 2000

	// waitRate is how many entries one caller asks for in waitWindow: a person joins
	// once, and ten an hour covers an office behind one address.
	waitRate   = 10
	waitWindow = time.Hour
	// waitBurst is how many entries the deployment takes in burstWindow before it says so,
	// once, in the log. It refuses no one, for the reason missBudget does not: 1,000 an
	// hour is 24,000 a day, past any real launch, so past it a person is meant to look,
	// and the caller's own rate is what bounds the writes.
	waitBurst   = 1000
	burstWindow = time.Hour
)

// The names the counts are kept under in the deployment's shared count.
const (
	verifyName = "network:invite-verify"
	missName   = "network:invite-miss"
	budgetName = "network:invite-budget"
	alarmName  = "network:invite-alarm"
	waitName   = "network:waitlist"
	burstName  = "network:waitlist-burst"
)

// newState is the state of a mounted package: the controller's client, and the key and
// the memory of the Base it grew that the intake keeps.
func newState(cl *client, c *coord.Coord) state {
	return state{cl: cl, seal: sealOf(c), grown: new(sync.Map)}
}

// sealLease is how long a pod that is minting the key keeps every other pod from minting
// it: above the two KMS calls a mint makes (each bounded at 10 s).
const sealLease = 30 * time.Second

// sealOf is the key held at inviteRef, whose first mint takes a lease on the deployment's
// coordinator c, so two pods that find no key at once do not each store one.
func sealOf(c *coord.Coord) *secret.Sealed {
	return secret.NewSealed(inviteRef, secret.Lease(c.Lease("network:seal", inviteRef, sealLease)))
}

// countKey is the key a count is made under, derived from the sealed key under a
// label of its own, so no count shares a key with a code.
func countKey(key []byte) []byte { return mac(key, "network/count") }

// tally is the name a value is counted under: its keyed hash, so the shared count holds
// no address and no identifier, and cannot be reversed by trying every one.
func tally(ck []byte, v string) string { return hex.EncodeToString(mac(ck, v)) }

// clientOf is the address a request arrived from, and empty for a call with no
// request behind it or an address the host could not read.
func clientOf(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return geo.IP(c)
	}
	return ""
}

// callerBuckets are what a caller is counted under: its address by addr.Buckets, as
// the edge counts it (an IPv6 address by its /64 and its /48, the wider at its scale),
// each key a tally. A caller whose address is unknown shares one bucket, never none.
func callerBuckets(ck []byte, from string) []addr.Bucket {
	if from == "" {
		return []addr.Bucket{{Key: tally(ck, "-"), Scale: 1}}
	}
	bs := addr.Buckets(from)
	for i := range bs {
		bs[i].Key = tally(ck, bs[i].Key)
	}
	return bs
}

// spend charges one request to each of buckets in the count called name, held to limit
// in every, and answers whether it was within the limit and how long until the window
// the refusal sits in ends. A request over the limit is charged too, so a caller
// cannot ride a refused one for free.
func (o ops) spend(ctx context.Context, name string, buckets []addr.Bucket, limit int, every time.Duration) (bool, time.Duration) {
	keys, scales := make([]string, len(buckets)), make([]int, len(buckets))
	for i, b := range buckets {
		keys[i], scales[i] = b.Key, b.Scale
	}
	left, reset := o.s.Coord.Spend(ctx, name, keys, scales, limit, every)
	return left >= 0, time.Until(reset)
}

// tooMany is the one refusal a count gives, and says nothing of which count. It tells
// the caller when to come back, in seconds, as Retry-After: the end of the window, or,
// where the shared count does not say when its window opens, the window's whole length.
func tooMany(ctx context.Context, wait time.Duration) error {
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Retry-After", strconv.FormatInt(max(1, int64((wait+time.Second-1)/time.Second)), 10))
	}
	return zip.Errorf(http.StatusTooManyRequests, "too many requests")
}

// brandOf answers the brand org the intake files under: one the registry knows, or the
// 503 a deployment that cannot hold the intake gives, saying nothing of why.
func (o ops) brandOf() (string, error) {
	org := cloud.Brand()
	if !brand.Registered(org) {
		return "", zip.Errorf(http.StatusServiceUnavailable, "network access is not configured")
	}
	return org, nil
}

// hold makes sure the brand's Base holds the collections the intake writes. It is asked
// AFTER a stranger's caller has been counted, never before, so a Base that is failing is
// tried at the rate of each caller and not once for every request.
func (o ops) hold(ctx context.Context, org string) error {
	if err := o.grow(ctx, org); err != nil {
		o.s.Log.Warn("network: the brand's Base cannot hold the intake's collections", "org", org, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "network access is not configured")
	}
	return nil
}

// home is the brand org with its Base held: what the operator's ops, which are no
// stranger's, ask first.
func (o ops) home(ctx context.Context) (string, error) {
	org, err := o.brandOf()
	if err != nil {
		return "", err
	}
	return org, o.hold(ctx, org)
}

// door answers the brand org and the key the intake is sealed under. Only an operator's
// issue passes provision, which seals the key when there is none; a path any stranger
// can reach never does, and while no key is sealed it answers 503 as a deployment that
// is not configured, having made nothing. KMS that cannot answer, or holds a key that is
// not one, is 503 too.
func (o ops) door(ctx context.Context, provision bool) (string, []byte, error) {
	org, err := o.brandOf()
	if err != nil {
		return "", nil, err
	}
	key, err := o.s.State.seal.Get(ctx, o.s.KMS, provision)
	switch {
	case errors.Is(err, secret.ErrBusy):
		// Another pod is sealing the key this moment: nothing was minted here; try again.
		if c, ok := cloud.Request(ctx); ok {
			c.SetHeader("Retry-After", "5")
		}
		return "", nil, zip.Errorf(http.StatusServiceUnavailable, "the key is being sealed; try again shortly")
	case errors.Is(err, secret.ErrUnsealed):
		return "", nil, zip.Errorf(http.StatusServiceUnavailable, "network access is not configured")
	}
	if err != nil {
		return "", nil, o.unavailable("the invite key cannot be read or sealed", err)
	}
	return org, key, nil
}

// watch counts one event against the deployment's count called name and says so, once in
// each window, when the count passes limit. It refuses no one: a count that shut a door at
// a threshold would let any crowd of addresses close it on everyone.
func (o ops) watch(ctx context.Context, name string, limit int, every time.Duration, said string) {
	left, _ := o.s.Coord.Spend(ctx, name, []string{"all"}, []int{1}, limit, every)
	if left >= 0 {
		return
	}
	first, err := o.s.Coord.Once(ctx, alarmName, name, every)
	if err != nil || first {
		o.s.Log.Warn(said, "limit", limit, "window", every.String())
	}
}

// unavailable is a store or a key that did not answer: logged without a value, and
// told to the caller as 503, never as a verdict.
func (o ops) unavailable(what string, err error) error {
	o.s.Log.Warn("network: "+what, "err", err)
	return zip.Errorf(http.StatusServiceUnavailable, "network access is unavailable")
}
