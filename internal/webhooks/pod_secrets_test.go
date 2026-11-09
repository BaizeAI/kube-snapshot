package webhooks

import (
	"encoding/json"
	"slices"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	snapshotpodv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
	snapshotpkg "github.com/baizeai/kube-snapshot/pkg/apis/snapshotpod/v1alpha1"
)

// TestPodCreatePullSecrets 实际应用准入补丁，验证多容器凭据合并、去重及空引用过滤。
// 分别覆盖无凭据和已有凭据两条路径，不依赖 map 遍历导致的补丁顺序。
func TestPodCreatePullSecrets(t *testing.T) {
	for _, tt := range []struct {
		name     string
		existing []corev1.LocalObjectReference
		want     []string
	}{
		{"missing list", nil, []string{"secret-a", "secret-b"}},
		{"existing and duplicate", []corev1.LocalObjectReference{{Name: "existing"}, {Name: "secret-a"}}, []string{"existing", "secret-a", "secret-b"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sp := newSnapshotPod()
			sp.UID = "snapshot-uid"
			sp.Annotations = map[string]string{snapshotpkg.RoundRequestAnnotation: "present"}
			sp.Spec.RecoveryRound = 1
			sp.Status.Snapshots = nil
			pod := newPod()
			pod.Spec.Containers = nil
			pod.Spec.ImagePullSecrets = tt.existing
			objects := []client.Object{sp}
			// 重复的 secret-a 与空引用用于验证凭据去重和空值过滤。
			for i, secret := range []string{"secret-a", "secret-b", "secret-a", ""} {
				name := string(rune('a' + i))
				pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: name, Image: "old:tag"})
				sp.Status.Snapshots = append(sp.Status.Snapshots, snapshotpodv1alpha1.SnapshotRunningStatus{
					ContainerName: name, TriggerRound: 1, TaskName: name,
					ImageRef: "example.com/" + name + ":new",
				})
				objects = append(objects, &snapshotpodv1alpha1.SnapshotPodTask{
					ObjectMeta: metav1.ObjectMeta{
						Name: name, Namespace: sp.Namespace,
						OwnerReferences: []metav1.OwnerReference{
							*metav1.NewControllerRef(sp, snapshotpodv1alpha1.GroupVersion.WithKind("SnapshotPod")),
						},
						Annotations: map[string]string{
							snapshotpkg.RestoreCredentialSourceAnnotation: snapshotpkg.RestoreCredentialSourceTask,
						},
					},
					Spec: snapshotpodv1alpha1.SnapshotPodTaskSpec{
						RegistrySecretRef: secret,
					},
				})
			}
			raw, err := json.Marshal(pod)
			if err != nil {
				t.Fatal(err)
			}
			p := &PodImageWebhookAdmission{Client: newFakeClient(objects...)}
			resp := p.Handle(t.Context(), admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Create,
					Resource:  metav1.GroupVersionResource{Version: "v1", Resource: "pods"},
					Object:    runtime.RawExtension{Raw: raw},
				},
			})
			if !resp.Allowed {
				t.Fatalf("admission denied: %+v", resp.Result)
			}
			// 解码并实际执行 webhook 返回的 JSONPatch，验证最终 Pod，而非只检查操作列表。
			patchBytes, err := json.Marshal(resp.Patches)
			if err != nil {
				t.Fatal(err)
			}
			patch, err := jsonpatch.DecodePatch(patchBytes)
			if err != nil {
				t.Fatal(err)
			}
			patched, err := patch.Apply(raw)
			if err != nil {
				t.Fatalf("apply patch %s: %v", patchBytes, err)
			}
			var got corev1.Pod
			if err := json.Unmarshal(patched, &got); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, secret := range got.Spec.ImagePullSecrets {
				names = append(names, secret.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, tt.want) {
				t.Errorf("secrets = %v, want %v", names, tt.want)
			}
			for _, c := range got.Spec.Containers {
				if wantImage := "example.com/" + c.Name + ":new"; c.Image != wantImage {
					t.Errorf("container %s image = %q, want %q", c.Name, c.Image, wantImage)
				}
			}
		})
	}
}
