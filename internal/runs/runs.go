package runs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
)

// Held is the question: which sessions have a run executing in you?
const Held = "agents.held"

// Wait bounds the question, so a pod that does not answer delays a sweep by this and
// no more.
var Wait = 3 * time.Second

// Answer is a process's reply to Held: the sessions it executes, and no when it
// executes none.
func Answer(ids []string) ([]byte, bool) {
	if len(ids) == 0 {
		return nil, false
	}
	b, err := json.Marshal(ids)
	return b, err == nil
}

// ErrAlone is a process that cannot ask the others: its coord answers for itself alone,
// so the processes beside it, which may be executing runs, cannot hear the question.
var ErrAlone = errors.New("runs: this process cannot ask the others which runs they execute")

// Others are the sessions every OTHER process of the deployment is executing, and an
// error when they could not all be asked: a run may be executing that the asker cannot
// see, and the one thing it must not do then is say none is. A nil coord is the process
// that executes every run there is, which has no others. A process that does not
// execute them, and has no shared coord, cannot ask (ErrAlone).
func Others(ctx context.Context, c *coord.Coord) (map[string]bool, error) {
	out := map[string]bool{}
	if c == nil {
		return out, nil
	}
	if !c.Shared() {
		return out, ErrAlone
	}
	ctx, cancel := context.WithTimeout(ctx, Wait)
	defer cancel()
	answers, err := c.Gather(ctx, Held, nil)
	for _, a := range answers {
		var ids []string
		if json.Unmarshal(a.Body, &ids) == nil {
			for _, id := range ids {
				out[id] = true
			}
		}
	}
	return out, err
}
