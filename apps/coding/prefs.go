package coding

// prefs.go is what a person set for their runs. The pref app holds each person's
// preference document (apps/pref), and two of its keys are a run's:
//
//	instructions  the person's own instructions, which the run's harness reads
//	              as its user's (sandboxrunner.go equip)
//	code          {model, effort, …}: what a run thinks with when its request
//	              names nothing
//
// The run asks as the person, when it is admitted, because the document is theirs
// and keyed on them; a run started in the background acts only for an org and has
// nobody to ask about.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/client"
	prefpeer "github.com/hanzoai/cloud/client/pref"
	"github.com/hanzoai/cloud/sdk/plane"
)

// Preferred is what one person set for their runs.
type Preferred struct {
	Instructions  string
	Model, Effort string
}

// maxInstructions bounds a person's instructions. The document they live in is
// bounded smaller (apps/pref maxDoc), so this is the boundary's own statement
// rather than a limit a person meets.
const maxInstructions = 16 << 10

// ErrSettings is a person's settings the run could not read. The endpoint answers
// it 503: nothing the caller asked for was wrong.
var ErrSettings = errors.New("coding: your settings could not be read")

// preferred reads what the person on ctx set for their runs.
//
// A deployment that does not serve preferences answers none, and the run takes
// the platform's defaults. One that serves them and cannot answer refuses the run,
// as does a document the run cannot read: a run started without the instructions
// its person set is a different run from the one they asked for.
func preferred(ctx context.Context) (Preferred, error) {
	c, cancel := bounded(ctx)
	defer cancel()
	own, err := prefpeer.PrefOwn(c, &client.PrefsIn{})
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return Preferred{}, nil
	case err != nil:
		return Preferred{}, fmt.Errorf("%w: %v", ErrSettings, err)
	case own == nil:
		return Preferred{}, fmt.Errorf("%w: the pref app answered nothing", ErrSettings)
	}
	return preferences(own.Doc)
}

// preferences reads a run's two keys out of a preference document. Each key is
// read on its own, so a key another surface owns cannot make these unreadable,
// and one of the wrong shape is refused by its name.
func preferences(doc string) (Preferred, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &keys); err != nil {
		return Preferred{}, fmt.Errorf("coding: your settings are not a JSON object")
	}
	var p Preferred
	if raw, ok := keys["instructions"]; ok {
		if err := json.Unmarshal(raw, &p.Instructions); err != nil {
			return Preferred{}, fmt.Errorf("coding: your settings' instructions are not text")
		}
		p.Instructions = strings.TrimSpace(p.Instructions)
		if len(p.Instructions) > maxInstructions {
			return Preferred{}, fmt.Errorf("coding: your instructions are over %d KiB", maxInstructions>>10)
		}
	}
	if raw, ok := keys["code"]; ok {
		var code struct {
			Model  string `json:"model"`
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(raw, &code); err != nil {
			return Preferred{}, fmt.Errorf("coding: your settings' code defaults are not a model and an effort")
		}
		p.Model, p.Effort = strings.TrimSpace(code.Model), strings.TrimSpace(code.Effort)
	}
	return p, nil
}
