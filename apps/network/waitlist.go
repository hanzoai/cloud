package network

// waitlist.go — THE WAITLIST for Hanzo Network, open to a stranger.
//
// A person who is not in yet leaves an address, or their X handle, and the role they
// come as (POST /v1/network/waitlist). Nothing is mailed and nothing is confirmed: the
// entry is a place in a line, not a subscription, so there is no link to press and
// no consent to record, and an address can be taken by anyone. What the op owes the
// person it names is that it tells a third party nothing about them.
//
// THE ANSWER SAYS NOTHING ABOUT THE IDENTIFIER. It is 202 pending for one never seen
// and one asked for before alike, however many times it has been asked for, and with
// the same headers, and it does the same work for each: it reads the entry and upserts
// it. There is no count of an identifier's asks to differ by, because none is needed:
// a write sends nothing to anyone and is an idempotent upsert of the one record an
// identifier is, so repeats cost one row, and the caller's own rate bounds the writes. A
// repeat changes nothing, so the first role, source and language stand, and when. Only
// that rate is 429, and it says nothing of who is asked for. The deployment's hourly
// count of entries is said aloud past its threshold and refuses no one.
//
// AN X HANDLE IS NOT LOOKED UP. It is a name the person typed, kept as they gave it
// once its @ is dropped and its shape is judged, and never asked of IAM, of X or of
// anything else: an anonymous endpoint that answered differently for a handle that is
// somebody's would be a directory of who has an account. No wallet address is taken.
//
// An entry holds its kind, the HMAC digest of its identifier, its role, where the
// form was and the person's language, and when. It holds nothing about the caller:
// not the address the request came from, not its agent, not a header. No email or
// handle is stored: the digest is keyed by the deployment's one invite secret, under
// a domain of its own, so neither the value nor its use as an invite code is read
// back from the row.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

// The kinds of identifier.
const (
	kindEmail = "email"
	kindX     = "x"
)

// roles are the ways a person comes.
var roles = map[string]bool{"miner": true, "builder": true, "researcher": true, "staker": true}

// xShape is an X handle as typed, without its @: letters, digits and underscores, one to
// fifteen. It is judged before it is lower-cased, so a character that only folds to one
// of these is not one.
var xShape = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

// WaitlistIn is what the form sends. Exactly one of email and xHandle is read.
//
// The two unexported fields record the body's size and shape at decode, so the op
// answers its gates in order rather than having the decoder answer 400 first.
type WaitlistIn struct {
	oversize  bool
	malformed bool

	// Email is the address to put on the waitlist: at most 320 characters, one
	// address. Send this or xHandle, never both.
	Email string `json:"email"`
	// XHandle is the X handle to put on the waitlist: one to fifteen letters, digits
	// and underscores, with or without a leading @. It is kept without the @ and in the
	// case it was given, and is never looked up. Send this or email, never both.
	XHandle string `json:"xHandle"`
	// Role is how the person comes: miner, builder, researcher or staker, in any case.
	// Required.
	Role string `json:"role"`
	// Source is where the form was, such as hanzo.network/join. Optional; one line,
	// clipped at 200 characters.
	Source string `json:"source"`
	// Locale is the person's language as a BCP 47 tag, such as en or pt-BR. Optional;
	// any other value is recorded as none.
	Locale string `json:"locale"`
}

// UnmarshalJSON records the body's size and whether it decoded into this shape, and
// refuses nothing: every judgement stays in the op, in its order.
func (in *WaitlistIn) UnmarshalJSON(b []byte) error {
	type body WaitlistIn // sheds the method, so this does not recurse
	var v body
	oversize := len(b) > maxAccessBody
	malformed := json.Unmarshal(b, &v) != nil
	*in = WaitlistIn(v)
	in.oversize, in.malformed = oversize, malformed
	return nil
}

// WaitlistEntry is where a waitlist request stands.
type WaitlistEntry struct {
	// Status is "pending": the entry is on the waitlist and will be told when there
	// is room. It is the same answer whatever the identifier's standing.
	Status string `json:"status"`
}

// waitDomain separates a waitlist digest from an invite code's MAC and from the
// counts derived from the same key, so one KMS secret serves all three and a
// value used for one cannot stand for another.
const waitDomain = "waitlist\x00"

