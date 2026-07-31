package admin

// The PLATFORM CONTROL PLANE board (/v1/admin/flags) — every runtime LAUNCH / RELEASE
// switch (waitlist, public signup, subsystem activation, gateway limits, network ids)
// with its LIVE value, evaluated through the embedded native flag engine
// (apps/flags — SQLite-per-project definitions, in-process pure-Go evaluation).
// SuperAdmin only (core.Admit, like every /v1/admin/*).
//
// ONE flag engine, TWO verbs. GET reads the board; PUT writes a switch's definition
// through flags.SetPlatformSwitch — the ONE write path, audited in the store's
// activity log. A flip is hot: this pod applies immediately, peers converge within one
// evaluation TTL (default 15s), no redeploy. Org/project product flags are managed on
// /v1/flags (org-scoped); this surface is the platform's own switchboard.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/zap-proto/zip"
)

// flagsBoard reads the platform control-plane board: every runtime launch/release
// switch (waitlist, public signup, subsystem activation, gateway limits, network ids)
// with its LIVE value and where that value came from — a stored definition or the
// compiled-in default.
//
// Response: {"status":"ok","msg":"","data":{"switches":[{"key":"waitlist.chat",
// "category":"launch","label":"Chat waitlist","description":"Gate chat behind the waitlist",
// "value":true,"source":"default"}]}}
func flagsBoard(ctx context.Context, _ *core.None) (*flagsOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	board := flags.Board()
	return &flagsOut{Status: core.OK, Data: &board}, nil
}

// flagsOut is the envelope of both flag ops: the read board, and the board as it stands
// AFTER a write — so a caller sees the effect of its own flip without a second read.
type flagsOut struct {
	Status string           `json:"status"`
	Msg    string           `json:"msg"`
	Data   *flags.BoardView `json:"data"`
}

// setFlagIn is the PUT /v1/admin/flags/:key input: the switch from the path, its
// definition from the body.
type setFlagIn struct {
	// Key is the switch to write, taken from the path (e.g. "waitlist.chat").
	Key string `json:"key"`
	// Active is the switch itself: true enables the flag for every evaluation.
	Active bool `json:"active"`
	// Filters is the optional rollout/payload block of a VALUED switch, e.g.
	// {"groups":[{"properties":[],"rollout_percentage":100}],"payloads":{"true":250}}.
	Filters json.RawMessage `json:"filters,omitempty"`
}

// setFlag stores or overwrites ONE platform switch's definition. It answers with the
// whole board as it now stands. The flip is hot: this pod applies it immediately and
// peers converge within one evaluation TTL (15s by default), with no redeploy.
//
// The body reaches the flag engine BYTE-FOR-BYTE — it is the engine's definition
// format, not this layer's, so a field the engine understands and admin does not must
// still arrive intact. setFlagIn names the two fields that matter for documentation; it
// is not a filter.
//
// The write is recorded in the store's activity log against the caller's email.
//
// Example: {"active":true,"filters":{"groups":[{"properties":[],"rollout_percentage":100}]}}
// Response: {"status":"ok","msg":"","data":{"switches":[{"key":"waitlist.chat",
// "category":"launch","label":"Chat waitlist","description":"Gate chat behind the waitlist",
// "value":true,"source":"stored"}]}}
func setFlag(ctx context.Context, in *setFlagIn) (*flagsOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return nil, zip.ErrBadRequest("key is required")
	}
	body := c.Body()
	if len(body) == 0 || !json.Valid(body) {
		return nil, zip.ErrBadRequest("body must be the flag definition JSON")
	}
	if err := flags.SetPlatformSwitch(key, json.RawMessage(body), c.UserEmail()); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	board := flags.Board()
	return &flagsOut{Status: core.OK, Data: &board}, nil
}
