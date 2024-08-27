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
)

var (
	scheme            = runtime.NewScheme()
	registrySecretRef = "registry-secret"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(snapshotpodv1alpha1.AddToScheme(scheme))
}

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

func newFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func mustMarshalObject(obj runtime.Object) []byte {
	data, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return data
}

func newPodWithDeleteTime() *corev1.Pod {
	pod := newPod()
	pod.DeletionTimestamp = lo.ToPtr(metav1.NewTime(time.Now()))
	return pod
}

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
			name: "test handle delete event",
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
						Message: "new round: 3",
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
