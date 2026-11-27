package provider

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/client"
	triagepeer "github.com/hanzoai/cloud/client/triage"
	"github.com/hanzoai/cloud/sdk"
)

// triageBudget bounds telling triage one delivery.
const triageBudget = 90 * time.Second

// triaged are the deliveries that can be about a run's work: an issue handed to a
// run, a pull request, and what checks say of its head.
var triaged = map[string]bool{"issues": true, "pull_request": true, "status": true, "check_suite": true}

// triageFn tells triage one delivery, a variable so a test can hear it.
var triageFn = triagepeer.TriageGitHub

// tellTriage hands a verified delivery to triage as the org its installation
// belongs to. The delivery's own work does not wait on it, and a triage that does
// not answer costs that delivery and says so in the log, never GitHub a retry.
func tellTriage(s *cloud.Service[state], event, delivery string, body []byte) {
	if !triaged[event] {
		return
	}
	var ev struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if json.Unmarshal(body, &ev) != nil {
		return
	}
	org, ok := OrgForExternalID("github", strconv.FormatInt(ev.Installation.ID, 10))
	if !ok {
		return
	}
	in := &client.GitHubDelivery{Event: event, Delivery: delivery, Body: json.RawMessage(append([]byte(nil), body...))}
	go func() {
		ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), triageBudget)
		defer cancel()
		if _, err := triageFn(ctx, in); err != nil {
			s.Log.Warn("github: a delivery was not told to triage", "event", event, "delivery", delivery, "err", err)
		}
	}()
}
