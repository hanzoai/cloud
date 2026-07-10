// Copyright (c) Hanzo AI. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// transport.go — the OPTIONAL controller<->host control channel.
//
// The host role is complete without a control channel: it drives tenant
// selection from the local YAML config (cfg.Orgs) and mints JIT runners
// directly against GitHub. That is the documented offline / local mode.
//
// When a control plane is available, the host additionally opens a
// session to the in-cluster arcd singleton to heartbeat and pull the
// tenant set. arcd's concrete implementation is ZAP-on-QUIC with an
// X25519MLKEM768 hybrid PQ-KEM mTLS wire — but this package does not
// depend on it. The host only needs the small surface below; the
// concrete Dialer (and all of its cert/KMS machinery) is injected by
// the cloud CLI. Default is nil: no dialer, YAML-only mode.
package runner

import "context"

// Tenant is the wire identity of one runner scale set the controller
// tells a host to serve. Credentials never appear here — they are
// loaded locally, keyed by RunnerScaleSetName (see credentials.go).
//
// This is a verbatim copy of arc's arctransport.Tenant so the tenant
// catalog (tenantsource.go) and credential merge (credentials.go) port
// without depending on the archived arctransport package.
type Tenant struct {
	Name                        string
	Org                         string
	RunnerLabels                []string
	RunnerGroup                 string
	Arch                        string
	RunnerScaleSetID            uint32
	MaxRunners                  uint32
	ConfigureURL                string
	RunnerScaleSetName          string
	EphemeralRunnerSetName      string
	EphemeralRunnerSetNamespace string
	MinRunners                  uint32
}

// ControlChannel is everything the host role needs from a control-plane
// transport. It is intentionally tiny — heartbeat + tenant pull + close.
// A nil ControlChannel means "no control plane": the host runs fully on
// its local YAML config, which is the supported offline fallback.
type ControlChannel interface {
	// GetTenants pulls the set of tenants this box should be serving.
	GetTenants(ctx context.Context, box string, labels []string, arch, goos string) ([]Tenant, error)
	// Heartbeat reports the box's liveness to the controller.
	Heartbeat(ctx context.Context, box string, lastReconcileUnixSec int64, version string) error
	// Close releases the underlying connection.
	Close() error
}

// Dialer opens a ControlChannel to the given control-plane address for
// the given host config. Returning (nil, nil) is legal and means "no
// channel available"; the host logs the fallback and stays in YAML-only
// mode. The concrete arctransport dialer — cert material from KMS, the
// ZAP-on-QUIC client — lives in the cloud CLI and is assigned to
// Config.Dialer there. This package ships no default dialer, so the
// host is standalone unless one is injected.
type Dialer func(ctx context.Context, addr string, cfg *Config) (ControlChannel, error)
