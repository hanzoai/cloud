package admin

// The /v1/admin/services board — the launch-control LENS on the ONE flag engine, twin
// of /v1/admin/flags. Every hosted service (studio/chat/console/app/api/team + runtime
// onboards) with its LIVE waitlist mode — the switch waitlist.<svc> evaluated through
// clients/flags. This is the "remove the waitlist one service at a time" toggle.
// SuperAdmin only (core.Guard), like every platform /v1/admin/*.
//
// Formerly clients/featuregate owned its OWN SQLite mode store + this control plane;
// both folded onto the flag engine so the platform has ONE decision plane. featuregate
// now owns only the native Enforce middleware — a consumer of flags.WaitlistModeForHost.
// Per-user approval (the second, orthogonal axis) stays IAM's, reached via the existing
// admin IAM proxy — not re-served here.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/flags"
	"github.com/zap-proto/zip"
)

// services answers GET /v1/admin/services — the launch board (every service + live mode).
func services(s *cloud.Service[core.State], c *zip.Ctx) error {
	rows, err := flags.ListWaitlistServices(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list services: %v", err)
	}
	return core.OK(c, map[string]any{"services": rows})
}

// upsertService answers POST /v1/admin/services — onboard or edit a hosted service so a
// new host is governed WITHOUT a redeploy. A re-register PRESERVES the live switch.
func upsertService(s *cloud.Service[core.State], c *zip.Ctx) error {
	var in flags.ServiceInput
	if err := c.Bind(&in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Service) == "" {
		return zip.ErrBadRequest("service slug is required")
	}
	view, err := flags.UpsertWaitlistService(c.Context(), in, c.UserEmail())
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
	view, err := flags.SetWaitlistMode(c.Context(), service, body.WaitlistMode, c.UserEmail())
	if err != nil {
		if errors.Is(err, flags.ErrServiceNotFound) {
			return zip.ErrNotFound("service not found: " + service)
		}
		return zip.Errorf(http.StatusInternalServerError, "set mode: %v", err)
	}
	return core.OK(c, map[string]any{"service": view})
}
