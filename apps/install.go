package apps

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/membership"
)

// cloud builds this callback but does not install it: importing the Kubernetes
// client from cloud drags ~1700 packages into every subsystem, since they all
// import cloud for Deps. Registration lives here, in the composition root, where
// membership is already linked — the same register-into-a-registry idiom the
// git→code seam in apps.go uses.
//
// Outside a cluster K8s errors and cloud falls back to its static peer set.
func init() { cloud.Peers = membership.K8s }
