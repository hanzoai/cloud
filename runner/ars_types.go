// Copyright (c) Hanzo AI. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// ars_types.go — minimal AutoscalingRunnerSet CRD.
//
// The controller-side tenant catalog (crTenantSource) lists
// AutoscalingRunnerSet custom resources to discover which scale sets
// exist. Arc's full apis/actions.github.com/v1alpha1 type embeds a
// corev1.PodTemplateSpec (the entire k8s core API) plus proxy, TLS,
// vault, and listener-metadata sub-objects — far too large to copy
// cleanly, and none of it is read by the tenant catalog.
//
// This is the minimal projection: TypeMeta + ObjectMeta + the six Spec
// fields crTenantSource / arsToTenant actually read, with hand-written
// DeepCopy and scheme registration so it satisfies client.Object and
// works with a controller-runtime client (real or fake). The GroupKind
// ("actions.github.com/v1alpha1, AutoscalingRunnerSet") matches arc so
// a client points at the same CRs.
package runner

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group-version these objects register under.
	GroupVersion = schema.GroupVersion{Group: "actions.github.com", Version: "v1alpha1"}

	// SchemeBuilder registers the CRD types with a runtime.Scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the CRD types in this group-version to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(&AutoscalingRunnerSet{}, &AutoscalingRunnerSetList{})
}

// AutoscalingRunnerSet is the schema for the autoscalingrunnersets API.
// Only the identity + sizing fields the tenant catalog reads are
// modeled; the upstream Spec/Status carry much more.
type AutoscalingRunnerSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AutoscalingRunnerSetSpec `json:"spec,omitempty"`
}

// AutoscalingRunnerSetSpec is the minimal desired state read by the
// tenant catalog.
type AutoscalingRunnerSetSpec struct {
	GitHubConfigUrl      string   `json:"githubConfigUrl,omitempty"`
	RunnerGroup          string   `json:"runnerGroup,omitempty"`
	RunnerScaleSetName   string   `json:"runnerScaleSetName,omitempty"`
	RunnerScaleSetLabels []string `json:"runnerScaleSetLabels,omitempty"`
	MaxRunners           *int     `json:"maxRunners,omitempty"`
	MinRunners           *int     `json:"minRunners,omitempty"`
}

// AutoscalingRunnerSetList is a list of AutoscalingRunnerSet.
type AutoscalingRunnerSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AutoscalingRunnerSet `json:"items"`
}

// DeepCopyInto copies the receiver into out.
func (in *AutoscalingRunnerSetSpec) DeepCopyInto(out *AutoscalingRunnerSetSpec) {
	*out = *in
	if in.RunnerScaleSetLabels != nil {
		out.RunnerScaleSetLabels = make([]string, len(in.RunnerScaleSetLabels))
		copy(out.RunnerScaleSetLabels, in.RunnerScaleSetLabels)
	}
	if in.MaxRunners != nil {
		v := *in.MaxRunners
		out.MaxRunners = &v
	}
	if in.MinRunners != nil {
		v := *in.MinRunners
		out.MinRunners = &v
	}
}

// DeepCopy returns a deep copy of the spec.
func (in *AutoscalingRunnerSetSpec) DeepCopy() *AutoscalingRunnerSetSpec {
	if in == nil {
		return nil
	}
	out := new(AutoscalingRunnerSetSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (in *AutoscalingRunnerSet) DeepCopyInto(out *AutoscalingRunnerSet) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

// DeepCopy returns a deep copy of the object.
func (in *AutoscalingRunnerSet) DeepCopy() *AutoscalingRunnerSet {
	if in == nil {
		return nil
	}
	out := new(AutoscalingRunnerSet)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject satisfies runtime.Object.
func (in *AutoscalingRunnerSet) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto copies the receiver into out.
func (in *AutoscalingRunnerSetList) DeepCopyInto(out *AutoscalingRunnerSetList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]AutoscalingRunnerSet, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

// DeepCopy returns a deep copy of the list.
func (in *AutoscalingRunnerSetList) DeepCopy() *AutoscalingRunnerSetList {
	if in == nil {
		return nil
	}
	out := new(AutoscalingRunnerSetList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject satisfies runtime.Object.
func (in *AutoscalingRunnerSetList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
