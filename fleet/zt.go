// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// zt.go — how the fleet reaches an apiserver that lives on the Hanzo Zero
// Trust fabric instead of on the public internet.
//
// A BYO cluster on a laptop has no routable endpoint. Its owner publishes the
// apiserver as a fabric service (POST /v1/network/services) and registers a
// kubeconfig whose server names that service's DNS — a host ending in ".zt".
// Such a host is not DNS: nothing resolves it and nothing needs to, so guardHost
// accepts it without a lookup and SafeRESTConfig gives the connection this
// dialer instead of TCP. The service name is the host with the ".zt" suffix
// stripped — "k3s.acme.zt" dials the fabric service "k3s.acme" — which is
// exactly the name the network surface wrote when it published the service. One
// convention, stated there, consumed here.
//
// The fleet is on the fabric as the CLOUD ITSELF. The controller trusts Hanzo
// IAM as its only signer, so the cloud logs in to the client API by ext-jwt
// with its own machine token (iamurl.Machine) and is the identity whose
// externalId is that token's subject. That identity carries the reserved org's
// "org-admin" role attribute (the network app ensures it, apps/network
// publish.go own), which is what every published service's dial
// policy admits alongside the owning org — so it can reach any tenant's
// published apiserver while tenants reach only their own.
//
// TRUST is two roots: the system pool verifies the controller, served behind
// Cloudflare under a public certificate, and the CAs the controller publishes at
// /.well-known/est/cacerts, read over that verified connection, verify the edge
// routers a dial crosses.
//
// The context is opened on first use and kept: the SDK holds the edge sessions
// and channels, so one context serves every dial. Every login reads the token
// afresh from the machine source, which renews it before it expires, so a
// re-authentication never presents a stale one. A failed open is not kept, so a
// controller that was briefly unreachable is tried again on the next dial, and a
// deployment without the machine identity fails a ".zt" dial with the reason
// rather than failing at boot.

package fleet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/go-openapi/runtime"
	"github.com/go-openapi/strfmt"
	"github.com/hanzozt/edge-api/rest_util"
	apis "github.com/hanzozt/sdk-golang/edge-apis"
	"github.com/hanzozt/sdk-golang/zt"
	"golang.org/x/oauth2"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
)

// ztSuffix marks a host as a fabric service name rather than a DNS name.
const ztSuffix = ".zt"

// fabricHost reports whether host addresses the fabric's namespace.
func fabricHost(host string) bool {
	return strings.HasSuffix(strings.ToLower(host), ztSuffix)
}

// fabric holds the cloud's fabric context once one has opened.
var fabric struct {
	sync.Mutex
	ztx zt.Context
}

// fabricContext returns the cloud's fabric context, opening it on first use.
func fabricContext() (zt.Context, error) {
	fabric.Lock()
	defer fabric.Unlock()
	if fabric.ztx != nil {
		return fabric.ztx, nil
	}
	src := iam.Machine()
	if src == nil {
		return nil, fmt.Errorf("dialing a %s apiserver requires the cloud's own IAM identity (IAM_CLIENT_ID, IAM_CLIENT_SECRET)", ztSuffix)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("read the system roots: %w", err)
	}
	ztx, err := open(cloud.ZTBase(), src, roots)
	if err != nil {
		return nil, err
	}
	fabric.ztx = ztx
	return ztx, nil
}

// open builds a fabric context on the controller at base that logs in by
// ext-jwt with tokens from src. roots verify the controller; the CAs it
// publishes join them to verify its routers.
func open(base string, src oauth2.TokenSource, roots *x509.CertPool) (zt.Context, error) {
	cas, err := rest_util.GetControllerWellKnownCasWithTlsConfig(base, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		return nil, fmt.Errorf("read the fabric CAs from %s: %w", base, err)
	}
	pool := roots.Clone()
	for _, ca := range cas {
		pool.AddCert(ca)
	}
	cred := &bearer{src: src}
	cred.CaPool = pool
	ztx, err := zt.NewContext(&zt.Config{ZtAPI: base + "/edge/client/v1", Credentials: cred})
	if err != nil {
		return nil, fmt.Errorf("open the fabric context: %w", err)
	}
	ctrl := ztx.(*zt.ContextImpl).CtrlClt
	// POST /authenticate?method=ext-jwt is the one way in, so the SDK never
	// takes the OIDC flow it would pick when the controller offers one.
	ctrl.SetUseOidc(false)
	// Building the context started a background read of the controller's
	// version, and every login rewrites the TLS config that read dials with.
	// Waiting for the read here orders the two.
	ctrl.API.ControllerSupportsOidc()
	return ztx, nil
}

// bearer is the ext-jwt credential with its token read from src at every
// login, never fixed when the context opened.
type bearer struct {
	apis.JwtCredentials
	src oauth2.TokenSource
}

// AuthenticateRequest writes a login's headers with a current IAM token.
func (b *bearer) AuthenticateRequest(r runtime.ClientRequest, reg strfmt.Registry) error {
	t, err := b.src.Token()
	if err != nil {
		return fmt.Errorf("mint the IAM token: %w", err)
	}
	if err := b.BaseCredentials.AuthenticateRequest(r, reg); err != nil {
		return err
	}
	return r.SetHeaderParam("Authorization", "Bearer "+t.AccessToken)
}

// fabricDial dials a ".zt" apiserver over the fabric. The port is carried by
// the published service's own configuration, so only the host matters here; the
// SDK authenticates on demand and the returned edge connection is a net.Conn.
func fabricDial(_ context.Context, _ string, addr string) (net.Conn, error) {
	ztx, err := fabricContext()
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return ztx.Dial(strings.TrimSuffix(strings.ToLower(host), ztSuffix))
}
