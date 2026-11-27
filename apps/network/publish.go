// publish.go — the service publisher: how a NAME goes onto the org's overlay.
//
// A published service is one request and five fabric objects, and this request
// is the only place their names, configs and roles are decided:
//
//	config  "<name>.<org>-host"       host.v1      where the HOSTING identity forwards: tcp, host, port
//	config  "<name>.<org>-intercept"  intercept.v1 the DNS the fabric answers: "<name>.<org>.zt"
//	service "<name>.<org>"            tagged "org-<org>", end-to-end encryption required
//	policy  "<name>.<org>-bind"       identities with role "<name>-host.<org>" may HOST it
//	policy  "<name>.<org>-dial"       identities with role "org-<org>" may DIAL it — any of the
//	                                  org's devices — and the cloud's own identity ("org-admin",
//	                                  the reserved platform org) so the fleet can reach a
//	                                  published apiserver (apps/fleet/zt.go)
//
// The two config types are the controller's own built-ins, addressed by the
// fixed ids it creates them under (controller/db/migration_initialize.go:
// hostV1ConfigTypeId, interceptV1ConfigType).

package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/sdk"
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
	// the service as "<name>.<org>" and answers for it at "<name>.<org>.zt".
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
// role, dialable at "<name>.<org>.zt" by any of the org's identities — and by
// the cloud's own, which is what lets a BYO cluster's apiserver be attached to
// the fleet with a ".zt" kubeconfig.
//
// Answers 201 with the service and its DNS name. The objects behind it are
// created in dependency order and unwound on failure, so a half-published
// service never lingers on the fabric. Publishing is a steward's act — an
// admin of the org or its own machine client — and a plain member is 403.
//
// A write, so it does not degrade: an unconfigured deployment answers 503.
func (o ops) publishService(ctx context.Context, in *serviceIn) (*publishedView, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	if !steward(ctx) {
		return nil, errSteward("publishing a service")
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
		for _, m := range slices.Backward(made) {
			_, _ = s.State.cl.call(ctx, http.MethodDelete, m, "", nil)
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

// own makes sure, once per process, that the cloud's own identity carries the
// reserved org's role — the one every dial policy above admits beside the
// owning org, which is how the fleet reaches a published apiserver. It is the
// identities endpoint's own ensure, keyed on the cloud's IAM subject, and runs
// ahead of the first call the cloud makes on its session. A failure is logged
// and tried again on a later call; nothing waits on it.
func (cl *client) own(ctx context.Context) {
	if !cl.owned.CompareAndSwap(false, true) {
		return
	}
	var err error
	if t, terr := cl.iam.Token(); terr != nil {
		err = terr
	} else {
		var id cloud.VerifiedIdentity
		if id, err = cloud.NewTokenValidator(cloud.DiscoveryURL(), nil).Validate(t.AccessToken); err == nil && id.Subject == "" {
			err = fmt.Errorf("the machine token names no subject")
		}
		if err == nil {
			_, err = cl.ensure(ctx, id.Subject, id.Subject, []string{orgRole(authz.AdminOrg)})
		}
	}
	if err != nil {
		cl.owned.Store(false)
		cl.log.Warn("zt: the cloud's own identity may lack its dial role", "role", orgRole(authz.AdminOrg), "err", err)
	}
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

// serviceRef addresses one published service by the id the list returns.
type serviceRef struct {
	// ID is the service id from the path. The URL is the addressing authority,
	// so it binds from there whatever else the request carries.
	ID string `json:"id"`
}

// unpublishService takes a name off the org's overlay: the two policies, the
// service and its two configs that publishing made, in that order, and the
// "<name>-host" role from each of the org's identities — that role exists
// only for the bind policy to select, and a role naming no service is one
// the identities endpoint refuses to write.
//
// Only objects carrying this service's own names are deleted, so a policy
// that selects the service by attribute is left alone. An id belonging to
// another org — or to nothing — is 404 before any write, as for an identity.
// Like publishing it is a steward's act, and a plain member is 403 before the
// controller is read.
func (o ops) unpublishService(ctx context.Context, in *serviceRef) (*cloud.Unit, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	if !steward(ctx) {
		return nil, errSteward("taking a service off the network")
	}
	cl := s.State.cl
	all, err := listAll[ztService](cl, ctx, "/services")
	if err != nil {
		return nil, err
	}
	mine := filterServices(all, org)
	i := slices.IndexFunc(mine, func(z ztService) bool { return z.ID == strings.TrimSpace(in.ID) })
	if i < 0 {
		return nil, zip.ErrNotFound("service not found")
	}
	sv := mine[i]
	base := "/services/" + url.PathEscape(sv.ID)

	named := func(sub string, names ...string) ([]string, error) {
		objs, err := listAll[ztNamed](cl, ctx, base+sub)
		if err != nil {
			return nil, err
		}
		var paths []string
		for _, n := range objs {
			if slices.Contains(names, n.Name) {
				paths = append(paths, sub+"/"+url.PathEscape(n.ID))
			}
		}
		return paths, nil
	}
	pols, err := named("/service-policies", sv.Name+"-bind", sv.Name+"-dial")
	if err != nil {
		return nil, err
	}
	cfgs, err := named("/configs", sv.Name+"-host", sv.Name+"-intercept")
	if err != nil {
		return nil, err
	}
	// Configs go last: the controller refuses to delete one a service still uses.
	for _, p := range slices.Concat(pols, []string{base}, cfgs) {
		if _, err := cl.call(ctx, http.MethodDelete, p, "", nil); err != nil {
			return nil, err
		}
	}

	role := scoped(short(sv.Name, org)+"-host", org)
	ids, err := listAll[ztIdentity](cl, ctx, "/identities")
	if err != nil {
		return nil, err
	}
	for _, id := range filterIdentities(ids, org) {
		if !hasRole(id.RoleAttributes, role) {
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(id.RoleAttributes), func(r string) bool { return r == role })
		if _, err := cl.call(ctx, http.MethodPatch, "/identities/"+url.PathEscape(id.ID), "", map[string]any{"roleAttributes": kept}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
