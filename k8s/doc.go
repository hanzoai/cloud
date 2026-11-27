// Package k8s is the Kubernetes coordinates, declared once: the
// GroupVersionResources for our own CRs and the upstream objects we read.
//
// These are VALUES, not per-subsystem opinions: "the operator App CR" is one fact,
// and it was previously declared three times (apps/platform appsGVR, apps/deploy
// appsCRGVR, apps/platform appsGVR) under two different names. Three copies of a
// constant do not disagree until one of them is edited — and the whole point of the
// App/Service kind migration is that this coordinate CHANGES. When it does, a
// subsystem still holding a private copy silently reads the wrong resource and its
// board goes quietly empty, which is exactly the failure paas already shipped once.
//
// One declaration, one name, every consumer. Adding a coordinate here is how a new
// subsystem addresses the cluster; declaring a private copy is the bug this package
// exists to prevent.
package k8s
