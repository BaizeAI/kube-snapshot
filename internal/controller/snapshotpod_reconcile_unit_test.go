package controller

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	snapshotv1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
	snapshotpkg "github.com/baizeai/kube-snapshot/pkg/apis/snapshotpod/v1alpha1"
)

// TestSnapshotReconcileWithoutPod checks existing task recovery and status updates without creating tasks.
// 目标 Pod 已不存在时仍同步已创建 Task；缺失 Task 的轮次则保留未完成状态并安排重试。
func TestSnapshotReconcileWithoutPod(t *testing.T) {
	for _, tc := range []struct {
		name, request    string
		containers       []string
		withRun, missing bool
	}{
		{name: "request with recorded task", request: `{"round":2,"images":{"app":"registry.example/repo:tag"},"registrySecretRef":"secret"}`, withRun: true},
		{name: "request recovers missing run", request: `{"round":2,"images":{"app":"registry.example/repo:tag"},"registrySecretRef":"secret"}`},
		{
			name: "request with missing task",
			request: `{"round":2,"images":{"app":"registry.example/repo:tag","sidecar":"registry.example/sidecar:tag"},` +
				`"registrySecretRef":"secret"}`,
			missing: true,
		},
		{name: "invalid request still syncs recorded task", request: `{`, withRun: true, missing: true},
		{name: "legacy explicit containers", containers: []string{"app"}, withRun: true},
		{name: "legacy unknown scope completed", withRun: true},
		{name: "legacy unknown scope incomplete", withRun: true, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := snapshotv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			sp := &snapshotv1.SnapshotPod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "snap", Namespace: "ns", UID: types.UID("uid"),
				},
				Spec: snapshotv1.SnapshotPodSpec{
					TriggerRound: 2,
					Target:       snapshotv1.TargetPod{Name: "gone", Containers: tc.containers},
				},
				Status: snapshotv1.SnapshotPodStatus{LastTriggerRound: 2},
			}
			if tc.name == "request with missing task" || tc.name == "legacy unknown scope incomplete" {
				sp.Status.LastTriggerRound = 1
			}
			if tc.name == "legacy unknown scope completed" {
				sp.Status.Conditions = []metav1.Condition{{
					Type: "Tasks", Status: metav1.ConditionFalse, Reason: "TasksReady",
					Message: "container scope is unknown", LastTransitionTime: metav1.Now(),
				}}
			}
			if tc.request != "" {
				sp.Annotations = map[string]string{snapshotpkg.RoundRequestAnnotation: tc.request}
			}
			if tc.withRun {
				sp.Status.Snapshots = []snapshotv1.SnapshotRunningStatus{{
					TriggerRound: 2, TaskName: "snap-2-app", ContainerName: "app",
					ImageRef: "registry.example/repo:tag", Result: snapshotv1.SnapshotRunningResultCreated,
				}}
			}
			task := &snapshotv1.SnapshotPodTask{
				ObjectMeta: metav1.ObjectMeta{
					Name: "snap-2-app", Namespace: "ns",
					OwnerReferences: []metav1.OwnerReference{snapshotPodOwner(sp)},
					Labels:          map[string]string{snapshotpkg.SnapshotNameLabel: sp.Name},
				},
				Spec:   snapshotv1.SnapshotPodTaskSpec{CommitImage: "registry.example/repo:tag"},
				Status: snapshotv1.SnapshotPodTaskStatus{Phase: snapshotv1.SnapshotPodTaskPhaseCompleted},
			}
			if tc.request != "" {
				task.Annotations = map[string]string{snapshotpkg.RestoreCredentialSourceAnnotation: snapshotpkg.RestoreCredentialSourceTask}
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sp, task).WithObjects(sp, task).Build()
			r := &SnapshotPodReconciler{Client: c, Scheme: scheme}
			result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(sp)})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(sp), sp); err != nil {
				t.Fatal(err)
			}
			if len(sp.Status.Snapshots) != 1 || sp.Status.Snapshots[0].Result != snapshotv1.SnapshotRunningResultSuccess {
				t.Fatalf("task status was not synced: %+v", sp.Status.Snapshots)
			}
			var tasks snapshotv1.SnapshotPodTaskList
			if err := c.List(t.Context(), &tasks, client.InNamespace("ns")); err != nil {
				t.Fatal(err)
			}
			if len(tasks.Items) != 1 {
				t.Fatalf("unexpected tasks: %+v", tasks.Items)
			}
			if tc.missing && result.RequeueAfter == 0 {
				t.Fatal("incomplete round must be retried")
			}
			if tc.name == "legacy unknown scope completed" {
				if result.RequeueAfter != 0 {
					t.Fatalf("completed legacy round was requeued: %v", result.RequeueAfter)
				}
				found := false
				for _, condition := range sp.Status.Conditions {
					if condition.Type == "Tasks" {
						found = true
						if condition.Status != metav1.ConditionTrue || condition.Message != "" {
							t.Fatalf("tasks condition was not recovered: %+v", condition)
						}
					}
				}
				if !found {
					t.Fatal("tasks condition is missing")
				}
			}
			if (tc.name == "request with missing task" || tc.name == "legacy unknown scope incomplete") && sp.Status.LastTriggerRound != 1 {
				t.Fatalf("incomplete round advanced: %d", sp.Status.LastTriggerRound)
			}
		})
	}
}

