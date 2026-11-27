package network

// invite.go — AN INVITE CODE, and the question a stranger asks of one.
//
// A code is HNZ- and ten characters of Crockford base32 (0-9 and A-Z without I, L, O
// and U), shown in two groups of five, as HNZ-7K9Q2-MXZ4P, and drawn from crypto/rand:
// 32^10 of them, about 1.1e15. It is the one credential an invitation is: whoever holds
// it may be let in, so it is shown once, at issue, and only a keyed hash of it is stored.
//
// Verifying a code is open to strangers (POST /v1/network/invite/verify): a person
// types a code the page asks them for, before they have an account. That makes the
// op a guessing oracle, so it is built to be one that says almost nothing.
//
// IT NEVER SAYS WHY. A code never issued, one that only looks like a code, one
// revoked and one expired are answered alike: the same status, the same body, and
// the same work, one lookup and one constant-time comparison, whatever the input. The
// only refusals are about the request (the deployment, the caller's rate, the body)
// and one about the count of misses, which is told to a caller whatever it asked.
//
// GUESSING IS BOUNDED PER CALLER. Each caller is held to verifyRate verifications an
// hour and to missRate MISSES an hour, then 429 until the window passes; an IPv6
// caller is counted by its /64 and its /48. A miss is reserved before the lookup, so a
// flood of parallel guesses cannot overshoot, and given back when the code turns out to
// be live, so a hit is never a miss. The deployment's own count of misses refuses no
// one: it says once, in the log, that the budget for the window is spent. A count that
// shut the door at a threshold would let any crowd of callers close it on everyone.
//
// VERIFYING DOES NOT SPEND THE CODE. It stays good until it expires or is revoked.
// Nothing on the server redeems a code yet, so nothing yet refuses a person for lacking
// one.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/spec"
	addr "github.com/hanzoai/ai/address"
	"github.com/zap-proto/zip"
)

const (
	// codePrefix leads every code, and codeAlphabet is Crockford's base32: the digits
	// and the letters but I, L, O and U, which are read as 1, 1, 0 and nothing.
	codePrefix   = "HNZ"
	codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	// codeLen is how many characters of the alphabet follow the prefix, and codeGroup
	// how many make a group.
	codeLen   = 10
	codeGroup = 5
	// longest is the most of a spelling that is read: a code with every blank and
	// hyphen the shape allows between its characters.
	longest = 64
	// decoy is what a string that is not a code is looked up as, so it costs the same
	// lookup as one that is. Its answer is never used.
	decoy = "HNZ-00000-00000"
)

// lookalike folds the characters Crockford reads as another: I and L are 1, O is 0.
func lookalike(c byte) byte {
	switch c {
	case 'I', 'L':
		return '1'
	case 'O':
		return '0'
	}
	return c
}

// normalise is the one spelling a code has, HNZ-XXXXX-XXXXX: upper-cased, every hyphen
// and blank dropped wherever it stands, I and L read as 1 and O as 0. It accepts ASCII
// only (a letter that only upper-cases to an ASCII one is not that letter) and answers
// false for anything that is not HNZ and ten characters of the alphabet, with no
// reason.
func normalise(s string) (string, bool) {
	if len(s) > longest {
		return "", false
	}
	b := make([]byte, 0, len(s))
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 0x80:
			return "", false
		case c == '-' || c == ' ' || c == '\t' || c == '\r' || c == '\n':
			continue
		case c >= 'a' && c <= 'z':
			c -= 'a' - 'A'
		}
		b = append(b, c)
	}
	rest, ok := strings.CutPrefix(string(b), codePrefix)
	if !ok || len(rest) != codeLen {
		return "", false
	}
	body := make([]byte, codeLen)
	for i := range codeLen {
		body[i] = lookalike(rest[i])
		if strings.IndexByte(codeAlphabet, body[i]) < 0 {
			return "", false
		}
	}
	return codePrefix + "-" + string(body[:codeGroup]) + "-" + string(body[codeGroup:]), true
}

// newCode draws a fresh code from crypto/rand.
func newCode() (string, error) {
	body := make([]byte, codeLen)
	for i := range body {
		n, err := pick(len(codeAlphabet))
		if err != nil {
			return "", err
		}
		body[i] = codeAlphabet[n]
	}
	return codePrefix + "-" + string(body[:codeGroup]) + "-" + string(body[codeGroup:]), nil
}

// pick is a uniform integer in [0, n) from crypto/rand by rejection sampling: a
// byte at or past the largest multiple of n below 256 is drawn again, so no value
// is likelier than another.
func pick(n int) (int, error) {
	limit := 256 - 256%n
	var b [1]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		if int(b[0]) < limit {
			return int(b[0]) % n, nil
		}
	}
}

// mac is the HMAC-SHA-256 of a value under key: of a normalised code, the only form
// in which a code is stored, and, behind a domain prefix, of a waitlist identifier.
func mac(key []byte, v string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(v))
	return m.Sum(nil)
}

// inviteID is the id an invite is kept under: the first 15 characters of its MAC in hex.
func inviteID(sum []byte) string { return hex.EncodeToString(sum)[:15] }

