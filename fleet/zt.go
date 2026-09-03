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
// The identity is ZT_IDENTITY_FILE: an enrolled fabric identity JSON,
// KMS-injected in production the same way the network client's ZT_* credential
// is. It carries the reserved org's "org-admin" role attribute, which is what
// every published service's dial policy admits alongside the owning org — so
// this one identity can reach any tenant's published apiserver while tenants
// reach only their own. Opened on first use and kept: the SDK holds the edge
// sessions and channels, so one context serves every dial. A deployment without
// the identity fails a ".zt" dial with the reason rather than failing at boot.
package fleet

import (
	"github.com/hanzoai/cloud/internal/environ"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/hanzozt/sdk-golang/zt"
)

// ztIdentityEnv names the cloud's own enrolled fabric identity file.
const ztIdentityEnv = "ZT_IDENTITY_FILE"

// ztSuffix marks a host as a fabric service name rather than a DNS name.
const ztSuffix = ".zt"

// fabricHost reports whether host addresses the fabric's namespace.
func fabricHost(host string) bool {
	return strings.HasSuffix(strings.ToLower(host), ztSuffix)
}

// fabric opens the cloud's fabric context once. An error is kept too: the
// identity file is deployment configuration, so a deployment without it answers
// every fabric dial the same way rather than probing the filesystem per dial.
var fabric = sync.OnceValues(func() (zt.Context, error) {
	path := environ.Or(ztIdentityEnv, "")
	if path == "" {
		return nil, fmt.Errorf("dialing a %s apiserver requires %s (an enrolled fabric identity)", ztSuffix, ztIdentityEnv)
	}
	ztx, err := zt.NewContextFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("open fabric identity %s: %w", path, err)
	}
	return ztx, nil
})

// fabricDial dials a ".zt" apiserver over the fabric. The port is carried by
// the published service's own configuration, so only the host matters here; the
// SDK authenticates on demand and the returned edge connection is a net.Conn.
func fabricDial(_ context.Context, _ string, addr string) (net.Conn, error) {
	ztx, err := fabric()
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return ztx.Dial(strings.TrimSuffix(strings.ToLower(host), ztSuffix))
}
