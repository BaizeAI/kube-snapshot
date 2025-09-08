# Metrics Documentation

This document describes the Prometheus metrics exposed by kube-snapshot for monitoring image push operations.

## Exposed Metrics

### `snapshot_image_push_total`
Counter metric tracking the total number of snapshot image push attempts.

**Labels:**
- `namespace`: Kubernetes namespace of the SnapshotPodTask
- `pod`: Name of the pod being snapshotted
- `image`: Name of the image being pushed  
- `status`: Either "success" or "failure"
- `reason`: For failures, provides the reason (e.g., "auth_error", "push_failed", "image_not_exists", "runtime_error")

**Examples:**
```
snapshot_image_push_total{namespace="default",pod="my-app-pod",image="registry.example.com/my-app:snapshot-123",status="success",reason=""} 1
snapshot_image_push_total{namespace="default",pod="my-app-pod",image="registry.example.com/my-app:snapshot-124",status="failure",reason="auth_error"} 1
```

### `snapshot_image_push_failures_total`
Counter metric tracking only the failed image push attempts.

**Labels:**
- `namespace`: Kubernetes namespace of the SnapshotPodTask
- `pod`: Name of the pod being snapshotted
- `image`: Name of the image being pushed
- `reason`: Failure reason (e.g., "auth_error", "push_failed", "image_not_exists", "runtime_error")

**Examples:**
```
snapshot_image_push_failures_total{namespace="default",pod="my-app-pod",image="registry.example.com/my-app:snapshot-124",reason="auth_error"} 1
snapshot_image_push_failures_total{namespace="production",pod="db-pod",image="registry.example.com/db:snapshot-456",reason="push_failed"} 3
```

### `snapshot_tasks_total`
Counter metric tracking snapshot tasks by their completion phase.

**Labels:**
- `namespace`: Kubernetes namespace of the SnapshotPodTask
- `phase`: Task phase (e.g., "CREATED", "COMPLETED", "FAILED")

**Examples:**
```
snapshot_tasks_total{namespace="default",phase="COMPLETED"} 25
snapshot_tasks_total{namespace="default",phase="FAILED"} 3
```

## Configuration

### Enabling Metrics
Metrics are enabled by default on port `:8080`. You can configure the metrics endpoint using the `--metrics-bind-address` flag:

```bash
# Default (metrics enabled on :8080)
./manager

# Custom port
./manager --metrics-bind-address=:9090

# Disable metrics
./manager --metrics-bind-address=0
```

### Secure Metrics
For production environments, you can enable secure metrics serving:

```bash
./manager --metrics-secure=true
```

## Alerting Examples

### Image Push Failure Rate Alert
Alert when image push failure rate exceeds 10% over 5 minutes:

```yaml
groups:
- name: kube-snapshot-alerts
  rules:
  - alert: HighImagePushFailureRate
    expr: |
      (
        sum(rate(snapshot_image_push_failures_total[5m])) /
        sum(rate(snapshot_image_push_total[5m]))
      ) > 0.1
    for: 2m
    labels:
      severity: warning
    annotations:
      summary: "High image push failure rate for kube-snapshot"
      description: "Image push failure rate is {{ $value | humanizePercentage }} over the last 5 minutes"
```

### Specific Image Push Failures
Alert on any image push failures for critical applications:

```yaml
  - alert: CriticalImagePushFailure  
    expr: increase(snapshot_image_push_failures_total{namespace="production"}[5m]) > 0
    for: 0m
    labels:
      severity: critical
    annotations:
      summary: "Image push failed for production namespace"
      description: "Image push failed for pod {{ $labels.pod }} in namespace {{ $labels.namespace }}: {{ $labels.reason }}"
```

### Registry Authentication Issues
Alert specifically on authentication errors:

```yaml
  - alert: RegistryAuthenticationFailure
    expr: increase(snapshot_image_push_failures_total{reason="auth_error"}[5m]) > 0
    for: 0m
    labels:
      severity: warning
    annotations:
      summary: "Registry authentication failure"
      description: "Authentication failed for image {{ $labels.image }} in namespace {{ $labels.namespace }}"
```

## Grafana Dashboard Examples

### Push Success Rate Panel
```json
{
  "targets": [
    {
      "expr": "sum(rate(snapshot_image_push_total{status=\"success\"}[5m])) / sum(rate(snapshot_image_push_total[5m]))",
      "legendFormat": "Success Rate"
    }
  ],
  "title": "Image Push Success Rate",
  "type": "stat"
}
```

### Failed Pushes by Reason
```json
{
  "targets": [
    {
      "expr": "sum by (reason) (rate(snapshot_image_push_failures_total[5m]))",
      "legendFormat": "{{ reason }}"
    }
  ],
  "title": "Push Failures by Reason",
  "type": "graph"
}
```