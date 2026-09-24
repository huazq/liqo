// Copyright 2019-2026 The Liqo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RemoteAPIAccessSpec defines the desired ZBT access path to a remote Kubernetes API server.
type RemoteAPIAccessSpec struct {
	// RemoteClusterID is the Liqo cluster ID whose API server is reached through this access path.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="RemoteClusterID field is immutable"
	RemoteClusterID ClusterID `json:"remoteClusterID"`
}

// RemoteAPIAccessStatus defines the observed state of RemoteAPIAccess.
type RemoteAPIAccessStatus struct {
	// ListenerAddress is the cluster-local host:port of the ZBT listener.
	ListenerAddress string `json:"listenerAddress,omitempty"`

	// ListenerNodePort is the node port used by CLI bootstrap clients outside
	// the cluster. It is protected by the Zenoh transport configuration.
	ListenerNodePort int32 `json:"listenerNodePort,omitempty"`

	// Conditions report listener and backend readiness.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=remoteapiaccesses,scope=Namespaced,shortName=raa
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Remote cluster",type="string",JSONPath=".spec.remoteClusterID"
// +kubebuilder:printcolumn:name="Listener",type="string",JSONPath=".status.listenerAddress"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// RemoteAPIAccess declares the local ZBT path to one remote Kubernetes API server.
type RemoteAPIAccess struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RemoteAPIAccessSpec   `json:"spec,omitempty"`
	Status RemoteAPIAccessStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RemoteAPIAccessList contains a list of RemoteAPIAccess objects.
type RemoteAPIAccessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RemoteAPIAccess `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RemoteAPIAccess{}, &RemoteAPIAccessList{})
}
