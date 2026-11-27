// Copyright © 2026 Hanzo AI. MIT License.

package limits

import (
	"testing"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// A class saved unbounded (flags.Unbounded, -1) is included without limit: the
// plan pays however much was used, never credits, and the class reads unbounded
// rather than a share of a figure. 0 still includes none. Mutation: drop the
// Unbounded case in termsOf and -1 clamps to 0, so the first premium call is
// refused.
func TestAnUnboundedClassIsNeverUsedUp(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "unlimited", 1, "external:comp")
		w.l.flag = policy(saved(nil, "unlimited", 0, 0, flags.Unbounded, flags.Unbounded))
		const million = int64(1_000_000) * 1_000_000_000 // $1,000,000 in nano-dollars
		for i := range 3 {
			g, hit := w.priced(premium, ClassPremium)
			if hit != nil || g == nil || g.Pays != PaysPlan {
				t.Fatalf("call %d: grant %+v hit %+v, want the plan to pay", i, g, hit)
			}
			w.use(g, million)
		}
		s := w.read()
		for _, class := range []string{ClassPremium, ClassOurs} {
			c := s.Classes[class]
			if !c.Unbounded || c.Paying != PaysPlan || c.Period.Limit != 0 || c.Period.Percent() != 0 {
				t.Fatalf("%s reads %+v, want unbounded, paid by the plan, no limit", class, c)
			}
		}
		if used := s.Classes[ClassPremium].Period.Used; used < 3*million {
			t.Fatalf("premium used %d, want every call counted", used)
		}

		none := newWorld(t, "unlimited", 1, "external:comp")
		none.l.flag = policy(saved(nil, "unlimited", 0, 0, 0, 0))
		if g, hit := none.priced(premium, ClassPremium); g != nil && g.Pays == PaysPlan || hit == nil {
			t.Fatalf("a plan including none: grant %+v hit %+v, want it refused", g, hit)
		}
	})
}
