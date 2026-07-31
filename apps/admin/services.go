package admin

// The /v1/admin/services board — the launch-control LENS over the waitlist gate, twin
// of /v1/admin/flags. Every hosted service (studio/chat/console/app/api/team + runtime
// onboards) with its LIVE waitlist mode — the switch waitlist.<svc>, evaluated through
// clients/admission (which composes the flag engine one-way). This is the "remove the
// waitlist one service at a time" toggle. SuperAdmin only (core.Admit), like every
// platform /v1/admin/*.
//
// The registry + mode decide + these admin control funcs live in clients/admission,
// the complete launch-gate feature; flags is the pure engine underneath. Per-user
// approval (the second, orthogonal axis) stays IAM's, reached via the existing admin IAM
// proxy — not re-served here.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admission"
	"github.com/zap-proto/zip"
)

// services reads the launch board. It lists every hosted service in the registry with
// its LIVE waitlist mode, evaluated through the flag engine. This is the "remove the
// waitlist one service at a time" view.
//
// Response: {"status":"ok","msg":"","data":{"services":[{"service":"chat",
// "displayName":"Chat","description":"","hosts":["chat.hanzo.ai"],"waitlistMode":true}]}}
func services(ctx context.Context, _ *core.None) (*servicesOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	rows, err := admission.ListWaitlistServices(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list services: %v", err)
	}
	return &servicesOut{Status: core.OK, Data: &serviceList{Services: rows}}, nil
}

// serviceList is the launch board payload.
type serviceList struct {
	// Services is every registered service with its live waitlist mode.
	Services []admission.ServiceView `json:"services"`
}

// servicesOut is the GET /v1/admin/services envelope.
type servicesOut struct {
	Status string       `json:"status"`
	Msg    string       `json:"msg"`
	Data   *serviceList `json:"data"`
}

// serviceOne is the payload of the two write ops: the ONE service they touched.
type serviceOne struct {
	// Service is the row as it stands after the write, live mode included.
	Service admission.ServiceView `json:"service"`
}

// serviceOut is the envelope of the two service write ops.
type serviceOut struct {
	Status string      `json:"status"`
	Msg    string      `json:"msg"`
	Data   *serviceOne `json:"data"`
}

// serviceModeIn is the POST /v1/admin/services/:service/mode input.
type serviceModeIn struct {
	// Service is the slug to flip, taken from the path.
	Service string `json:"service"`
	// WaitlistMode is the new mode: true gates the service behind the waitlist, false
	// opens it. This is the launch lever.
	WaitlistMode bool `json:"waitlistMode"`
}

// upsertService onboards a hosted service, or edits one. A new host comes under the
// launch gate WITHOUT a redeploy. Re-registering an existing service PRESERVES its live
// switch — editing the hosts of a service that is already open must not silently close
// it again.
//
// Example: {"service":"chat","displayName":"Chat","description":"Hanzo Chat",
// "hosts":["chat.hanzo.ai"],"waitlistMode":true}
// Response: {"status":"ok","msg":"","data":{"service":{"service":"chat","displayName":"Chat",
// "description":"Hanzo Chat","hosts":["chat.hanzo.ai"],"waitlistMode":true}}}
func upsertService(ctx context.Context, in *admission.ServiceInput) (*serviceOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Service) == "" {
		return nil, zip.ErrBadRequest("service slug is required")
	}
	view, err := admission.UpsertWaitlistService(ctx, *in, c.UserEmail())
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	return &serviceOut{Status: core.OK, Data: &serviceOne{Service: view}}, nil
}

// setServiceMode flips ONE service's waitlist switch — the launch lever. Hot: it takes
// effect on this pod immediately and on peers within one evaluation TTL, with no
// redeploy. An unknown service is a 404, not a silent create; onboarding goes through
// upsertService.
//
// Example: {"waitlistMode":false}
// Response: {"status":"ok","msg":"","data":{"service":{"service":"chat","displayName":"Chat",
// "description":"Hanzo Chat","hosts":["chat.hanzo.ai"],"waitlistMode":false}}}
func setServiceMode(ctx context.Context, in *serviceModeIn) (*serviceOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	service := strings.TrimSpace(in.Service)
	if service == "" {
		return nil, zip.ErrBadRequest("service is required")
	}
	view, err := admission.SetWaitlistMode(ctx, service, in.WaitlistMode, c.UserEmail())
	if err != nil {
		if errors.Is(err, admission.ErrServiceNotFound) {
			return nil, zip.ErrNotFound("service not found: " + service)
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "set mode: %v", err)
	}
	return &serviceOut{Status: core.OK, Data: &serviceOne{Service: view}}, nil
}
