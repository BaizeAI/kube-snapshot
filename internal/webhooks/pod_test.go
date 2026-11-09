package webhooks

import (
	"context"
	"reflect"
	"testing"
	"time"

	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/json"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/google/go-cmp/cmp"
	"github.com/samber/lo"

	snapshotpodv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
	snapshotpodpkgv1alpha1 "github.com/baizeai/kube-snapshot/pkg/apis/snapshotpod/v1alpha1"
)

// scheme 和 registrySecretRef 是 webhook 测试共享的类型注册表与恢复凭据名。
var (
	scheme            = runtime.NewScheme()
	registrySecretRef = "registry-secret"
)

// init 注册 webhook 测试 fake client 所需的 Kubernetes 与 SnapshotPod 类型。
func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(snapshotpodv1alpha1.AddToScheme(scheme))
}

// newPod 构造不带拉取凭据的单容器 Pod fixture。
func newPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "notebook",
					Image: "jupyter:latest",
				},
			},
		},
	}
}

// newPod1 构造带有不同于恢复凭据的已有 pull secret 的 Pod fixture。
func newPod1() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test",
		},
		Spec: corev1.PodSpec{
			ImagePullSecrets: []corev1.LocalObjectReference{
				{
					Name: "test-secret",
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "notebook",
					Image: "jupyter:latest",
				},
			},
		},
	}
}

// newPod2 构造已包含恢复所需 registry secret 的 Pod fixture。
func newPod2() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test",
		},
		Spec: corev1.PodSpec{
			ImagePullSecrets: []corev1.LocalObjectReference{
				{
					Name: registrySecretRef,
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "notebook",
					Image: "jupyter:latest",
				},
			},
		},
	}
}

// newSnapshotPod 构造包含两条历史快照记录的 SnapshotPod fixture。
func newSnapshotPod() *snapshotpodv1alpha1.SnapshotPod {
	t1 := time.Now()
	t2 := t1.Add(time.Hour)
	return &snapshotpodv1alpha1.SnapshotPod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-snapshot-pod",
			Namespace: "test",
		},
		Spec: snapshotpodv1alpha1.SnapshotPodSpec{
			Target: snapshotpodv1alpha1.TargetPod{
				Name: "test-pod",
			},
			ImageSaveOptions: snapshotpodv1alpha1.ImageSaveOptions{
				ImageRefFormat:    "release.daocloud.io/test/{last_repository}",
				RegistrySecretRef: registrySecretRef,
			},
			TriggerRound:  2,
			RecoveryRound: 0,
		},
		Status: snapshotpodv1alpha1.SnapshotPodStatus{
			Snapshots: []snapshotpodv1alpha1.SnapshotRunningStatus{
				{
					ContainerName: "notebook",
					TriggerRound:  0,
					FailedReason:  "can not fetch task",
					ImageRef:      "release.daocloud.io/test/jupytor:round-1",
					Result:        "SUCCEED",
					TriggerTime:   metav1.NewTime(t1),
				},
				{
					ContainerName: "notebook",
					TriggerRound:  1,
					FailedReason:  "can not fetch task",
					ImageRef:      "release.daocloud.io/test/jupytor:round-2",
					Result:        "FAILED",
					TriggerTime:   metav1.NewTime(t2),
				},
			},
		},
	}
}

// newFakeClient 使用本测试共享 Scheme 创建 fake client。
func newFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// mustMarshalObject 将对象编码为 admission 请求的原始 JSON，编码失败时立即终止测试。
func mustMarshalObject(obj runtime.Object) []byte {
	data, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return data
}

// newPodWithDeleteTime 构造已有删除时间戳的 Pod，用于覆盖重复 DELETE 事件。
func newPodWithDeleteTime() *corev1.Pod {
	pod := newPod()
	pod.DeletionTimestamp = lo.ToPtr(metav1.NewTime(time.Now()))
	return pod
}

// TestPodDeleteTriggersReadyRound checks that automatic saving advances only after the current tasks are recorded.
// DELETE 只确认当轮 Task 已记录便推进准备轮次，不等待镜像 commit 或 push 完成。
func TestPodDeleteTriggersReadyRound(t *testing.T) {
	sp := newSnapshotPod()
	sp.UID = "snapshot-uid"
	sp.Spec.AutoSaveOptions.AutoSaveOnTermination = true
	sp.Status.Snapshots = append(sp.Status.Snapshots, snapshotpodv1alpha1.SnapshotRunningStatus{
		TriggerRound: 2, ContainerName: "notebook", TaskName: "test-snapshot-pod-2-notebook",
	})
	task := &snapshotpodv1alpha1.SnapshotPodTask{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-snapshot-pod-2-notebook",
			Namespace: sp.Namespace,
			Labels:    map[string]string{snapshotpodpkgv1alpha1.SnapshotNameLabel: sp.Name},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(sp, snapshotpodv1alpha1.GroupVersion.WithKind("SnapshotPod")),
			},
		},
	}
	cli := newFakeClient(sp, task)
	p := &PodImageWebhookAdmission{Client: cli}
	round, err := p.handlePodUpdate(t.Context(), sp.Namespace, newPod1())
	if err != nil || round != 3 {
		t.Fatalf("handlePodUpdate returned round %d, error %v", round, err)
	}
	updated := &snapshotpodv1alpha1.SnapshotPod{}
	if err := cli.Get(t.Context(), client.ObjectKeyFromObject(sp), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.TriggerRound != 3 {
		t.Fatalf("trigger round = %d, want 3", updated.Spec.TriggerRound)
	}
}

