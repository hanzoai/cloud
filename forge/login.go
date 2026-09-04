package forge

// login.go answers ONE question: what is this person called ON THE FORGE?
//
// It exists because Sudo takes a LOGIN and nothing else. Handing it anything
// else — an id, an email, a display name — is answered "unknown actor", which
// reads as "this user has no forge identity" and is indistinguishable from the
// user genuinely not having one. That is how a coding run came to refuse every
// caller: the endpoint passed the token's `sub`, a UUID, and the forge said it had
// never heard of them. It had not.
//
// # The rule is the forge's, not ours
//
// This deployment registers users through OIDC with oauth2_client.USERNAME set
// to `email` (hanzo-git.yaml), so the forge derives the login by running the
// address through its own NormalizeUserName (models/user/user.go), which takes
// the part before the @ and folds it to something a username may contain.
// [Login] reproduces that transformation and nothing else — same order, same
// character classes — because a second, approximate rule would agree for most
// people and quietly address the wrong account for the rest.
//
// It is a DERIVATION and not a lookup, which is what makes it safe to do here:
// there is no state to go stale. If the deployment ever sets USERNAME to
// something else, this becomes wrong for everyone at once and loudly — every
// run refuses — rather than wrong for a few people silently.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/hanzoai/cloud/client/iam"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// The forge's own normalisation, spelled the same way it is over there. The
// character sets are copied deliberately rather than approximated: a set that
// merely looks similar produces a login that merely looks right.
var (
	// Æ is expanded rather than stripped, so it survives as letters.
	loginExpand = strings.NewReplacer("Æ", "AE")
	// Quotes and accents are DELETED, not replaced — they join the characters
	// either side of them.
	loginDrop = regexp.MustCompile("['`´]")
	// Whitespace and the two punctuation marks a username may not carry become a
	// hyphen.
	loginHyphen = regexp.MustCompile(`[\s~+]`)
	// Diacritics are decomposed and their marks removed, so é becomes e.
	loginFold = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
)

// Login is the forge username for an IAM email address, empty when there is
// none to derive.
//
// Empty is a real answer and the caller must refuse on it. A person with no
// email claim has no login this can compute, and guessing one would address
// whichever account happens to hold the guess.
func Login(email string) string {
	s := strings.TrimSpace(email)
	if s == "" {
		return ""
	}
	// The local part, exactly as the forge takes it: everything before the FIRST
	// @, which is also what makes this a no-op for a value that is already a bare
	// username.
	s, _, _ = strings.Cut(s, "@")
	folded, _, err := transform.String(loginFold, loginExpand.Replace(s))
	if err != nil {
		// The forge fails the registration here rather than storing a mangled name,
		// so a caller that cannot derive the login must refuse for the same reason.
		return ""
	}
	return loginHyphen.ReplaceAllLiteralString(loginDrop.ReplaceAllLiteralString(folded, ""), "-")
}

// ErrNotYours means the login derived from a caller's address belongs to a
// DIFFERENT person on the forge. It is the refusal that makes [Login] safe to
// use as an identity.
var ErrNotYours = errors.New("forge: that login belongs to someone else")