// TestSnapshotReconcileLegacyPartialRound verifies that old round markers do not prevent missing task creation.
// 兼容历史状态将 LastTriggerRound 与 TriggerRound 标成相等、但请求注解仍属于旧轮的情况，并检查重复调谐不会重复补建容器 Task。
func TestSnapshotReconcileLegacyPartialRound(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%t", explicit), func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := snapshotv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			sp := &snapshotv1.SnapshotPod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "snap", Namespace: "ns", UID: types.UID("uid"),
					Annotations: map[string]string{
						snapshotpkg.RoundRequestAnnotation: `{"round":1,"images":{"app":"registry.example/old:tag"},` +
							`"registrySecretRef":""}`,
					},
				},
				Spec: snapshotv1.SnapshotPodSpec{
					TriggerRound: 2,
					Target:       snapshotv1.TargetPod{Name: "source"},
					ImageSaveOptions: snapshotv1.ImageSaveOptions{
						ImageRefFormat: "registry.example/{last_repository}:{tag}",
					},
				},
				Status: snapshotv1.SnapshotPodStatus{
					LastTriggerRound: 2,
					Snapshots: []snapshotv1.SnapshotRunningStatus{{
						TriggerRound: 2, TaskName: "snap-2-app", ContainerName: "app",
						ImageRef: "registry.example/original:fixed",
						Result:   snapshotv1.SnapshotRunningResultSuccess,
					}},
				},
			}
			if explicit {
				sp.Spec.Target.Containers = []string{"app", "sidecar"}
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "ns"},
				Spec: corev1.PodSpec{
					NodeName:   "node",
					Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}},
				},
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
					Name: "sidecar", Image: "docker.io/library/sidecar:v1",
					ContainerID: "containerd://sidecar",
				}}},
			}
			task := &snapshotv1.SnapshotPodTask{
				ObjectMeta: metav1.ObjectMeta{
					Name: "snap-2-app", Namespace: "ns",
					OwnerReferences: []metav1.OwnerReference{snapshotPodOwner(sp)},
					Labels:          map[string]string{snapshotpkg.SnapshotNameLabel: sp.Name},
				},
				Spec: snapshotv1.SnapshotPodTaskSpec{CommitImage: "registry.example/original:fixed"},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(sp, task).WithObjects(sp, pod, task).Build()
			r := &SnapshotPodReconciler{Client: c, Scheme: scheme}
			// 连续调谐两次：首次补建缺失容器，后续调谐应复用同名 Task。
			for range 2 {
				if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(sp)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(sp), sp); err != nil {
				t.Fatal(err)
			}
			// 已有 Task 的成功结果和固定 ImageRef 应保留，不被旧轮请求注解覆盖。
			if sp.Status.LastTriggerRound != 2 || len(sp.Status.Snapshots) != 2 ||
				sp.Status.Snapshots[0].Result != snapshotv1.SnapshotRunningResultSuccess ||
				sp.Status.Snapshots[0].ImageRef != "registry.example/original:fixed" {
				t.Fatalf("unexpected runs: %+v", sp.Status)
			}
			var added snapshotv1.SnapshotPodTask
			if err := c.Get(t.Context(), types.NamespacedName{Namespace: "ns", Name: "snap-2-sidecar"}, &added); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(added.Spec.CommitImage, "registry.example/sidecar:v1-snapped-") || added.Spec.ContainerID != "containerd://sidecar" {
				t.Fatalf("unexpected task: %+v", added.Spec)
			}
		})
	}
}
