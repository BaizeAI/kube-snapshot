# snapshot-pod
Snapshot and export running Pod container images on Kubernetes nodes.

## Description
This controller creates point-in-time snapshots of running Pods by committing container filesystem layers and optionally pushing the resulting image to a registry. It offers Prometheus metrics for observability of task states, failures, and controller health.

## Getting Started

### Prerequisites
- go version v1.22.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/snapshot-pod:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/snapshot-pod:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Metrics

All metrics are exposed on the controller's Prometheus endpoint.

- Snapshot tasks
  - `snapshot_tasks_total{status, namespace, snapshot_name}`: Counter. Number of tasks by final/observed status. Use with rate/increase if needed.
  - `snapshot_task_duration_seconds{status, namespace, snapshot_name}`: Histogram. Task duration seconds by status.
  - `snapshot_task_retries_total{namespace, snapshot_name, task_name}`: Counter. Retry attempts count.
  - `snapshot_active_tasks{namespace, snapshot_name}`: Gauge. Current active tasks per SnapshotPod.
  - `snapshot_tasks_by_node{node_name, status}`: Gauge. Task counts per node and status.
  - `snapshot_task_failed_total{namespace, snapshot_name, task_name}`: Counter. Increments when a task step errors or task exhausts max retries.

- Controller
  - `snapshot_controller_reconcile_total{controller, result}`: Counter. Reconcile outcomes (success/error/requeue).
  - `snapshot_controller_reconcile_duration_seconds{controller}`: Histogram. Reconcile latency.
  - `snapshot_controller_errors_total{controller, error_type}`: Counter. Controller error events.

- Image push
  - `snapshot_image_push_total{result, registry, namespace, snapshot_name}`: Counter. Push attempts.
  - `snapshot_image_push_duration_seconds{registry, namespace, snapshot_name}`: Histogram. Push latency.

### Alerting examples (PromQL)

- New failures in last 5 minutes (per task):
```promql
increase(snapshot_task_failed_total[5m]) by (namespace, snapshot_name, task_name) > 0
```

- Any new failure in a namespace:
```promql
sum by (namespace) (increase(snapshot_task_failed_total[5m])) > 0
```

- Optional stability:
```yaml
for: 1m
```

Notes:
- Always alert on a rate/increase over a window for counters; do not compare raw counters to 0.
- Tune the lookback window (e.g., 5m/10m) to balance sensitivity vs stability.

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/snapshot-pod:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/snapshot-pod/<tag or branch>/dist/install.yaml
```

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

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

