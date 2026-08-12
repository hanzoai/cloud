package forge

// login.go answers ONE question: what is this person called ON THE FORGE?
//
// It exists because Sudo takes a LOGIN and nothing else. Handing it anything
// else — an id, an email, a display name — is answered "unknown actor", which
// reads as "this user has no forge identity" and is indistinguishable from the
// user genuinely not having one. That is how a coding run came to refuse every
// caller: the door passed the token's `sub`, a UUID, and the forge said it had
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
	"regexp"
	"strings"
	"unicode"

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
