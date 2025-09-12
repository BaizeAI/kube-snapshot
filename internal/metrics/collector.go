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
	"context"
	"time"

	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	snapshotpodv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
)

// MetricsCollector collects and exports metrics
type MetricsCollector struct {
	client client.Client
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(client client.Client) *MetricsCollector {
	return &MetricsCollector{
		client: client,
	}
}

// Start runs the periodic metrics collection loop
func (mc *MetricsCollector) Start(ctx context.Context) {
	logger := log.FromContext(ctx)
	ticker := time.NewTicker(30 * time.Second) // collect every 30 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("metrics collector stopped")
			return
		case <-ticker.C:
			if err := mc.collectMetrics(ctx); err != nil {
				logger.Error(err, "failed to collect metrics")
			}
		}
	}
}

// collectMetrics runs all metric collectors
func (mc *MetricsCollector) collectMetrics(ctx context.Context) error {
	if err := mc.collectSnapshotPodMetrics(ctx); err != nil {
		return err
	}

	if err := mc.collectSnapshotPodTaskMetrics(ctx); err != nil {
		return err
	}

	return nil
}

// collectSnapshotPodMetrics gathers metrics for SnapshotPod
func (mc *MetricsCollector) collectSnapshotPodMetrics(ctx context.Context) error {
	// Reset the gauge vector to clear stale metrics from deleted SnapshotPods
	SnapshotTaskMetrics.Active.Reset()

	var snapshotPods snapshotpodv1alpha1.SnapshotPodList
	if err := mc.client.List(ctx, &snapshotPods); err != nil {
		return err
	}

	for _, sp := range snapshotPods.Items {
		activeCount := float64(len(lo.Filter(sp.Status.Snapshots, func(item snapshotpodv1alpha1.SnapshotRunningStatus, index int) bool {
			return item.Result == snapshotpodv1alpha1.SnapshotRunningResultCreated ||
				item.Result == snapshotpodv1alpha1.SnapshotRunningResultRunning
		})))
		SetActiveSnapshotTasks(sp.Namespace, sp.Name, activeCount)
	}

	return nil
}

// collectSnapshotPodTaskMetrics gathers metrics for SnapshotPodTask
func (mc *MetricsCollector) collectSnapshotPodTaskMetrics(ctx context.Context) error {
	// Reset the gauge vector to clear stale metrics from deleted SnapshotPodTasks
	SnapshotTaskMetrics.ByNode.Reset()

	var snapshotPodTasks snapshotpodv1alpha1.SnapshotPodTaskList
	if err := mc.client.List(ctx, &snapshotPodTasks); err != nil {
		return err
	}

	nodeStatusCounts := make(map[string]map[string]float64)

	for _, spt := range snapshotPodTasks.Items {
		nodeName := spt.Spec.NodeName
		var status string
		switch spt.Status.Phase {
		case snapshotpodv1alpha1.SnapshotPodTaskPhaseCompleted:
			status = SnapshotTaskStatusSuccess
		case snapshotpodv1alpha1.SnapshotPodTaskPhaseFailed:
			status = SnapshotTaskStatusFailed
		case snapshotpodv1alpha1.SnapshotPodTaskPhaseCreated:
			status = SnapshotTaskStatusCreated
		default:
			if spt.Status.Phase == "" {
				status = SnapshotTaskStatusCreated
			} else {
				status = string(spt.Status.Phase)
			}
		}

		if nodeStatusCounts[nodeName] == nil {
			nodeStatusCounts[nodeName] = make(map[string]float64)
		}
		nodeStatusCounts[nodeName][status]++

	}

	for nodeName, statusCounts := range nodeStatusCounts {
		for status, count := range statusCounts {
			SetSnapshotTasksByNode(nodeName, status, count)
		}
	}

	return nil
}
