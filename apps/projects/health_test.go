package projects

import (
	"context"
	"net/http"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sites"
)

// serviceWithEdge builds the smallest service this report needs: health reads
// the edge and nothing else, so it gets an edge and nothing else.
func serviceWithEdge(t *testing.T, e sites.Edge) *cloud.Service[state] {
	t.Helper()
	return &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.New("test")},
		State: state{apex: "hanzo.app", edge: e},
	}
}

// configuredEdge is an edge that holds credentials. It exists because the real
// one reads the environment, and a test that sets CF_API_TOKEN to assert on
// health would be testing the environment rather than the report.
type configuredEdge struct{ sites.NoEdge }

func (configuredEdge) Configured() bool { return true }

// The whole point of this endpoint is that the degraded case is REPORTED rather
// than inferred from a CDN response, so both states are pinned: an operator has
// to be able to tell "a publish is live now" from "a publish is live in four
// hours" without leaving the API.
func TestHealthReportsWhetherAPublishReachesReaders(t *testing.T) {
	for _, tc := range []struct {
		name       string
		edge       sites.Edge
		wantStatus string
		wantCode   int
		wantErr    bool
	}{
		{"no credentials", sites.NoEdge{}, "degraded", http.StatusServiceUnavailable, true},
		{"credentials", configuredEdge{}, "ok", http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := ops{s: serviceWithEdge(t, tc.edge)}
			got, err := o.health(context.Background(), nil)
			if err != nil {
				t.Fatalf("health: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.StatusCode() != tc.wantCode {
				t.Errorf("code = %d, want %d", got.StatusCode(), tc.wantCode)
			}
			if (got.Error != "") != tc.wantErr {
				t.Errorf("error = %q, want present=%v", got.Error, tc.wantErr)
			}
			// Freshness is the field an operator reads, so it is never blank in
			// either state — a health report that says "degraded" and nothing
			// else sends someone to the source to find out what that means.
			if got.Freshness == "" {
				t.Error("freshness is empty; that is the sentence this endpoint exists to say")
			}
		})
	}
}
