package webhooks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/samber/lo"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	snapshotpodv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
)

type PodImageWebhookAdmission struct {
	client.Client
}

type imageConfig struct {
	image      string
	pullSecret string
}

func (p *PodImageWebhookAdmission) SetupWebhookWithManager(mgr manager.Manager) error {
	mgr.GetWebhookServer().Register("/mutate-v1-pod", &webhook.Admission{
		Handler:      p,
		RecoverPanic: true,
	})
	return ctrl.NewWebhookManagedBy(mgr).For(&corev1.Pod{}).Complete()
}

func (p *PodImageWebhookAdmission) Handle(ctx context.Context, request admission.Request) admission.Response {
	podGVR := metav1.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}
	if request.Resource != podGVR {
		return admission.Allowed("not pod")
	}
	switch request.Operation {
	case admissionv1.Delete:
		pod := &corev1.Pod{}
		err := json.Unmarshal(request.OldObject.Raw, pod)
		if err != nil {
			return admission.Errored(500, err)
		}

		// avoid handling multiple pod deletion events. when pod deleted, webhook will send 3 times events, first event no delete timestamp.
		// see https://www.qinglite.cn/doc/527564767afc0aac0.
		if !pod.DeletionTimestamp.IsZero() {
			return admission.Allowed("already handled")
		}

		round, _ := p.handlePodUpdate(ctx, pod.Namespace, pod)
		if round != 0 {
			klog.Infof("trigger new round %d for pod deleting %s/%s", round, pod.Namespace, pod.Name)
		}
		return admission.Allowed(fmt.Sprintf("new round: %d", round))
	case admissionv1.Create:
		pod := &corev1.Pod{}
		err := json.Unmarshal(request.Object.Raw, pod)
		if err != nil {
			return admission.Errored(500, err)
		}
		patchMap, needPatch := p.handlePodCreate(ctx, pod)
		if !needPatch {
			return admission.Allowed("no need to patch")
		}
		var patchOps []jsonpatch.JsonPatchOperation
		for containerName, ic := range patchMap {
			for i, container := range pod.Spec.Containers {
				if container.Name == containerName {
					patchOps = append(patchOps, jsonpatch.JsonPatchOperation{
						Operation: "replace",
						Path:      "/spec/containers/" + strconv.Itoa(i) + "/image",
						Value:     ic.image,
					})
					if ic.pullSecret != "" && !lo.Contains(pod.Spec.ImagePullSecrets, corev1.LocalObjectReference{
						Name: ic.pullSecret,
					}) {
						// as issue https://github.com/kubernetes/kubernetes/issues/70281 mentioned
						// json patch distinguish nil and empty array.
						if len(pod.Spec.ImagePullSecrets) == 0 {
							patchOps = append(patchOps, jsonpatch.JsonPatchOperation{
								Operation: "add",
								Path:      "/spec/imagePullSecrets",
								Value:     []corev1.LocalObjectReference{{Name: ic.pullSecret}},
							})
						} else {
							// need append secret
							patchOps = append(patchOps, jsonpatch.JsonPatchOperation{
								Operation: "add",
								Path:      "/spec/imagePullSecrets/-",
								Value:     corev1.LocalObjectReference{Name: ic.pullSecret},
							})
						}
					}
				}
			}
		}
		klog.Infof("patched pod %s/%s for create: %+v", pod.Namespace, pod.Name, patchOps)
		return admission.Response{
			Patches: patchOps,
			AdmissionResponse: admissionv1.AdmissionResponse{
				UID:     request.UID,
				Allowed: true,
			},
		}
	}
	return admission.Allowed("allowed")
}

func (p *PodImageWebhookAdmission) handlePodUpdate(ctx context.Context, ns string, pod *corev1.Pod) (int32, error) {
	snapPodList := snapshotpodv1alpha1.SnapshotPodList{}
	err := p.List(ctx, &snapPodList, client.InNamespace(ns))
	if err != nil {
		return 0, err
	}
	// todo more than one sp?
	sp, ok := lo.Find(snapPodList.Items, func(item snapshotpodv1alpha1.SnapshotPod) bool {
		if item.Spec.Target.Name != "" {
			return item.Spec.Target.Name == pod.Name
		}
		return item.Spec.Target.Selector != nil && pod.Labels != nil &&
			lo.EveryBy(lo.Entries(item.Spec.Target.Selector), func(entry lo.Entry[string, string]) bool {
				return pod.Labels[entry.Key] == entry.Value
			})
	})
	if !ok {
		return 0, nil
	}

	if !sp.Spec.AutoSaveOptions.AutoSaveOnTermination {
		klog.Infof("no auto save for pod %s/%s", ns, pod.Name)
		return 0, nil
	}

	sp.Spec.TriggerRound += 1
	err = p.Update(ctx, &sp)
	return sp.Spec.TriggerRound, err
}

// handlePodCreate to check whether to alter pods image and imagePullSecret.
// map key is container name, value is imageConfig.
func (p *PodImageWebhookAdmission) handlePodCreate(ctx context.Context, pod *corev1.Pod) (map[string]imageConfig, bool) {
	snapPodList := snapshotpodv1alpha1.SnapshotPodList{}
	err := p.List(ctx, &snapPodList, client.InNamespace(pod.Namespace))
	if err != nil {
		return nil, false
	}
	// TODO(@kebe): more than one sp?
	// find all snapshot pods and match the container images.
	sp, ok := lo.Find(snapPodList.Items, func(item snapshotpodv1alpha1.SnapshotPod) bool {
		if item.Spec.Target.Name != "" {
			return item.Spec.Target.Name == pod.Name
		}
		return item.Spec.Target.Selector != nil && pod.Labels != nil &&
			lo.EveryBy(lo.Entries(item.Spec.Target.Selector), func(entry lo.Entry[string, string]) bool {
				return pod.Labels[entry.Key] == entry.Value
			})
	})
	if !ok {
		return nil, false
	}
	out := map[string]imageConfig{}
	switch {
	case sp.Spec.RecoveryRound > 0:
		lo.ForEach(lo.Filter(sp.Status.Snapshots, func(item snapshotpodv1alpha1.SnapshotRunningStatus, index int) bool {
			return item.TriggerRound == sp.Spec.RecoveryRound
		}), func(item snapshotpodv1alpha1.SnapshotRunningStatus, index int) {
			out[item.ContainerName] = imageConfig{
				image:      item.ImageRef,
				pullSecret: sp.Spec.ImageSaveOptions.RegistrySecretRef,
			}
		})
	case sp.Spec.RecoveryRound == 0:
		sort.Slice(sp.Status.Snapshots, func(i, j int) bool {
			return sp.Status.Snapshots[i].TriggerRound > sp.Status.Snapshots[j].TriggerRound
		})
		lo.ForEach(sp.Status.Snapshots, func(item snapshotpodv1alpha1.SnapshotRunningStatus, index int) {
			cName := item.ContainerName
			if _, ok := out[cName]; ok {
				return
			}
			if item.Result == snapshotpodv1alpha1.SnapshotRunningResultSuccess {
				out[cName] = imageConfig{
					image:      item.ImageRef,
					pullSecret: sp.Spec.ImageSaveOptions.RegistrySecretRef,
				}
			}
		})
	default:
		return nil, false
	}
	return out, len(out) > 0
}
