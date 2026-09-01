// publish.go — the service publisher: how a NAME goes onto the org's overlay.
//
// A published service is one request and five fabric objects, and this request
// is the only place their names, configs and roles are decided:
//
//	config  "<name>.<org>-host"       host.v1      where the HOSTING identity forwards: tcp, host, port
//	config  "<name>.<org>-intercept"  intercept.v1 the DNS the fabric answers: "<name>.<org>.ziti"
//	service "<name>.<org>"            tagged "org-<org>", end-to-end encryption required
//	policy  "<name>.<org>-bind"       identities with role "<name>-host.<org>" may HOST it
//	policy  "<name>.<org>-dial"       identities with role "org-<org>" may DIAL it — any of the
//	                                  org's devices — and the cloud's own identity ("org-admin",
//	                                  the reserved platform org) so the fleet can reach a
//	                                  published apiserver (apps/fleet/ziti.go)
//
// The two config types are the controller's own built-ins, addressed by the
// fixed ids it creates them under (controller/db/migration_initialize.go:
// hostV1ConfigTypeId, interceptV1ConfigType).
package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// The built-in tunneler config types, by the fixed ids the controller creates
// them under at first boot.
const (
	hostV1Type      = "NH5p4FpGR" // host.v1
	interceptV1Type = "g7cIWbcGg" // intercept.v1
)

// serviceIn is what POST /v1/network/services takes.
type serviceIn struct {
	// Name is the service's name within the org — a DNS label. The fabric knows
	// the service as "<name>.<org>" and answers for it at "<name>.<org>.ziti".
	Name string `json:"name"`
	// Host is where the HOSTING identity forwards a connection — an address the
	// host device itself can reach, "127.0.0.1" for a server on the device.
	Host string `json:"host"`
	// Port is the port beside Host, and the one the DNS name intercepts.
	Port int `json:"port"`
}

// publishedView is the service as published. (Not "serviceView": the admin app
// claims that name up to case, and a generator that PascalCases both reads one
// class.)
type publishedView struct {
	// ID is the fabric service's id.
	ID string `json:"id"`
	// Name is the service's name within the org.
	Name string `json:"name"`
	// DNS is the name the fabric answers for this service — what a kubeconfig
	// server, or any client on the org's overlay, dials.
	DNS string `json:"dns"`
}

// publishService puts a name on the org's overlay: a fabric service forwarding
// to host:port on whichever of the org's devices carries the "<name>-host"
// role, dialable at "<name>.<org>.ziti" by any of the org's identities — and by
// the cloud's own, which is what lets a BYO cluster's apiserver be attached to
// the fleet with a ".ziti" kubeconfig.
//
// Answers 201 with the service and its DNS name. The objects behind it are
// created in dependency order and unwound on failure, so a half-published
// service never lingers on the fabric.
//
// A write, so it does not degrade: an unconfigured deployment answers 503.
func (o ops) publishService(ctx context.Context, in *serviceIn) (*publishedView, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	name, err := label(in.Name)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	host := strings.TrimSpace(in.Host)
	if host == "" {
		return nil, zip.ErrBadRequest("host required")
	}
	if in.Port < 1 || in.Port > 65535 {
		return nil, zip.ErrBadRequest("port must be 1-65535")
	}

	fqn := scoped(name, org)
	dns := fqn + ztDNSSuffix

	// Everything made so far, newest first, so a failure part-way unwinds to
	// nothing rather than leaving a service with half its policies.
	var made []string
	undo := func() {
		for i := len(made) - 1; i >= 0; i-- {
			_, _ = s.State.cl.call(ctx, http.MethodDelete, made[i], "", nil)
		}
	}
	create := func(path string, body map[string]any) (string, error) {
		raw, err := s.State.cl.call(ctx, http.MethodPost, path, "", body)
		if err != nil {
			return "", err
		}
		var c ztCreated
		if err := json.Unmarshal(raw, &c); err != nil || c.Data.ID == "" {
			return "", zip.Errorf(http.StatusBadGateway, "zt: create %s returned no id", path)
		}
		made = append(made, path+"/"+url.PathEscape(c.Data.ID))
		return c.Data.ID, nil
	}

	hostCfg, err := create("/configs", map[string]any{
		"name": fqn + "-host", "configTypeId": hostV1Type,
		"data": map[string]any{"protocol": "tcp", "address": host, "port": in.Port},
	})
	if err != nil {
		return nil, err
	}
	interceptCfg, err := create("/configs", map[string]any{
		"name": fqn + "-intercept", "configTypeId": interceptV1Type,
		"data": map[string]any{
			"protocols":  []string{"tcp"},
			"addresses":  []string{dns},
			"portRanges": []map[string]int{{"low": in.Port, "high": in.Port}},
		},
	})
	if err != nil {
		undo()
		return nil, err
	}
	svcID, err := create("/services", map[string]any{
		"name":               fqn,
		"encryptionRequired": true,
		"configs":            []string{hostCfg, interceptCfg},
		"roleAttributes":     []string{orgRole(org)},
	})
	if err != nil {
		undo()
		return nil, err
	}
	if _, err := create("/service-policies", map[string]any{
		"name": fqn + "-bind", "type": "Bind", "semantic": "AnyOf",
		"serviceRoles":  []string{"@" + svcID},
		"identityRoles": []string{"#" + scoped(name+"-host", org)},
	}); err != nil {
		undo()
		return nil, err
	}
	if _, err := create("/service-policies", map[string]any{
		"name": fqn + "-dial", "type": "Dial", "semantic": "AnyOf",
		"serviceRoles":  []string{"@" + svcID},
		"identityRoles": []string{"#" + orgRole(org), "#" + orgRole(authz.AdminOrg)},
	}); err != nil {
		undo()
		return nil, err
	}

	return &publishedView{ID: svcID, Name: name, DNS: dns}, nil
}

// orgService refuses a "<service>-host" role whose service the org does not
// own: the attribute exists to be selected by that service's bind policy, so a
// role naming nothing would enroll a device that can host nothing — and nobody
// would learn why until it tried.
func (o ops) orgService(ctx context.Context, org, name string) error {
	all, err := listAll[ztService](o.s.State.cl, ctx, "/services")
	if err != nil {
		return err
	}
	for _, sv := range filterServices(all, org) {
		if short(sv.Name, org) == name {
			return nil
		}
	}
	return zip.ErrBadRequest("role " + name + "-host names no service of the org's — publish it first (POST /v1/network/services)")
}
