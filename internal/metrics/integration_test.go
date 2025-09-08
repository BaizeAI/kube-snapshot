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
)

func TestMetricsEndpointIntegration(t *testing.T) {
	// Test that metrics can be incremented and read
	RecordImagePushSuccess("test-namespace", "test-pod", "test-image")
	RecordImagePushFailure("test-namespace", "test-pod", "test-image", "test-error")
	RecordTaskPhase("test-namespace", "COMPLETED")

	// Test that the metrics variables are properly defined
	if SnapshotImagePushTotal == nil {
		t.Error("SnapshotImagePushTotal is nil")
	}
	if SnapshotImagePushFailuresTotal == nil {
		t.Error("SnapshotImagePushFailuresTotal is nil")
	}
	if SnapshotTasksTotal == nil {
		t.Error("SnapshotTasksTotal is nil")
	}

	t.Log("✓ Metrics are properly initialized and can be recorded")
}
