/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	acceptAutomatic = "acceptAutomatic"
)

// ServiceAttachment represents a Service Attachment associated with a service/ingress/gateway class
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
type ServiceAttachment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServiceAttachmentSpec   `json:"spec,omitempty"`
	Status ServiceAttachmentStatus `json:"status,omitempty"`
}

// ServiceAttachmentSpec is the spec for a ServiceAttachment resource
// +k8s:openapi-gen=true
type ServiceAttachmentSpec struct {
	// ConnectionPreference determines how consumers are accepted. Only allowed value is `acceptAutomatic`.
	// +required
	ConnectionPreference string `json:"connectionPreference"`

	// NATSubnets contains the list of subnet names for PSC
	// +required
	// +listType=atomic
	NATSubnets []string `json:"natSubnets"`

	// ResourceReference is the reference to the K8s resource that created the forwarding rule
	// Only Services can be used as a reference
	// +required
	// ResourceReference *core.TypedLocalObjectReference `json:"resourceReference"`
	ResourceReference ResourceReference `json:"resourceReference"`
}

// ServiceAttachmentStatus is the status for a ServiceAttachment resource
// +k8s:openapi-gen=true
type ServiceAttachmentStatus struct {
	// ServiceAttachmentURL is the URL for the GCE Service Attachment resource
	ServiceAttachmentURL string `json:"serviceAttachmentURL"`

	// ForwardingRuleURL is the URL to the GCE Forwarding Rule resource the
	// Service Attachment points to
	ForwardingRuleURL string `json:"forwardingRuleURL"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// ServiceAttachmentList is a list of ServiceAttachment resources
type ServiceAttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ServiceAttachment `json:"items"`
}

// +k8s:openapi-gen=true
type ResourceReference struct {
	// APIGroup is the group for the resource being referenced.
	// If APIGroup is not specified, the specified Kind must be in the core API group.
	// For any other third-party types, APIGroup is required.
	// +optional
	APIGroup *string `json:"apiGroup"`
	// Kind is the type of resource being referenced
	Kind string `json:"kind"`
	// Name is the name of resource being referenced
	Name string `json:"name"`
}
