package coding

// author.go — who a run's commit is authored by.
//
// In a workspace the platform made, the person a run is for asked for the
// change, so they are its AUTHOR; the agent made it, so it is the COMMITTER
// (sandboxrunner.go deliver). Both are facts, and git keeps a field for each.
// Every other run — the estate's, and GitHub's — is authored by the agent alone
// ([authorIn]).

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/forge"
)

// Author is a person as a commit names them. The zero value names nobody.
type Author struct{ Name, Email string }

// address is the shape an author's address must have to reach git: one @, and
// nothing git would read as the end of the field.
var address = regexp.MustCompile(`^[^\s<>@]{1,64}@[^\s<>@]{1,190}$`)

// author is the caller on ctx as their commit's author — their display name and
// their address, read from IAM.
//
// Only an address they have PROVED. One a person merely typed names nobody
// (client.Email), and a commit authored under it would credit the change to
// whoever does hold that address. Unproved, or unreadable, the answer is the
// zero Author, and the agent is then both author and committer: attribution
// is a record, and a record that cannot be stated truly is left out, never
// guessed.
func author(ctx context.Context) Author {
	call, cancel := bounded(ctx)
	defer cancel()
	who, err := iampeer.IAMEmail(call)
	if err != nil || who == nil || !who.Verified {
		return Author{}
	}
	return Author{Name: who.Name, Email: who.Address}
}

// authorIn is the author of a run's commit: the person, for a run on the forge
// in a workspace the platform made; the zero Author — the agent is author and
// committer — for every other run, the estate's and GitHub's, whose commits are
// authored exactly as they were before a person was ever named.
func authorIn(ctx context.Context, ws forge.Workspace, onForge bool) Author {
	if !ws.Made || !onForge {
		return Author{}
	}
	return author(ctx)
}

// crud is what git trims from both ends of an identity's name, so a name made
// only of it is an empty name, which `git commit` refuses.
const crud = ".,:;<>\"\\'"

// line is the Author as `git commit --author` takes it, "" when there is nobody
// to name. The name is cleaned of anything that would end the field, break the
// line or reorder how it displays, and an Author with no name is named by their
// address's local part.
func (a Author) line() string {
	email := strings.TrimSpace(a.Email)
	if !address.MatchString(email) {
		return ""
	}
	name := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, a.Name)), " ")
	if r := []rune(name); len(r) > 100 {
		name = strings.TrimSpace(string(r[:100]))
	}
	name = strings.Trim(name, crud+" ")
	if name == "" {
		local, _, _ := strings.Cut(email, "@")
		name = strings.Trim(local, crud)
	}
	if name == "" {
		return ""
	}
	return name + " <" + email + ">"
}