// LoginFor is [Login] with the ownership PROVEN, and it is the only one a
// caller should reach for.
//
// # Why deriving is not enough
//
// [Login] takes the local part of an address, so it maps MANY addresses onto ONE
// login: z@hanzo.ai and z@anywhere.example both derive `z`. IAM's own uniqueness
// does not help, because it is enforced on the whole address — the collision is
// created here, by dropping the domain.
//
// That turns a guessable string into an identity. Anyone who can obtain an
// account whose address begins with a colleague's local part derives that
// colleague's login, and everything downstream — Sudo, the entitlement read, the
// deploy key — is then performed AS THEM. On a shared signup org, where every
// self-serve account lands beside the staff (account.SignupOrg), the person
// obtaining that account can be a stranger.
//
// So the derived login is treated as a GUESS and checked against the forge:
// the user it names must carry the caller's own address. A stranger deriving a
// colleague's login is refused because the colleague's forge account holds the
// colleague's address, not theirs.
//
// # What it rests on
//
// The forge answers a site administrator with the user's real email, and answers
// everyone else with a placeholder (services/convert/user.go toUser: the address
// is returned only when the doer is the user or an admin). The machine
// credential is an administrator — Sudo requires it — so this reads the true
// value, and a deployment where it somehow did not would compare against
// `name@noreply.…` and refuse. It fails closed either way.
//
// # What it does not prove
//
// That the CALLER'S address is theirs. This deployment's tokens carry `email`
// and no `email_verified`, and a direct password signup records the address
// unverified, so an address nobody has registered in IAM yet can still be
// claimed by whoever registers it first. Where that address already belongs to
// somebody, IAM's uniqueness refuses the second registration; where it belongs
// to a forge account with no IAM account beside it, this check can be satisfied
// by claiming it. Closing that needs a verified-email claim in the token.
func (c *Client) LoginFor(ctx context.Context, email string) (string, error) {
	login := Login(email)
	if login == "" {
		return "", fmt.Errorf("forge: no login can be derived from %q", email)
	}
	var u struct {
		Login string `json:"login"`
		Email string `json:"email"`
	}
	err := c.Machine().do(ctx, "/users/"+url.PathEscape(login), nil, &u)
	if isMissing(err) {
		return "", fmt.Errorf("%w: %s", ErrUnknownActor, login)
	}
	if err != nil {
		return "", err
	}
	// Case-insensitive: an address is not case-sensitive in its domain and is
	// treated as insensitive throughout this estate. Both sides are trimmed
	// because one comes off a header.
	if !strings.EqualFold(strings.TrimSpace(u.Email), strings.TrimSpace(email)) {
		return "", fmt.Errorf("%w: %s is not %s", ErrNotYours, login, email)
	}
	return login, nil
}

// Caller is the forge login the REQUEST'S CALLER has proved is theirs, and it is
// the one way any surface should resolve who to act as.
//
// It is distinct from [Client.Actor], which reports who this client is already
// scoped to: that is state, this is a resolution. The usual shape is
// c.As(c.Caller(ctx)).
//
// It composes the two proofs that a forge identity needs, and both are here
// rather than at the call sites because a caller that had to remember them is a
// caller that will eventually be written without one:
//
//	the address is CONFIRMED   IAM records whether the person proved it, and a
//	                           direct password signup records FALSE until they
//	                           do. An address somebody merely typed is a claim.
//	the address IS the account's   [Client.LoginFor] settles that with the forge,
//	                           because a login is a local part and many
//	                           addresses derive one.
//
// Neither alone is enough. Confirming proves the person holds the address but
// not that the login it derives is theirs; the forge check proves the login
// matches an address but not that the caller holds it. Together the only way to
// act as z@hanzo.ai is to have proved z@hanzo.ai.
//
// THE ADDRESS COMES FROM IAM, not from the request. The identity header would do
// — it is minted from validated claims — but asking the store means the single
// input to this whole chain is the caller's SUBJECT, which the plane carries and
// nothing can forge, and the address is read from the same record that says
// whether it was proved. One source, one moment, no gap between the two facts.
//
// Fail closed at every step: an unreachable store, a principal it does not know,
// an unconfirmed address and a login that belongs to somebody else are each a
// refusal. There is no path here that ends at the machine identity.
func (c *Client) Caller(ctx context.Context) (string, error) {
	who, err := iam.IAMEmail(ctx)
	if err != nil {
		return "", fmt.Errorf("forge: could not establish who this caller is: %w", err)
	}
	if who == nil || strings.TrimSpace(who.Address) == "" {
		return "", fmt.Errorf("forge: this caller has no address, so it has no forge identity")
	}
	if !who.Verified {
		// Named plainly, because it is the one refusal here a person can act on
		// themselves — and because it is the intended behaviour rather than a
		// fault: an unconfirmed address cannot be the basis of an identity.
		return "", fmt.Errorf("forge: verify %s first — an unconfirmed address cannot be used "+
			"to act as anyone on the forge", who.Address)
	}
	return c.LoginFor(ctx, who.Address)
}
