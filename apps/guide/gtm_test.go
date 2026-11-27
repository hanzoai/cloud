package guide

import (
	"strings"
	"testing"
)

// TestFunnelCountsVisitorsByAnId: a visitor is the id a row names — the person or
// browser, else the pixel's day-scoped id — and a row naming neither is no visitor.
func TestFunnelCountsVisitorsByAnId(t *testing.T) {
	for _, want := range []string{
		"countIf(kind = 'page') AS pageviews",
		"uniqExactIf(if(distinct_id != '', distinct_id, anonymous_id), if(distinct_id != '', distinct_id, anonymous_id) != '') AS visitors",
	} {
		if !strings.Contains(funnelSQL, want) {
			t.Fatalf("funnel must carry %q; got %q", want, funnelSQL)
		}
	}
}
