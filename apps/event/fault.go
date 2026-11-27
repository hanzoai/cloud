// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package event

import "time"

// fault.go — an error fact, as a consumer outside this package reads it.
//
// /v1/event is the one way an error enters. The write core scrubs it, computes its
// grouping key (`issue`, applyFault) and publishes it on event.error; the warehouse
// durable lands it in event.fact. A consumer that files errors into issues binds its
// own durable on the same subject and reads each message through DecodeFault, so it
// decodes the plane's own wire rather than a second copy of its schema.

// ErrorSubject is the subject every error fact is published on.
var ErrorSubject = signalError.subject()

// Fault is one error fact: the error half of the published message.
type Fault struct {
	Org     string
	ID      string
	Time    time.Time
	Product string
	Env     string
	Service string
	Release string
	URL     string
	Path    string
	Person  string
	Session string
	Trace   string
	Span    string

	// Issue is the grouping key the ingest computed; Class the exception type,
	// Message its scrubbed text and Origin the place of the fault.
	Issue   string
	Class   string
	Message string
	Origin  string
	Handled bool

	// Level is the stored severity read back as its band's word (severityText).
	Level string

	Frames     []FaultFrame
	Attributes map[string]string
}

// FaultFrame is one stack frame of a Fault, crash site last.
type FaultFrame struct {
	Function string
	File     string
	Line     uint32
	Column   uint32
	Own      bool
}

// DecodeFault reads one message off event.error. ok is false for anything that is
// not an error fact — a subscriber envelope shares the subject (EventSignalKey) — and
// for a fact that names no tenant, so a consumer acks either unread.
func DecodeFault(data []byte) (Fault, bool) {
	m, err := decodeMessage(data)
	if err != nil || m.Signal != string(signalError) || m.Org == "" {
		return Fault{}, false
	}
	frames := make([]FaultFrame, len(m.Frames))
	for i, f := range m.Frames {
		frames[i] = FaultFrame{Function: f.Function, File: f.File, Line: f.Line, Column: f.Column, Own: f.Own}
	}
	return Fault{
		Org: m.Org, ID: m.ID, Time: m.Time,
		Product: m.Product, Env: m.Env, Service: m.Service, Release: m.Release,
		URL: m.URL, Path: m.Path, Person: m.Person, Session: m.Session,
		Trace: m.Trace, Span: m.Span,
		Issue: m.Issue, Class: m.Class, Message: m.Message, Origin: m.Origin, Handled: m.Handled,
		Level:      severityText(m.Severity),
		Frames:     frames,
		Attributes: m.Attributes,
	}, true
}