// InviteCheckIn is what the page sends to ask whether a code is live. Only this
// field is read.
//
// The two unexported fields record the body's size and shape at decode, so the op
// answers its gates in order rather than having the decoder answer 400 first.
type InviteCheckIn struct {
	oversize  bool
	malformed bool

	// Code is the invite code as the person has it, such as HNZ-7K9Q2-MXZ4P. Required.
	// Letters may be upper or lower case, the hyphens and spaces may be left out or put
	// anywhere, and I and L are read as 1 and O as 0, so a code copied by eye is found.
	// Anything that is not a code is not an error: it is answered as a code that is not
	// valid.
	Code string `json:"code"`
}

// UnmarshalJSON records the body's size and whether it decoded into this shape, and
// refuses nothing: every judgement stays in the op, in its order.
func (in *InviteCheckIn) UnmarshalJSON(b []byte) error {
	type body InviteCheckIn // sheds the method, so this does not recurse
	var v body
	oversize := len(b) > maxAccessBody
	malformed := json.Unmarshal(b, &v) != nil
	*in = InviteCheckIn(v)
	in.oversize, in.malformed = oversize, malformed
	return nil
}

// InviteVerdict is whether a code is live.
type InviteVerdict struct {
	// Valid is true when the code was issued by this deployment, is not revoked and
	// has not expired, and false for every other reason, none of which is said.
	Valid bool `json:"valid"`
}

// verifyInvite says whether an invite code is live: {"valid": true} or {"valid":
// false}, and nothing else. It never says why a code is not valid: a code that was
// never issued, one that is not shaped like a code, one that was revoked and one that
// has expired get the same status and the same body, and it says nothing of who issued
// a code. Verifying does not consume the code and does not spend it, and it needs no
// account. Nothing on the server enforces an invite until redemption exists; the
// operations that will need one will answer with the problem type invite_required.
//
// A deployment that cannot hold invites, or has issued none yet, answers 503; then a
// caller past ten requests an hour is 429, a body over 4 KiB 413 and a missing code or a
// body that is not JSON 400, in that order. A caller that has missed five times in an
// hour is refused 429 until the window passes, whatever it asks, so the refusal says
// nothing about whether a code would have been valid, and Retry-After says when to come
// back. The limits are per network address (an IPv6 /64, with the /48 around it counted
// at sixteen times as much): clients behind one address share them, and so do callers
// whose address is unknown.
//
// Example: {"code": "HNZ-7K9Q2-MXZ4P"}
// Response: {"valid": true}
func (o ops) verifyInvite(ctx context.Context, in *InviteCheckIn) (*InviteVerdict, error) {
	org, key, err := o.door(ctx, false)
	if err != nil {
		return nil, err
	}
	ck := countKey(key)
	buckets := callerBuckets(ck, clientOf(ctx))
	if ok, wait := o.spend(ctx, verifyName, buckets, verifyRate, verifyWindow); !ok {
		return nil, tooMany(ctx, wait)
	}
	if err := o.hold(ctx, org); err != nil {
		return nil, err
	}
	if in.oversize {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "request too large")
	}
	if in.malformed || strings.TrimSpace(in.Code) == "" {
		return nil, zip.ErrBadRequest("a code is required")
	}
	code, shaped := normalise(in.Code)
	if !shaped {
		code = decoy
	}
	give, err := o.reserveMiss(ctx, buckets)
	if err != nil {
		return nil, err
	}
	sum := mac(key, code)
	row, err := readInvite(ctx, org, inviteID(sum))
	if err != nil {
		give()
		return nil, o.unavailable("the Base did not answer an invite lookup", err)
	}
	stored := make([]byte, sha256.Size)
	if row != nil {
		if b, err := hex.DecodeString(row.CodeHMAC); err == nil {
			stored = b
		}
	}
	live := row != nil && row.Status == statusActive && time.Now().Before(unstamp(row.ExpiresAt))
	valid := hmac.Equal(sum, stored) && live && shaped
	if valid {
		give()
	} else {
		o.countMiss(ctx)
	}
	return &InviteVerdict{Valid: valid}, nil
}

// reserveMiss counts one miss against the caller before the lookup, and refuses 429
// when the caller's are spent. It answers the function that gives the miss back, for a
// code that turned out to be live or a fault that was not a guess. Counting before the
// lookup is what bounds guesses in flight: a refused request spends nothing, an
// admitted one holds its place until it is known. The shared count says how many a
// caller has spent and not when its window ends, so a refusal says to come back in a
// window's length.
func (o ops) reserveMiss(ctx context.Context, buckets []addr.Bucket) (func(), error) {
	ws := make([]coord.Window, len(buckets))
	keys := make([]string, len(buckets))
	for i, b := range buckets {
		ws[i] = coord.Window{Key: b.Key, Limit: missRate * b.Scale, TTL: missWindow}
		keys[i] = b.Key
	}
	if _, ok := o.s.Coord.Quota(ctx, missName, ws); !ok {
		return nil, tooMany(ctx, missWindow)
	}
	return func() { o.s.Coord.Unquota(context.WithoutCancel(ctx), missName, keys) }, nil
}

// countMiss counts a miss against the deployment, which says so once a window past its
// budget and refuses no one.
func (o ops) countMiss(ctx context.Context) {
	o.watch(ctx, budgetName, missBudget, missWindow,
		"network: the invite miss budget is spent for this window; verification is still answered")
}

// Verifying is open to a stranger: the document asks no credential of it.
func init() {
	spec.Open("/v1/network/invite/verify", http.MethodPost)
}
