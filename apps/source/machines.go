// Copyright © 2026 Hanzo AI. MIT License.

package source

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// Machine is the canonical unified machine record served by /v1/machines.
// It represents BOTH a person's linked local machines (desktops, laptops,
// workstations, browsers via HIP-1350) AND the org's cloud compute & BYO
// fleet machines (VMs, GPU nodes, DOKS workers, dialed-in boxes).
type Machine struct {
	// ID is the machine's identifier (`src_...` for linked machines, org-scoped name/id for cloud compute).
	ID string `json:"id"`
	// Kind classifies the machine: `desktop`, `browser`, `cloud_compute`, `gpu`, `byo`, or `bot`.
	Kind string `json:"kind"`
	// Origin distinguishes where the machine lives: `linked` (personal device), `cloud` (managed cloud compute), or `byo` (fleet worker).
	Origin string `json:"origin"`
	// Name is the human-readable label for this machine.
	Name string `json:"name"`
	// OS is the operating system (`darwin`, `linux`, `windows`, etc.).
	OS string `json:"os,omitempty"`
	// Arch is the CPU architecture (`arm64`, `amd64`, etc.).
	Arch string `json:"arch,omitempty"`
	// Version is the reported agent/app version for linked machines.
	Version string `json:"version,omitempty"`
	// State is normalized availability: `online`, `offline`, or `revoked`.
	State string `json:"state"`
	// Status is the raw provider lifecycle status for cloud compute machines (e.g. `running`, `active`, `off`).
	Status string `json:"status,omitempty"`
	// LastSeen is the last accepted heartbeat, RFC 3339 UTC.
	LastSeen string `json:"last_seen,omitempty"`
	// LinkedAt is when the machine was linked or created, RFC 3339 UTC.
	LinkedAt string `json:"linked_at,omitempty"`
	// Zap is the machine's reachable ZAP endpoint (when routable over Hanzo ZAP).
	Zap string `json:"zap,omitempty"`
	// Providers is the reported provider state from linked machines.
	Providers []SourceProvider `json:"providers,omitempty"`
	// Provider is the cloud or fleet provider (`digitalocean`, `aws`, `byo`, `local`, etc.).
	Provider string `json:"provider,omitempty"`
	// Region is the cloud region slug (`sfo3`) or `on-prem` / `local`.
	Region string `json:"region,omitempty"`
	// Size is the cloud instance size slug (`s-2vcpu-4gb`, `gpu-h100x8-640gb`, `byo-gpu`).
	Size string `json:"size,omitempty"`
	// Vcpu is logical CPU cores when known.
	Vcpu *int `json:"vcpu,omitempty"`
	// Mem is system RAM rendered for display (`8 GB`, `128 GB`).
	Mem string `json:"mem,omitempty"`
	// GPU names the accelerator(s) attached to this machine (`H100`, `NVIDIA GB10`).
	GPU string `json:"gpu,omitempty"`
	// PublicIP is the internet-facing IPv4 address for cloud compute machines.
	PublicIP string `json:"public_ip,omitempty"`
	// PrivateIP is the private VPC IPv4 address for cloud compute machines.
	PrivateIP string `json:"private_ip,omitempty"`
	// Agent names the cloud Agent bound to this machine, if any.
	Agent string `json:"agent,omitempty"`
}

// MachineQuery optionally filters GET /v1/machines.
type MachineQuery struct {
	// Kind filters by machine class: `desktop`, `browser`, `cloud`, `cloud_compute`, `gpu`, `byo`, `bot`, or empty for all.
	Kind string `json:"-" url:"kind"`
}

// MachineList is the unified response for GET /v1/machines.
// `machines` carries the full union of linked personal machines + cloud compute / BYO machines.
// `sources` mirrors the linked personal machines for seamless drop-in compatibility.
type MachineList struct {
	Machines []Machine `json:"machines"`
	Sources  []Source  `json:"sources"`
}

// fetchComputeMachines queries apps/compute over the internal plane (best-effort, bounded timeout)
// so a Visor/compute blip never hides a person's linked local machines.
func defaultFetchCompute(ctx context.Context, org, subject string) ([]client.ComputeMachine, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := cloud.Ask[client.Unit, client.ComputeMachinesOut](
		zip.WithCaller(cctx, zip.Caller{Org: org, User: subject}),
		"compute",
		client.ComputeListMachines,
		&client.Unit{},
	)
	if err != nil || out == nil {
		return nil, err
	}
	return out.Machines, nil
}

func sourceToMachine(s Source) Machine {
	return Machine{
		ID:        s.ID,
		Kind:      s.Kind,
		Origin:    "linked",
		Name:      s.Name,
		OS:        s.OS,
		Arch:      s.Arch,
		Version:   s.Version,
		State:     s.State,
		LastSeen:  s.LastSeen,
		LinkedAt:  s.LinkedAt,
		Zap:       s.Zap,
		Providers: s.Providers,
		Provider:  "local",
		Region:    "local",
	}
}

