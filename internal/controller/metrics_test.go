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

package controller

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	snapshotpodv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
	"github.com/baizeai/kube-snapshot/internal/metrics"
)

func TestReconcilePushImageMetrics(t *testing.T) {
	// Reset metrics before test
	metrics.SnapshotImagePushTotal.Reset()
	metrics.SnapshotImagePushFailuresTotal.Reset()

	reconciler := &SnapshotPodTaskReconciler{}

	// Create a test task with invalid container ID to trigger a failure
	spt := &snapshotpodv1alpha1.SnapshotPodTask{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
		},
		Spec: snapshotpodv1alpha1.SnapshotPodTaskSpec{
			PodName:     "test-pod",
			ContainerID: "invalid://container",
			CommitImage: "test-image:latest",
		},
	}

	ctx := context.Background()

	// Call reconcilePushImage which should fail and record metrics
	err := reconciler.reconcilePushImage(ctx, spt)
	if err == nil {
		t.Error("Expected error for invalid container ID, but got nil")
	}

	// Verify that failure metric was recorded
	expected := 1.0
	actual := testutil.ToFloat64(metrics.SnapshotImagePushFailuresTotal.WithLabelValues("default", "test-pod", "test-image:latest", "runtime_error"))
	if actual != expected {
		t.Errorf("Expected %f failure metric, got %f", expected, actual)
	}

	// Verify that total metric was also recorded
	actual = testutil.ToFloat64(metrics.SnapshotImagePushTotal.WithLabelValues("default", "test-pod", "test-image:latest", "failure", "runtime_error"))
	if actual != expected {
		t.Errorf("Expected %f total metric, got %f", expected, actual)
	}
}
