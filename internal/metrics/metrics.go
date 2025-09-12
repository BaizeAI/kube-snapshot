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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	SnapshotTaskMetrics = struct {
		Total    *prometheus.CounterVec
		Duration *prometheus.HistogramVec
		Retries  *prometheus.CounterVec
		Active   *prometheus.GaugeVec
		ByNode   *prometheus.GaugeVec
	}{
		Total: promauto.With(crmetrics.Registry).NewCounterVec(prometheus.CounterOpts{
			Name: "snapshot_tasks_total",
			Help: "Total number of snapshot tasks",
		}, []string{"status", "namespace", "snapshot_name"}),

		Duration: promauto.With(crmetrics.Registry).NewHistogramVec(prometheus.HistogramOpts{
			Name:    "snapshot_task_duration_seconds",
			Help:    "Duration of snapshot tasks in seconds",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10), // 1s, 2s, 4s, 8s, 16s, 32s, 64s, 128s, 256s, 512s
		}, []string{"status", "namespace", "snapshot_name"}),

		Retries: promauto.With(crmetrics.Registry).NewCounterVec(prometheus.CounterOpts{
			Name: "snapshot_task_retries_total",
			Help: "Total number of snapshot task retries",
		}, []string{"namespace", "snapshot_name", "task_name"}),

		Active: promauto.With(crmetrics.Registry).NewGaugeVec(prometheus.GaugeOpts{
			Name: "snapshot_active_tasks",
			Help: "Number of active snapshot tasks",
		}, []string{"namespace", "snapshot_name"}),

		ByNode: promauto.With(crmetrics.Registry).NewGaugeVec(prometheus.GaugeOpts{
			Name: "snapshot_tasks_by_node",
			Help: "Number of snapshot tasks by node",
		}, []string{"node_name", "status"}),
	}

	ControllerMetrics = struct {
		ReconcileTotal    *prometheus.CounterVec
		ReconcileDuration *prometheus.HistogramVec
		ErrorsTotal       *prometheus.CounterVec
	}{
		ReconcileTotal: promauto.With(crmetrics.Registry).NewCounterVec(prometheus.CounterOpts{
			Name: "snapshot_controller_reconcile_total",
			Help: "Total number of controller reconciles",
		}, []string{"controller", "result"}),

		ReconcileDuration: promauto.With(crmetrics.Registry).NewHistogramVec(prometheus.HistogramOpts{
			Name:    "snapshot_controller_reconcile_duration_seconds",
			Help:    "Duration of controller reconciles in seconds",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 15), // 1ms to ~32s
		}, []string{"controller"}),

		ErrorsTotal: promauto.With(crmetrics.Registry).NewCounterVec(prometheus.CounterOpts{
			Name: "snapshot_controller_errors_total",
			Help: "Total number of controller errors",
		}, []string{"controller", "error_type"}),
	}

	ImageMetrics = struct {
		PushTotal    *prometheus.CounterVec
		PushDuration *prometheus.HistogramVec
	}{
		PushTotal: promauto.With(crmetrics.Registry).NewCounterVec(prometheus.CounterOpts{
			Name: "snapshot_image_push_total",
			Help: "Total number of image push operations",
		}, []string{"result", "registry", "namespace", "snapshot_name"}),

		PushDuration: promauto.With(crmetrics.Registry).NewHistogramVec(prometheus.HistogramOpts{
			Name:    "snapshot_image_push_duration_seconds",
			Help:    "Duration of image push operations in seconds",
			Buckets: prometheus.ExponentialBuckets(1, 2, 12), // 1s to ~1h
		}, []string{"registry", "namespace", "snapshot_name"}),
	}

	SnapshotTaskFailed = promauto.With(crmetrics.Registry).NewCounterVec(prometheus.CounterOpts{
		Name: "snapshot_task_failed_total",
		Help: "Total number of failed snapshot tasks",
	}, []string{"namespace", "snapshot_name", "task_name"})
)

// SnapshotTaskStatus
const (
	SnapshotTaskStatusCreated  = "created"
	SnapshotTaskStatusRunning  = "running"
	SnapshotTaskStatusSuccess  = "success"
	SnapshotTaskStatusFailed   = "failed"
	SnapshotTaskStatusRetrying = "retrying"
)

// ControllerResult
const (
	ControllerResultSuccess = "success"
	ControllerResultError   = "error"
	ControllerResultRequeue = "requeue"
)

// ImagePushResult
const (
	ImagePushResultSuccess = "success"
	ImagePushResultFailed  = "failed"
)

// RecordSnapshotTask
func RecordSnapshotTask(status, namespace, snapshotName string, duration time.Duration) {
	SnapshotTaskMetrics.Total.WithLabelValues(status, namespace, snapshotName).Inc()
	SnapshotTaskMetrics.Duration.WithLabelValues(status, namespace, snapshotName).Observe(duration.Seconds())
}

// RecordSnapshotTaskRetry
func RecordSnapshotTaskRetry(namespace, snapshotName, taskName string) {
	SnapshotTaskMetrics.Retries.WithLabelValues(namespace, snapshotName, taskName).Inc()
}

// SetActiveSnapshotTasks
func SetActiveSnapshotTasks(namespace, snapshotName string, count float64) {
	SnapshotTaskMetrics.Active.WithLabelValues(namespace, snapshotName).Set(count)
}

// SetSnapshotTasksByNode
func SetSnapshotTasksByNode(nodeName, status string, count float64) {
	SnapshotTaskMetrics.ByNode.WithLabelValues(nodeName, status).Set(count)
}

// RecordControllerReconcile
func RecordControllerReconcile(controller, result string, duration time.Duration) {
	ControllerMetrics.ReconcileTotal.WithLabelValues(controller, result).Inc()
	ControllerMetrics.ReconcileDuration.WithLabelValues(controller).Observe(duration.Seconds())
}

// RecordControllerError
func RecordControllerError(controller, errorType string) {
	ControllerMetrics.ErrorsTotal.WithLabelValues(controller, errorType).Inc()
}

// RecordImagePush
func RecordImagePush(result, registry, namespace, snapshotName string, duration time.Duration) {
	ImageMetrics.PushTotal.WithLabelValues(result, registry, namespace, snapshotName).Inc()
	ImageMetrics.PushDuration.WithLabelValues(registry, namespace, snapshotName).Observe(duration.Seconds())
}

// RecordTaskFailed
func RecordTaskFailed(namespace, snapshotName, taskName string) {
	SnapshotTaskFailed.WithLabelValues(namespace, snapshotName, taskName).Inc()
}
