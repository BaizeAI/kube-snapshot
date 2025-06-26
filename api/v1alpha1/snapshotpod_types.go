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

type TargetPod struct {
	// Name of the target pod, one of name or selector is required.
	Name string `json:"name,omitempty"`
	// Selector of the target pod (labels). One of name or selector is required.
	// Matching logic:
	// 1. If both name and selector are specified, only name will be used for matching, and selector will be ignored.
	// 2. If name is not specified, selector will be used to match pods.
	//    - Only one pod can be matched by selector.
	//    - If multiple pods are matched, an error will be raised.
	Selector map[string]string `json:"selector,omitempty"`
	// Containers to snapshot, if not specified, snapshot all containers in the pod.
	Containers []string `json:"containers,omitempty"`
}

type CommitOptions struct {
	// PauseContainer specifies whether to pause the container before snapshot.
	PauseContainer bool `json:"pauseContainer,omitempty"`
	// Timeout specifies the timeout for the snapshot operation.
	Timeout string `json:"timeout,omitempty"`
	// Message specifies the message for the snapshot(will save to image's label).
	Message string `json:"message,omitempty"`
	// Author specifies the author for the snapshot(will save to image).
	Author string `json:"author,omitempty"`
}

type ImageSaveOptions struct {
	// ImageRefFormat is the format of new image.
	// we will replace {key} with is value.
	// eg: ImageRefFormat: aihaobor.daocloud.io/{domain}/{repository}:{tag}-202405230101
	// will be rendered as aihaobor.daocloud.io/docker.io/nginx:1.30.1-202405230101
	// the following keys supported:
	// domain - the registry domain, if origin image without domain, like nginx, we will set it to docker.io
	// repository - the repository of origin image, the whole part behind the domain, beyond : , like baize/baize-ui
	// last_repository - the last part repository, like baize-ui
	// tag - the tag of origin image, if not set, will be latest, if use sha format image like nginx@sha256:abcdef, tag will be the real sha value like abcdef
	ImageRefFormat    string `json:"imageRefFormat,omitempty"`
	RegistrySecretRef string `json:"registrySecretRef,omitempty"`
	// MaxRetries defines the maximum number of retries when the task fails
	// +optional
	// +kubebuilder:default:=3
	MaxRetries int32 `json:"maxRetries,omitempty"`

	// RetryDelaySeconds defines the delay between retries in seconds
	// +optional
	// +kubebuilder:default:=30
	RetryDelaySeconds int32 `json:"retryDelaySeconds,omitempty"`
}

type AutoSaveOptions struct {
	// weather to save the snapshot automatically when pod is terminated.
	AutoSaveOnTermination bool `json:"autoSaveOnTermination,omitempty"`
	// Cron expression for auto save.
	Cron string `json:"cron,omitempty"`
}

// SnapshotPodSpec defines the desired state of SnapshotPod
type SnapshotPodSpec struct {
	// Target pod to snapshot.
	Target TargetPod `json:"target,omitempty"`
	// Commit options.
	CommitOptions CommitOptions `json:"commitOptions,omitempty"`
	// Image save options.
	ImageSaveOptions ImageSaveOptions `json:"imageSaveOptions,omitempty"`
	// Auto save options.
	AutoSaveOptions AutoSaveOptions `json:"autoSaveOptions,omitempty"`
	// +kubebuilder:validation:Optional
	TriggerRound int32 `json:"triggerRound"`
	// +kubebuilder:validation:Optional
	// RecoveryRound define the round to recovery on the next time.
	// 0 means the last succeed round.
	RecoveryRound int32 `json:"recoveryRound"`
}

type SnapshotRunningResult string

const (
	SnapshotRunningResultCreated SnapshotRunningResult = "CREATED"
	SnapshotRunningResultRunning SnapshotRunningResult = "RUNNING"
	SnapshotRunningResultFailed  SnapshotRunningResult = "FAILED"
	SnapshotRunningResultSuccess SnapshotRunningResult = "SUCCEED"
)

type SnapshotRunningStatus struct {
	TriggerRound int32       `json:"triggerRound"`
	TriggerTime  metav1.Time `json:"triggerTime,omitempty"`
	// TaskName ref to SnapshotPodTask
	TaskName      string                `json:"taskName"`
	ImageRef      string                `json:"imageRef,omitempty"`
	ContainerName string                `json:"containerName"`
	Result        SnapshotRunningResult `json:"result,omitempty"`
	FailedReason  string                `json:"failedReason,omitempty"`
	CompletedAt   *metav1.Time          `json:"completedAt,omitempty"`
}

// SnapshotPodStatus defines the observed state of SnapshotPod
type SnapshotPodStatus struct {
	// Running status of the snapshot.
	Snapshots        []SnapshotRunningStatus `json:"runs,omitempty"`
	Conditions       []metav1.Condition      `json:"conditions"`
	LastTriggerRound int32                   `json:"lastTriggerRound"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// SnapshotPod is the Schema for the snapshotpods API
// +kubebuilder:resource:shortName=snap;sp
// +kubebuilder:printcolumn:name="pod",type=string,JSONPath=`.spec.target.name`
// +kubebuilder:printcolumn:name="triggerRound",type=number,JSONPath=`.spec.triggerRound`
type SnapshotPod struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SnapshotPodSpec   `json:"spec,omitempty"`
	Status SnapshotPodStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SnapshotPodList contains a list of SnapshotPod
type SnapshotPodList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SnapshotPod `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SnapshotPod{}, &SnapshotPodList{})
}
