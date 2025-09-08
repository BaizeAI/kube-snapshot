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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestImagePushMetrics(t *testing.T) {
	// Reset metrics before test
	SnapshotImagePushTotal.Reset()
	SnapshotImagePushFailuresTotal.Reset()

	// Test recording a successful push
	RecordImagePushSuccess("default", "test-pod", "test-image:latest")

	// Verify the success metric was incremented
	expected := 1.0
	actual := testutil.ToFloat64(SnapshotImagePushTotal.WithLabelValues("default", "test-pod", "test-image:latest", "success", ""))
	if actual != expected {
		t.Errorf("Expected %f, got %f for successful push metric", expected, actual)
	}

	// Test recording a failed push
	RecordImagePushFailure("default", "test-pod", "test-image:latest", "auth_error")

	// Verify the failure metric was incremented
	actual = testutil.ToFloat64(SnapshotImagePushTotal.WithLabelValues("default", "test-pod", "test-image:latest", "failure", "auth_error"))
	if actual != expected {
		t.Errorf("Expected %f, got %f for failed push metric", expected, actual)
	}

	// Verify the dedicated failure metric was also incremented
	actual = testutil.ToFloat64(SnapshotImagePushFailuresTotal.WithLabelValues("default", "test-pod", "test-image:latest", "auth_error"))
	if actual != expected {
		t.Errorf("Expected %f, got %f for dedicated failure metric", expected, actual)
	}
}

func TestTaskPhaseMetrics(t *testing.T) {
	// Reset metrics before test
	SnapshotTasksTotal.Reset()

	// Test recording task phases
	RecordTaskPhase("default", "COMPLETED")
	RecordTaskPhase("default", "FAILED")

	// Verify the metrics were incremented
	expected := 1.0
	actual := testutil.ToFloat64(SnapshotTasksTotal.WithLabelValues("default", "COMPLETED"))
	if actual != expected {
		t.Errorf("Expected %f, got %f for COMPLETED task metric", expected, actual)
	}

	actual = testutil.ToFloat64(SnapshotTasksTotal.WithLabelValues("default", "FAILED"))
	if actual != expected {
		t.Errorf("Expected %f, got %f for FAILED task metric", expected, actual)
	}
}

func TestMetricsRegistration(t *testing.T) {
	// Verify that our metrics are registered with prometheus
	registry := prometheus.NewRegistry()

	// This should not panic if metrics are properly defined
	err := registry.Register(SnapshotImagePushTotal)
	if err != nil {
		t.Errorf("Failed to register SnapshotImagePushTotal: %v", err)
	}

	err = registry.Register(SnapshotImagePushFailuresTotal)
	if err != nil {
		t.Errorf("Failed to register SnapshotImagePushFailuresTotal: %v", err)
	}

	err = registry.Register(SnapshotTasksTotal)
	if err != nil {
		t.Errorf("Failed to register SnapshotTasksTotal: %v", err)
	}
}