// TestPodImageWebhookAdmission_Handle 验证 Pod 创建与删除请求的准入响应和恢复补丁。
func TestPodImageWebhookAdmission_Handle(t *testing.T) {
	type fields struct {
		Client client.Client
	}
	type args struct {
		request admission.Request
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   admission.Response
	}{
		{
			name: "create pod",
			fields: fields{
				Client: newFakeClient(newSnapshotPod()),
			},
			args: args{
				request: admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						Operation: admissionv1.Create,
						UID:       "test-uid",
						Object: runtime.RawExtension{
							Raw: mustMarshalObject(newPod()),
						},
						Resource: metav1.GroupVersionResource{
							Group:    "",
							Version:  "v1",
							Resource: "pods",
						},
					},
				},
			},
			want: admission.Response{
				Patches: []jsonpatch.Operation{
					{
						Operation: "replace",
						Path:      "/spec/containers/0/image",
						Value:     "release.daocloud.io/test/jupytor:round-1",
					},
					{
						Operation: "add",
						Path:      "/spec/imagePullSecrets",
						Value:     []corev1.LocalObjectReference{{Name: registrySecretRef}},
					},
				},
				AdmissionResponse: admissionv1.AdmissionResponse{
					UID:     "test-uid",
					Allowed: true,
				},
			},
		},
		{
			name: "create pod with a pull secret",
			fields: fields{
				Client: newFakeClient(newSnapshotPod()),
			},
			args: args{
				request: admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						Operation: admissionv1.Create,
						UID:       "test-uid",
						Object: runtime.RawExtension{
							Raw: mustMarshalObject(newPod1()),
						},
						Resource: metav1.GroupVersionResource{
							Group:    "",
							Version:  "v1",
							Resource: "pods",
						},
					},
				},
			},
			want: admission.Response{
				Patches: []jsonpatch.Operation{
					{
						Operation: "replace",
						Path:      "/spec/containers/0/image",
						Value:     "release.daocloud.io/test/jupytor:round-1",
					},
					{
						Operation: "add",
						Path:      "/spec/imagePullSecrets/-",
						Value:     corev1.LocalObjectReference{Name: "registry-secret"},
					},
				},
				AdmissionResponse: admissionv1.AdmissionResponse{
					UID:     "test-uid",
					Allowed: true,
				},
			},
		},
		{
			name: "create pod with same pull secret",
			fields: fields{
				Client: newFakeClient(newSnapshotPod()),
			},
			args: args{
				request: admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						Operation: admissionv1.Create,
						UID:       "test-uid",
						Object: runtime.RawExtension{
							Raw: mustMarshalObject(newPod2()),
						},
						Resource: metav1.GroupVersionResource{
							Group:    "",
							Version:  "v1",
							Resource: "pods",
						},
					},
				},
			},
			want: admission.Response{
				Patches: []jsonpatch.Operation{
					{
						Operation: "replace",
						Path:      "/spec/containers/0/image",
						Value:     "release.daocloud.io/test/jupytor:round-1",
					},
				},
				AdmissionResponse: admissionv1.AdmissionResponse{
					UID:     "test-uid",
					Allowed: true,
				},
			},
		},
		{
			// 当前轮准备未齐时仍放行删除，但不递增轮次；准入阶段不等待镜像推送。
			name: "test handle delete event while current round is incomplete",
			fields: fields{
				Client: newFakeClient(func() client.Object {
					pod := newSnapshotPod()
					pod.Spec.AutoSaveOptions.AutoSaveOnTermination = true
					return pod
				}()),
			},
			args: args{
				request: admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						Operation: admissionv1.Delete,
						UID:       "test-uid",
						OldObject: runtime.RawExtension{
							Raw: mustMarshalObject(newPod1()),
						},
						Resource: metav1.GroupVersionResource{
							Group:    "",
							Version:  "v1",
							Resource: "pods",
						},
					},
				},
			},
			want: admission.Response{
				AdmissionResponse: admissionv1.AdmissionResponse{
					Allowed: true,
					Result: &metav1.Status{
						Message: "new round: 0",
						Code:    200,
					},
				},
			},
		},
		{
			name: "test disable auto save",
			fields: fields{
				Client: newFakeClient(newSnapshotPod()),
			},
			args: args{
				request: admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						Operation: admissionv1.Delete,
						UID:       "test-uid",
						OldObject: runtime.RawExtension{
							Raw: mustMarshalObject(newPod1()),
						},
						Resource: metav1.GroupVersionResource{
							Group:    "",
							Version:  "v1",
							Resource: "pods",
						},
					},
				},
			},
			want: admission.Response{
				AdmissionResponse: admissionv1.AdmissionResponse{
					Allowed: true,
					Result: &metav1.Status{
						Message: "new round: 0",
						Code:    200,
					},
				},
			},
		},
		{
			name: "test event already handled",
			fields: fields{
				Client: newFakeClient(newSnapshotPod()),
			},
			args: args{
				request: admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						Operation: admissionv1.Delete,
						UID:       "test-uid",
						OldObject: runtime.RawExtension{
							Raw: mustMarshalObject(newPodWithDeleteTime()),
						},
						Resource: metav1.GroupVersionResource{
							Group:    "",
							Version:  "v1",
							Resource: "pods",
						},
					},
				},
			},
			want: admission.Response{
				AdmissionResponse: admissionv1.AdmissionResponse{
					Allowed: true,
					Result: &metav1.Status{
						Message: "already handled",
						Code:    200,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &PodImageWebhookAdmission{
				Client: tt.fields.Client,
			}
			if got := p.Handle(context.TODO(), tt.args.request); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Got different than expected: %s", cmp.Diff(got, tt.want))
			}
		})
	}
}
