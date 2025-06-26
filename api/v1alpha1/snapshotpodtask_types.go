/*
Copyright 2024.

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

// SnapshotPodTaskSpec defines the desired state of SnapshotPodTask
type SnapshotPodTaskSpec struct {
	NodeName                string        `json:"nodeName,omitempty"`
	PodName                 string        `json:"podName"`
	ContainerID             string        `json:"containerID,omitempty"`
	CommitOptions           CommitOptions `json:"commitOptions"`
	OriginImage             string        `json:"originImage,omitempty"`
	OriginRegistrySecretRef string        `json:"originRegistrySecretRef,omitempty"`
	CommitImage             string        `json:"commitImage,omitempty"`
	RegistrySecretRef       string        `json:"registrySecretRef,omitempty"`

	// MaxRetries defines the maximum number of retries when the task fails
	// +optional
	// +kubebuilder:default:=3
	MaxRetries int32 `json:"maxRetries,omitempty"`

	// RetryDelaySeconds defines the delay between retries in seconds
	// +optional
	// +kubebuilder:default:=30
	RetryDelaySeconds int32 `json:"retryDelaySeconds,omitempty"`
}

type SnapshotPodTaskPhase string

const (
	SnapshotPodTaskPhaseCreated   SnapshotPodTaskPhase = "CREATED"
	SnapshotPodTaskPhaseFailed    SnapshotPodTaskPhase = "FAILED"
	SnapshotPodTaskPhaseCompleted SnapshotPodTaskPhase = "COMPLETED"
)

// SnapshotPodTaskStatus defines the observed state of SnapshotPodTask
type SnapshotPodTaskStatus struct {
	Conditions []metav1.Condition   `json:"conditions,omitempty"`
	Phase      SnapshotPodTaskPhase `json:"phase,omitempty"`

	// RetryCount records the number of retries attempted
	// +optional
	RetryCount int32 `json:"retryCount,omitempty"`

	// LastRetryTime records the time of the last retry attempt
	// +optional
	LastRetryTime *metav1.Time `json:"lastRetryTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// SnapshotPodTask is the Schema for the snapshotpodtasks API
// +kubebuilder:resource:shortName=spt
// +kubebuilder:printcolumn:name="pod",type=string,JSONPath=`.spec.podName`
// +kubebuilder:printcolumn:name="image",type=string,JSONPath=`.spec.commitImage`
// +kubebuilder:printcolumn:name="phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="retries",type=string,JSONPath=`.status.retryCount`
type SnapshotPodTask struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SnapshotPodTaskSpec   `json:"spec,omitempty"`
	Status SnapshotPodTaskStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SnapshotPodTaskList contains a list of SnapshotPodTask
type SnapshotPodTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SnapshotPodTask `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SnapshotPodTask{}, &SnapshotPodTaskList{})
}