func computeToMachine(cm client.ComputeMachine) Machine {
	origin := "cloud"
	kind := "cloud_compute"
	if strings.EqualFold(cm.Provider, "byo") {
		origin = "byo"
		kind = "byo"
	} else if cm.Agent != "" {
		kind = "bot"
	} else if strings.TrimSpace(cm.GPU) != "" {
		kind = "gpu"
	}

	state := "offline"
	switch strings.ToLower(strings.TrimSpace(cm.Status)) {
	case "online", "running", "active", "ready":
		state = "online"
	}

	name := strings.TrimSpace(cm.Name)
	if name == "" {
		name = cm.ID
	}

	return Machine{
		ID:        cm.ID,
		Kind:      kind,
		Origin:    origin,
		Name:      name,
		OS:        cm.OS,
		State:     state,
		Status:    cm.Status,
		LinkedAt:  cm.CreatedTime,
		Provider:  cm.Provider,
		Region:    cm.Region,
		Size:      cm.Type,
		Vcpu:      cm.Vcpu,
		Mem:       cm.Mem,
		GPU:       cm.GPU,
		PublicIP:  cm.PublicIP,
		PrivateIP: cm.PrivateIP,
		Agent:     cm.Agent,
	}
}

func matchesMachineFilter(m Machine, filter string) bool {
	if filter == "" || filter == "all" {
		return true
	}
	switch filter {
	case "cloud", "cloud_compute", "vm":
		return m.Origin == "cloud" || m.Kind == "cloud_compute" || m.Kind == "gpu" || m.Kind == "bot"
	case "linked", "local":
		return m.Origin == "linked"
	case "gpu":
		return m.Kind == "gpu" || strings.TrimSpace(m.GPU) != ""
	default:
		return strings.EqualFold(m.Kind, filter) || strings.EqualFold(m.Origin, filter)
	}
}

// ListMachines returns EVERY machine available to the caller in one unified view:
// the signed-in person's linked desktops/devices (HIP-1350) PLUS the org's cloud
// compute instances, GPU nodes, and BYO fleet workers.
func (o ops) listMachines(ctx context.Context, in *MachineQuery) (*MachineList, error) {
	sto, org, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := sto.List(ctx, subject)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "machines: %v", err)
	}

	filter := ""
	if in != nil {
		filter = strings.ToLower(strings.TrimSpace(in.Kind))
	}

	sources := make([]Source, 0, len(rows))
	machines := make([]Machine, 0, len(rows))
	seenLinked := make(map[string]struct{}, 2*len(rows))

	for _, r := range rows {
		sv := o.view(r)
		sources = append(sources, sv)
		m := sourceToMachine(sv)
		seenLinked["id:"+strings.ToLower(m.ID)] = struct{}{}
		if m.Name != "" {
			seenLinked["name:"+strings.ToLower(m.Name)] = struct{}{}
		}
		if matchesMachineFilter(m, filter) {
			machines = append(machines, m)
		}
	}

	// Best-effort fold of cloud compute + BYO fleet machines from apps/compute.
	if o.s.State.compute != nil && filter != "desktop" && filter != "browser" && filter != "linked" && filter != "local" {
		if cms, err := o.s.State.compute(ctx, org, subject); err != nil {
			o.s.Log.Debug("machines: compute plane unavailable; returning linked machines only", "org", org, "err", err)
		} else {
			for _, cm := range cms {
				m := computeToMachine(cm)
				// Avoid duplicating a BYO worker that is already present as a linked machine with the same hostname.
				if m.Origin == "byo" {
					if _, dupID := seenLinked["id:"+strings.ToLower(m.ID)]; dupID {
						continue
					}
					if _, dupName := seenLinked["name:"+strings.ToLower(m.Name)]; dupName {
						continue
					}
				}
				if matchesMachineFilter(m, filter) {
					machines = append(machines, m)
				}
			}
		}
	}

	return &MachineList{Machines: machines, Sources: sources}, nil
}

// GetMachine returns one machine by id: checks the caller's linked machines first,
// then falls back to the org's cloud compute & BYO fleet machines.
func (o ops) getMachine(ctx context.Context, in *SourceRef) (*Machine, error) {
	sto, org, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	if r, err := sto.Get(ctx, subject, in.ID); err == nil {
		m := sourceToMachine(o.view(r))
		return &m, nil
	} else if err != errNotFound {
		return nil, zip.Errorf(http.StatusInternalServerError, "machines: %v", err)
	}

	if o.s.State.compute != nil {
		if cms, err := o.s.State.compute(ctx, org, subject); err == nil {
			for _, cm := range cms {
				if cm.ID == in.ID || strings.EqualFold(cm.Name, in.ID) {
					m := computeToMachine(cm)
					return &m, nil
				}
			}
		}
	}
	return nil, zip.ErrNotFound("machine not found")
}
