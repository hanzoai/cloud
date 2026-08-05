package cloud

import (
	"context"
	"sync"

	"github.com/hanzoai/cloud/types"
)

// This file is the whole Kubernetes story for the OSS core: a registration seam
// and a default that refuses honestly. There is no client-go here, and that is
// the point — `go list -deps` on this binary finds no k8s.io, so "runs without a
// cluster" is a property of the build rather than a configuration you have to
// get right.
//
// The private build calls RegisterK8sClientFactory with a dynamic-client
// implementation. Nothing else changes: paas and validators already ask deps.K8s
// for what they need and already fail closed when it is not ready.

var (
	k8sFactoryMu sync.RWMutex
	k8sFactory   func(cfg *Config) (K8sClient, error)
)

// RegisterK8sClientFactory installs the cluster implementation. Called from the
// private build's init; the OSS binary never calls it and therefore never links
// a Kubernetes client.
func RegisterK8sClientFactory(f func(cfg *Config) (K8sClient, error)) {
	k8sFactoryMu.Lock()
	defer k8sFactoryMu.Unlock()
	k8sFactory = f
}

// BuildK8sClient returns the registered implementation, or the unavailable
// default. It never returns nil: a nil client would push a nil-check to every
// call site, and the one that got forgotten would panic in a request handler
// instead of returning a 503 that names what is missing.
func BuildK8sClient(cfg *Config) K8sClient {
	k8sFactoryMu.RLock()
	f := k8sFactory
	k8sFactoryMu.RUnlock()
	if f == nil {
		return unavailableK8s{reason: "kubernetes support is not linked into this build (OSS core); " +
			"paas and validators are cluster-facing and stay disabled"}
	}
	c, err := f(cfg)
	if err != nil {
		return unavailableK8s{reason: err.Error()}
	}
	if c == nil {
		return unavailableK8s{reason: "kubernetes factory returned no client"}
	}
	return c
}

// unavailableK8s is the no-cluster default. Every method fails with the SAME
// reason Ready reports, so an operator reading a 503 body and an operator
// reading the health route learn the same thing.
type unavailableK8s struct{ reason string }

func (u unavailableK8s) err() error { return &k8sUnavailableError{reason: u.reason} }

func (u unavailableK8s) Get(context.Context, string, string, string, string, string) (map[string]any, error) {
	return nil, u.err()
}

func (u unavailableK8s) List(context.Context, string, string, string, string, int64) ([]map[string]any, error) {
	return nil, u.err()
}

func (u unavailableK8s) Create(context.Context, string, string, string, string, map[string]any) error {
	return u.err()
}

func (u unavailableK8s) MergePatch(context.Context, string, string, string, string, string, []byte) error {
	return u.err()
}

func (u unavailableK8s) Ready() (bool, string) { return false, u.reason }

// k8sUnavailableError is deliberately NOT one of the cluster sentinels. A
// caller asking "was this a NotFound?" must get "no" when the truth is "there
// is no cluster" — otherwise validators would read an absent cluster as an
// absent object and proceed to Create against nothing.
type k8sUnavailableError struct{ reason string }

func (e *k8sUnavailableError) Error() string { return "k8s: unavailable: " + e.reason }

// Compile-time proof the default satisfies the seam.
var _ types.K8sClient = unavailableK8s{}
