// Copyright © 2026 Hanzo AI. MIT License.

package flags

import (
	"context"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// What an operator has set one flag to, published on the internal plane.
//
// [Bool], [Int] and [String] answer from `mounted`, a package global this app's own
// Use fills — so they are correct in the flags binary and, in every other binary,
// silently return the caller's compiled default. Measured: a process that links
// this package and does not mount it reads `3` for a cap window an operator set to
// anything at all. Four apps read flags that way — the AI spend ceiling, the free
// allowance, affiliate commission rates, and admission's waitlist switch, where a
// false reads as "this service is not gated".
//
// So the question crosses a process boundary, which is what the plane is for.

// exposeValue publishes the flag read. Use calls it.
func exposeValue() {
	zip.Post[plane.FlagValueIn, plane.FlagValue](cloud.Plane(), "/flags/value",
		value,
		zip.WithOperationID(plane.FlagsValue),
		zip.WithSummary("What an operator has set one flag to"))
}

// value resolves one flag for the CALLER'S OWN org.
//
// The caller supplies its own Default, and that is what makes this op possible
// without a catalog: a Def is declared where its flag is used — in a loop over
// tiers, or per service registered at run time — so this app cannot be assumed to
// hold one for every key. What it holds is the operator's setting, which is the
// half the caller genuinely cannot know.
//
// Parsing happens HERE, by the same [Client.resolve] that serves /v1/flag, so a
// stored value means one thing however it is asked for. A caller that parsed the
// text itself would be a second parser, and the two would disagree the first time
// somebody wrote "yes" instead of "true".
func value(ctx context.Context, in *plane.FlagValueIn) (*plane.FlagValue, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("value: no org on the call")
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return nil, zip.ErrBadRequest("value: a flag key is required")
	}
	def := Def{Key: key, Default: in.Default, Type: kindOf(in.Kind)}
	text, source := mounted.resolve(def)
	out := &plane.FlagValue{Value: text, Set: source == "flags"}
	out.N, _ = strconv.Atoi(strings.TrimSpace(text))
	out.On, _ = strconv.ParseBool(strings.TrimSpace(text))
	return out, nil
}

// kindOf reads the wire's type name. An unknown one is a string, which is what a
// value nobody has typed already is.
func kindOf(s string) Type {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "bool":
		return TypeBool
	case "int":
		return TypeInt
	default:
		return TypeString
	}
}
