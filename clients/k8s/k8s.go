// Package k8s holds the Kubernetes coordinates the cloud binary addresses — the
// GroupVersionResources that identify our own CRs and the upstream objects we read.
//
// These are VALUES, not per-subsystem opinions: "the operator App CR" is one fact,
// and it was previously declared three times (clients/paas appsGVR, clients/deploy
// appsCRGVR, clients/platform appsGVR) under two different names. Three copies of a
// constant do not disagree until one of them is edited — and the whole point of the
// App/Service kind migration is that this coordinate CHANGES. When it does, a
// subsystem still holding a private copy silently reads the wrong resource and its
// board goes quietly empty, which is exactly the failure paas already shipped once.
//
// One declaration, one name, every consumer. Adding a coordinate here is how a new
// subsystem addresses the cluster; declaring a private copy is the bug this package
// exists to prevent.
package k8s

import "k8s.io/apimachinery/pkg/runtime/schema"

// Apps is the operator's App CR (hanzo.ai/v1) — the ONE workload kind the fleet
// runs on. `Service` is the v0.3.0 alias it replaced; the fleet holds zero of them,
// so there is deliberately no ServicesGVR here to scan as well.
var Apps = schema.GroupVersionResource{Group: "hanzo.ai", Version: "v1", Resource: "apps"}

// Deployments is the live Deployment behind each App — the source of the RUNNING
// image tag, since the App CR's status does not surface it.
var Deployments = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

// Namespaces backs scan-set discovery: asking which namespaces exist is how the
// fleet is found, rather than hardcoding a list that goes stale on the next tenant.
var Namespaces = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
