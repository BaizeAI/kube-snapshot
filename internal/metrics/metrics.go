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

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// SnapshotImagePushTotal tracks the total number of image push attempts
	SnapshotImagePushTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "snapshot_image_push_total",
			Help: "Total number of snapshot image push attempts",
		},
		[]string{"namespace", "pod", "image", "status", "reason"},
	)

	// SnapshotImagePushFailuresTotal tracks the total number of image push failures
	SnapshotImagePushFailuresTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "snapshot_image_push_failures_total",
			Help: "Total number of snapshot image push failures",
		},
		[]string{"namespace", "pod", "image", "reason"},
	)

	// SnapshotTasksTotal tracks the total number of snapshot tasks by phase
	SnapshotTasksTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "snapshot_tasks_total",
			Help: "Total number of snapshot tasks by phase",
		},
		[]string{"namespace", "phase"},
	)
)

func init() {
	// Register custom metrics with the global prometheus registry which is used by controller-runtime
	metrics.Registry.MustRegister(
		SnapshotImagePushTotal,
		SnapshotImagePushFailuresTotal,
		SnapshotTasksTotal,
	)
}

// RecordImagePushSuccess records a successful image push
func RecordImagePushSuccess(namespace, pod, image string) {
	SnapshotImagePushTotal.WithLabelValues(namespace, pod, image, "success", "").Inc()
}

// RecordImagePushFailure records a failed image push
func RecordImagePushFailure(namespace, pod, image, reason string) {
	SnapshotImagePushTotal.WithLabelValues(namespace, pod, image, "failure", reason).Inc()
	SnapshotImagePushFailuresTotal.WithLabelValues(namespace, pod, image, reason).Inc()
}

// RecordTaskPhase records a task reaching a specific phase
func RecordTaskPhase(namespace, phase string) {
	SnapshotTasksTotal.WithLabelValues(namespace, phase).Inc()
}
