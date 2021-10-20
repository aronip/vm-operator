/*
Copyright 2021.

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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// VirtualMachineSpec defines the desired state of VirtualMachine
type VirtualMachineSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4
	CPU int32 `json:"cpu"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=8
	Memory int32 `json:"memory"`
	// +kubebuilder:validation:Required
	Template string `json:"template"`
	// +kubebuilder:validation:Required
	ResourcePool string `json:"resourcePool"`
}

type StatusPhase string

const (
	RunningStatusPhase StatusPhase = "RUNNING"
	PendingStatusPhase StatusPhase = "PENDING"
	ErrorStatusPhase   StatusPhase = "ERROR"
)

// VirtualMachineStatus defines the observed state of VirtualMachine
type VirtualMachineStatus struct {
	Phase       StatusPhase `json:"phase"`
	Name        string      `json:"name,omitempty"`
	MoRefID     string      `json:"moRefID,omitempty"`
	LastMessage string      `json:"lastMessage,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
// +kubebuilder:resource:shortName={"vm"}
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="CPU",type=integer,JSONPath=`.spec.cpu`
// +kubebuilder:printcolumn:name="Memory",type=integer,JSONPath=`.spec.memory`
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.template`
// +kubebuilder:printcolumn:name="MoRefId",type=string,JSONPath=`.status.moRefID`
// +kubebuilder:printcolumn:name="Last_Message",type=string,JSONPath=`.status.lastMessage`

// VirtualMachine is the Schema for the virtualmachines API
type VirtualMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualMachineSpec   `json:"spec,omitempty"`
	Status VirtualMachineStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VirtualMachineList contains a list of VirtualMachine
type VirtualMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VirtualMachine{}, &VirtualMachineList{})
}
