// Copyright © 2026 Hanzo AI. MIT License.

package flags

import (
	"context"
	"strconv"

	"github.com/hanzoai/cloud/plane"
)

// Int and Bool are what a caller outside the flags binary asks, and they exist so
// that asking is one expression rather than five.
//
// The caller's own compiled value is the FALLBACK and travels with the question:
// a flag's definition is declared where it is used, so the app that owns the flag
// store cannot be assumed to hold a Def for every key, while what an operator SET
// is precisely what the caller cannot know for itself.
//
// AN UNREACHABLE FLAG PLANE YIELDS THE FALLBACK, never a zero. That is not
// leniency, it is the behaviour these call sites already had — a deployment with
// no flags plugin ran on compiled defaults — so converting a read cannot change
// what a fleet without that plugin does. What it changes is the fleet WITH one,
// where the answer used to be the default too.

// Int resolves an integer flag, falling back to def.
func Int(ctx context.Context, key string, def int) int {
	v, err := FlagsValue(ctx, &plane.FlagValueIn{
		Key: key, Kind: "int", Default: strconv.Itoa(def),
	})
	if err != nil || v == nil || !v.Set {
		return def
	}
	return v.N
}

// Bool resolves a boolean flag, falling back to def.
func Bool(ctx context.Context, key string, def bool) bool {
	v, err := FlagsValue(ctx, &plane.FlagValueIn{
		Key: key, Kind: "bool", Default: strconv.FormatBool(def),
	})
	if err != nil || v == nil || !v.Set {
		return def
	}
	return v.On
}
