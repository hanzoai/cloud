package main

// resume_test.go — a lost run is asked again from what its session recorded, which has to
// include what was said to it while no pod held it. The first resume gave it that in its
// task; a second resume rebuilds the task from the session, and drops it unless the
// session kept it.

import (
	"strconv"
	"testing"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/coding"
)

// sessionEvents is a session's recorded events, paged as the store pages them.
type sessionEvents []agents.Event

func (es *sessionEvents) add(kind, payload string) {
	*es = append(*es, agents.Event{Seq: int64(len(*es) + 1), Kind: kind, Payload: payload})
}

func (es sessionEvents) page(since int64, limit int) ([]agents.Event, error) {
	var out []agents.Event
	for _, e := range es {
		if e.Seq > since && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

// The trace: a run is started, works long enough to record more events than a page holds,
// is lost, and is resumed with two instructions that were said while no pod held it. The
// first resume recorded them on the session. The resumed run is lost again, and the second
// resume is asked of the session alone: it is given the task and both instructions, in
// order, and not the message a turn took in place, which its dev thread already holds.
//
// MUTATION: build the task from the first message only (the old rule) and the second
// resume drops both instructions.
func TestASecondResumeIsGivenWhatTheFirstWas(t *testing.T) {
	var es sessionEvents
	es.add(agents.KindMessage, `{"role":"user","text":"fix the header"}`)
	es.add(agents.KindStatus, `{"status":"started","tool":"dev","model":"zen"}`)
	for i := range 1300 {
		es.add(agents.KindLog, `{"message":"step `+strconv.Itoa(i)+`"}`)
	}
	es.add(agents.KindMessage, `{"role":"user","text":"a word the turn took in place"}`)
	es.add(agents.KindStatus, `{"status":"resumed"}`)
	es.add(agents.KindMessage, `{"role":"user","text":"use the other endpoint","queued":true,"requestId":"r1"}`)
	es.add(agents.KindMessage, `{"role":"user","text":"and add a test","queued":true}`)

	in, ok := resumeAsk(es.page, agents.Session{ID: "sess_q", Org: "acme", Repo: "acme/api", Base: "main", Branch: "agent/q"})
	if !ok {
		t.Fatal("a run resumed once was not resumed again")
	}
	if want := coding.Said("fix the header", []string{"use the other endpoint", "and add a test"}); in.Prompt != want {
		t.Fatalf("the second resume's task is %q, want %q", in.Prompt, want)
	}
	if in.Tool != "dev" || in.Model != "zen" || in.Repo != "acme/api" {
		t.Fatalf("the second resume lost what the run was started with: %+v", in)
	}
}

// A run that ended, or was resumed twice, is not resumed; one nothing was said to is given
// the task it was started with.
func TestAResumeAsksOnlyForARunThatCanBeResumed(t *testing.T) {
	var plain sessionEvents
	plain.add(agents.KindMessage, `{"role":"user","text":"fix the header"}`)
	plain.add(agents.KindStatus, `{"status":"started"}`)
	if in, ok := resumeAsk(plain.page, agents.Session{}); !ok || in.Prompt != "fix the header" {
		t.Fatalf("a run nothing was said to is asked (%q, %v)", in.Prompt, ok)
	}
	ended := append(sessionEvents(nil), plain...)
	ended.add(agents.KindStatus, `{"status":"completed"}`)
	if _, ok := resumeAsk(ended.page, agents.Session{}); ok {
		t.Fatal("a run that ended was resumed")
	}
	twice := append(sessionEvents(nil), plain...)
	twice.add(agents.KindStatus, `{"status":"resumed"}`)
	twice.add(agents.KindStatus, `{"status":"resumed"}`)
	if _, ok := resumeAsk(twice.page, agents.Session{}); ok {
		t.Fatal("a run resumed twice was resumed again")
	}
	var none sessionEvents
	if _, ok := resumeAsk(none.page, agents.Session{}); ok {
		t.Fatal("a run that left no task was resumed")
	}
}
