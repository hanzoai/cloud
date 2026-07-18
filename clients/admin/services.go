package admin

// The /v1/admin/services board — the launch-control LENS over the waitlist gate, twin
// of /v1/admin/flags. Every hosted service (studio/chat/console/app/api/team + runtime
// onboards) with its LIVE waitlist mode — the switch waitlist.<svc>, evaluated through
// clients/admission (which composes the flag engine one-way). This is the "remove the
// waitlist one service at a time" toggle. SuperAdmin only (core.Guard), like every
// platform /v1/admin/*.
//
// The registry + mode decide + these admin control funcs live in clients/admission,
// the complete launch-gate feature; flags is the pure engine underneath. Per-user
// approval (the second, orthogonal axis) stays IAM's, reached via the existing admin IAM
// proxy — not re-served here.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admission"
	"github.com/zap-proto/zip"
)

// services answers GET /v1/admin/services — the launch board (every service + live mode).
func services(s *cloud.Service[core.State], c *zip.Ctx) error {
	rows, err := admission.ListWaitlistServices(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list services: %v", err)
	}
	return core.OK(c, map[string]any{"services": rows})
}

// upsertService answers POST /v1/admin/services — onboard or edit a hosted service so a
// new host is governed WITHOUT a redeploy. A re-register PRESERVES the live switch.
func upsertService(s *cloud.Service[core.State], c *zip.Ctx) error {
	var in admission.ServiceInput
	if err := c.Bind(&in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Service) == "" {
		return zip.ErrBadRequest("service slug is required")
	}
	view, err := admission.UpsertWaitlistService(c.Context(), in, c.UserEmail())
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	return core.OK(c, map[string]any{"service": view})
}

// setServiceMode answers POST /v1/admin/services/:service/mode — flip one service's
// waitlist switch {waitlistMode:bool}. The launch lever; hot, no redeploy.
func setServiceMode(s *cloud.Service[core.State], c *zip.Ctx) error {
	service := strings.TrimSpace(c.Param("service"))
	if service == "" {
		return zip.ErrBadRequest("service is required")
	}
	var body struct {
		WaitlistMode bool `json:"waitlistMode"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	view, err := admission.SetWaitlistMode(c.Context(), service, body.WaitlistMode, c.UserEmail())
	if err != nil {
		if errors.Is(err, admission.ErrServiceNotFound) {
			return zip.ErrNotFound("service not found: " + service)
		}
		return zip.Errorf(http.StatusInternalServerError, "set mode: %v", err)
	}
	return core.OK(c, map[string]any{"service": view})
}
