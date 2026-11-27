package studio

// gpus.go — GET /v1/studio/gpus: the GPUs a studio.render step can run on.
//
// The fleet is the workflow app's: every machine `hanzo link` registered writes a
// presence record (namespace fleet, one per machine) and heartbeats it while it
// runs — the same records the compute fleet board (/v1/compute/fleet) reads. A GPU
// is online while its machine's record is live and fresh, and it can take a
// studio.render step only while it advertises that capability.

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/workflow"
	"github.com/hanzoai/cloud/sdk"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
)

const (
	fleetNS = "fleet"
	// liveWindow is how fresh a machine's heartbeat must be to count as online,
	// the fleet board's rule.
	liveWindow = 90 * time.Second
)

// presence is the part of a machine's registration the board reads.
type presence struct {
	Hostname string `json:"hostname"`
	GPUs     []struct {
		Name        string `json:"name"`
		MemoryTotal string `json:"memoryTotal"`
	} `json:"gpus"`
	Capabilities []string `json:"capabilities"`
}

// fleet reads the org's machines, every page of them.
type fleet func(ctx context.Context, org string) ([]tasks.StandaloneActivity, error)

func planeFleet(ctx context.Context, org string) ([]tasks.StandaloneActivity, error) {
	var all []tasks.StandaloneActivity
	cursor := ""
	for range 20 {
		page, err := workflow.WorkflowActivities(cloud.For(ctx, org), &client.ActivitiesIn{Namespace: fleetNS, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		var rows []tasks.StandaloneActivity
		if err := json.Unmarshal(page.Rows, &rows); err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if page.Next == "" || len(rows) == 0 {
			break
		}
		cursor = page.Next
	}
	return all, nil
}

var mib = regexp.MustCompile(`(?i)^\s*([0-9.]+)\s*(mib|mb|gib|gb)?\s*$`)

// Lists the GPUs your org has linked with `hanzo link` that can take a
// studio.render step, each with whether it is free now. With none online, a
// studio.render step waits in the queue.
func (o ops) gpus(ctx context.Context, _ *pageIn) (*GpuList, error) {
	ctx, _, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.fleet(ctx, orgOf(ctx))
	if err != nil {
		return nil, err
	}
	out := &GpuList{Data: []Gpu{}}
	now := time.Now()
	for _, a := range rows {
		b, _ := json.Marshal(a.Input)
		var reg presence
		if json.Unmarshal(b, &reg) != nil || !slices.Contains(reg.Capabilities, gpuType) {
			continue
		}
		status := "idle"
		switch a.Status {
		case jobCompleted, jobFailed, jobCanceled:
			status = "offline"
		}
		beat, err := time.Parse(time.RFC3339, a.LastHeartbeatTime)
		if err != nil || now.Sub(beat) > liveWindow {
			status = "offline"
		}
		for i, g := range reg.GPUs {
			v := Gpu{
				ID: a.Execution.WorkflowId + ":" + strconv.Itoa(i), Name: g.Name, Machine: or(reg.Hostname, a.Execution.WorkflowId),
				Owner: "org", Status: status, Seen: a.LastHeartbeatTime,
			}
			if m := mib.FindStringSubmatch(g.MemoryTotal); m != nil {
				f, _ := strconv.ParseFloat(m[1], 64)
				if strings.HasPrefix(strings.ToLower(m[2]), "g") {
					f *= 1024
				}
				v.VramMB = int64(f)
			}
			out.Data = append(out.Data, v)
		}
	}
	return out, nil
}