// waitID is the id an entry is kept under: the first 15 hex characters of the
// HMAC-SHA-256 of its domain, kind and normalized identifier under the
// deployment's invite key. It holds no identifier: neither an email nor an X
// handle is stored. An X handle is the same handle in any case.
func waitID(key []byte, kind, identifier string) string {
	sum := mac(key, waitDomain+kind+"\x00"+strings.ToLower(identifier))
	return hex.EncodeToString(sum)[:15]
}

// identifier is the one identifier a request carries, in the spelling it is kept
// under, and its kind; or the 400 for a request that carries none, both, or one that
// is not shaped like what it says.
func identifier(in *WaitlistIn) (kind, id string, err error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	x, _ := strings.CutPrefix(strings.TrimSpace(in.XHandle), "@")
	switch {
	case (email != "") == (strings.TrimSpace(in.XHandle) != ""):
		return "", "", zip.ErrBadRequest("exactly one of email and xHandle is required")
	case email != "":
		if len(email) > maxEmail || !text.Email(email) {
			return "", "", zip.ErrBadRequest("a valid email is required")
		}
		return kindEmail, email, nil
	case !xShape.MatchString(x):
		return "", "", zip.ErrBadRequest("an X handle is one to fifteen letters, digits or underscores")
	}
	return kindX, x, nil
}

// joinWaitlist puts an address or an X handle on the Hanzo Network waitlist and answers
// 202 pending. Nothing is mailed. It gives the same answer for an identifier that is
// new and one asked for before, however often, and with the same headers, so it says
// nothing about who is on the list; a repeat changes nothing, and the first entry
// stands. An X handle is never looked up anywhere and no wallet address is taken. No
// account is needed, and the entry keeps nothing about the caller. It also keeps
// no email or handle: only a keyed HMAC digest of the identifier, under the one
// invite secret with a domain of its own, is filed.
//
// A deployment that cannot hold the waitlist, or has issued no invite yet, answers 503;
// then a caller past ten requests an hour is 429 (with Retry-After), a body over 4 KiB
// 413, and a body that is not JSON, one with neither or both of email and xHandle, an
// invalid one or a role that is not miner, builder, researcher or staker 400, in that
// order. The ten an hour are per network address: clients behind one address share them,
// and so do callers whose address is unknown. Nothing here is gated by an invite yet; the operations that will be answer with
// the problem type invite_required.
//
// Example: {"email": "ada@example.com", "role": "builder", "source": "hanzo.network/join", "locale": "en"}
// Response: {"status": "pending"}
func (o ops) joinWaitlist(ctx context.Context, in *WaitlistIn) (*WaitlistEntry, error) {
	org, key, err := o.door(ctx, false)
	if err != nil {
		return nil, err
	}
	ck := countKey(key)
	if ok, wait := o.spend(ctx, waitName, callerBuckets(ck, clientOf(ctx)), waitRate, waitWindow); !ok {
		return nil, tooMany(ctx, wait)
	}
	if err := o.hold(ctx, org); err != nil {
		return nil, err
	}
	if in.oversize {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "request too large")
	}
	if in.malformed {
		return nil, zip.ErrBadRequest("invalid request body")
	}
	kind, id, err := identifier(in)
	if err != nil {
		return nil, err
	}
	role := strings.ToLower(strings.TrimSpace(in.Role))
	if !roles[role] {
		return nil, zip.ErrBadRequest("role is one of miner, builder, researcher or staker")
	}
	// The deployment's count of entries is said aloud past its threshold and refuses no one.
	o.watch(ctx, burstName, waitBurst, burstWindow,
		"network: the waitlist count is past its hourly threshold; entries are still taken")
	// The entry is read and written whether or not there is one: a repeat writes what
	// it read, so what it does is what a new one does and nothing it names is kept.
	digest := waitID(key, kind, id)
	row, err := readWait(ctx, org, digest)
	if err != nil {
		return nil, o.unavailable("the Base did not answer a waitlist read", err)
	}
	if row == nil {
		row = &waitRow{
			ID: digest, Kind: kind, Role: role,
			Source: text.Trim(text.OneLine(in.Source), maxLine), CreatedAt: stamp(time.Now()),
		}
		if l := strings.TrimSpace(in.Locale); text.Tag(l) {
			row.Locale = l
		}
	}
	if err := writeWait(ctx, org, *row); err != nil {
		return nil, o.unavailable("the Base did not take a waitlist entry", err)
	}
	return &WaitlistEntry{Status: "pending"}, nil
}

// The waitlist is a stranger's: the document asks no credential of it.
func init() {
	spec.Open("/v1/network/waitlist", http.MethodPost)
}
