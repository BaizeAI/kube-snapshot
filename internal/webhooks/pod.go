package webhooks

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/samber/lo"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	snapshotpodv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
	snapshotpodpkgv1alpha1 "github.com/baizeai/kube-snapshot/pkg/apis/snapshotpod/v1alpha1"
)

// PodImageWebhookAdmission 在创建 Pod 时恢复快照镜像，并在删除时按配置触发自动保存。
type PodImageWebhookAdmission struct {
	client.Client
}

// imageConfig 将同一容器要恢复的镜像与所需拉取凭据一起传递给补丁生成逻辑。
type imageConfig struct {
	image      string
	pullSecret string
}

// SetupWebhookWithManager 注册 Pod 准入处理器，复用 manager 的客户端和 Webhook 服务。
func (p *PodImageWebhookAdmission) SetupWebhookWithManager(mgr manager.Manager) error {
	mgr.GetWebhookServer().Register("/mutate-v1-pod", &webhook.Admission{
		Handler:      p,
		RecoverPanic: lo.ToPtr(true),
	})
	return ctrl.NewWebhookManagedBy(mgr).For(&corev1.Pod{}).Complete()
}

// Handle 将创建事件转换为恢复补丁；删除事件仅尽力触发保存，不等待任务完成。
// 恢复凭据查询失败会返回错误，自动删除触发失败则记录原因并继续放行删除。
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

		// 已进入删除流程的 Pod 不再次触发，避免后续删除请求重复增加轮次。
		if !pod.DeletionTimestamp.IsZero() {
			return admission.Allowed("already handled")
		}

		round, err := p.handlePodUpdate(ctx, pod.Namespace, pod)
		if err != nil {
			klog.Warningf(
				"failed to protect snapshot round before deleting pod %s/%s; allowing deletion without triggering a new round: %v",
				pod.Namespace, pod.Name, err,
			)
		}
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
		patchMap, needPatch, err := p.handlePodCreateWithError(ctx, pod)
		if err != nil {
			return admission.Errored(500, err)
		}
		if !needPatch {
			return admission.Allowed("no need to patch")
		}
		var patchOps []jsonpatch.JsonPatchOperation
		// imagePullSecrets 属于 Pod；合并已有及本次新增凭据，避免多容器覆盖或重复追加。
		secrets := slices.Clone(pod.Spec.ImagePullSecrets)
		for containerName, ic := range patchMap {
			for i, container := range pod.Spec.Containers {
				if container.Name == containerName {
					patchOps = append(patchOps, jsonpatch.JsonPatchOperation{
						Operation: "replace",
						Path:      "/spec/containers/" + strconv.Itoa(i) + "/image",
						Value:     ic.image,
					})
					if ic.pullSecret != "" && !slices.Contains(secrets, corev1.LocalObjectReference{
						Name: ic.pullSecret,
					}) {
						// 空列表可能未序列化为字段，先创建数组；之后仅向 /- 追加。
						// 重复向 /spec/imagePullSecrets 执行 add 会覆盖先前容器的凭据。
						if len(secrets) == 0 {
							patchOps = append(patchOps, jsonpatch.JsonPatchOperation{
								Operation: "add",
								Path:      "/spec/imagePullSecrets",
								Value:     []corev1.LocalObjectReference{{Name: ic.pullSecret}},
							})
						} else {
							// 数组已存在，保留先前条目并追加当前容器所需凭据。
							patchOps = append(patchOps, jsonpatch.JsonPatchOperation{
								Operation: "add",
								Path:      "/spec/imagePullSecrets/-",
								Value:     corev1.LocalObjectReference{Name: ic.pullSecret},
							})
						}
						secrets = append(secrets, corev1.LocalObjectReference{Name: ic.pullSecret})
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

// handlePodUpdate 仅在启用自动保存且当前轮任务已准备齐全时递增轮次。
// 不清除最近手动请求注解；轮次递增后旧注解失配，控制器自然改用长期配置。
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
	ready, err := p.snapshotRoundReady(ctx, &sp, pod)
	if err != nil {
		return 0, err
	}
	if !ready {
		// 准备未完成时保留当前轮次，防止缺失容器的手动请求被自动轮次覆盖。
		klog.Infof(
			"snapshot round %d is still being prepared for pod %s/%s; allowing deletion without starting an automatic round",
			sp.Spec.TriggerRound, ns, pod.Name,
		)
		return 0, nil
	}

	sp.Spec.TriggerRound += 1
	err = p.Update(ctx, &sp)
	return sp.Spec.TriggerRound, err
}

// snapshotRoundReady 核对当前轮每个预期容器的真实任务和运行记录，不仅依赖 LastTriggerRound。
// 准备齐全不等于推送成功；此处不等待 commit/push，也不查询远端 tag。
func (p *PodImageWebhookAdmission) snapshotRoundReady(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod, pod *corev1.Pod) (bool, error) {
	if sp.Spec.TriggerRound <= 0 {
		return true, nil
	}

	var request *snapshotpodpkgv1alpha1.RoundRequest
	if raw, ok := sp.Annotations[snapshotpodpkgv1alpha1.RoundRequestAnnotation]; ok {
		decoded, err := snapshotpodpkgv1alpha1.DecodeRoundRequest(raw, sp.Spec.TriggerRound)
		if err != nil {
			return false, err
		}
		if decoded.Round == sp.Spec.TriggerRound {
			request = &decoded
		}
	}

	// 匹配的请求映射优先；旧调用仍按目标容器配置或待删除 Pod 的普通容器清单判断。
	var containers []string
	switch {
	case request != nil:
		containers = lo.Keys(request.Images)
	case len(sp.Spec.Target.Containers) > 0:
		containers = sp.Spec.Target.Containers
	default:
		containers = lo.Map(pod.Spec.Containers, func(container corev1.Container, _ int) string {
			return container.Name
		})
	}
	if len(containers) == 0 {
		return false, fmt.Errorf("snapshot round %d has no target containers", sp.Spec.TriggerRound)
	}

	tasks := snapshotpodv1alpha1.SnapshotPodTaskList{}
	if err := p.List(ctx, &tasks, client.InNamespace(sp.Namespace), client.MatchingLabels{
		snapshotpodpkgv1alpha1.SnapshotNameLabel: sp.Name,
	}); err != nil {
		return false, fmt.Errorf("list snapshot tasks before pod deletion: %w", err)
	}
	tasksByName := make(map[string]snapshotpodv1alpha1.SnapshotPodTask, len(tasks.Items))
	for _, task := range tasks.Items {
		tasksByName[task.Name] = task
	}
	for _, container := range containers {
		taskName := fmt.Sprintf("%s-%d-%s", sp.Name, sp.Spec.TriggerRound, container)
		task, ok := tasksByName[taskName]
		if !ok || !metav1.IsControlledBy(&task, sp) {
			return false, nil
		}
		if !lo.ContainsBy(sp.Status.Snapshots, func(run snapshotpodv1alpha1.SnapshotRunningStatus) bool {
			return run.TriggerRound == sp.Spec.TriggerRound && run.ContainerName == container && run.TaskName == taskName
		}) {
			return false, nil
		}
	}
	return true, nil
}

// handlePodCreateWithError 按容器选择恢复镜像，并将新协议的凭据读取错误传回准入入口。
// 返回映射以容器名为键；镜像沿用历史 ImageRef，不解析或改写其 tag。
func (p *PodImageWebhookAdmission) handlePodCreateWithError(ctx context.Context, pod *corev1.Pod) (map[string]imageConfig, bool, error) {
	snapPodList := snapshotpodv1alpha1.SnapshotPodList{}
	err := p.List(ctx, &snapPodList, client.InNamespace(pod.Namespace))
	if err != nil {
		// 保留旧路径对 SnapshotPod 列表读取失败的放行行为。
		return nil, false, nil
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
		return nil, false, nil
	}
	out := map[string]imageConfig{}
	switch {
	case sp.Spec.RecoveryRound > 0:
		// 指定轮次沿用原有选择语义，不增加成功状态过滤。
		for _, item := range sp.Status.Snapshots {
			if item.TriggerRound != sp.Spec.RecoveryRound {
				continue
			}
			pullSecret, err := p.restoreRegistrySecret(ctx, &sp, item)
			if err != nil {
				return nil, false, err
			}
			out[item.ContainerName] = imageConfig{image: item.ImageRef, pullSecret: pullSecret}
		}
	case sp.Spec.RecoveryRound == 0:
		// 每个容器独立选择最近成功记录，可能来自不同轮次和不同凭据。
		slices.SortFunc(sp.Status.Snapshots, func(a, b snapshotpodv1alpha1.SnapshotRunningStatus) int {
			return cmp.Compare(b.TriggerRound, a.TriggerRound)
		})
		for _, item := range sp.Status.Snapshots {
			cName := item.ContainerName
			if _, ok := out[cName]; ok {
				continue
			}
			if item.Result == snapshotpodv1alpha1.SnapshotRunningResultSuccess {
				pullSecret, err := p.restoreRegistrySecret(ctx, &sp, item)
				if err != nil {
					return nil, false, err
				}
				out[cName] = imageConfig{image: item.ImageRef, pullSecret: pullSecret}
			}
		}
	default:
		return nil, false, nil
	}
	return out, len(out) > 0, nil
}

// restoreRegistrySecret 只对新协议标记的任务读取其 RegistrySecretRef，旧任务沿用长期配置。
// 缺失任务时的回退仅是尽力恢复，无法保证与新快照的仓库凭据匹配。
func (p *PodImageWebhookAdmission) restoreRegistrySecret(
	ctx context.Context,
	sp *snapshotpodv1alpha1.SnapshotPod,
	run snapshotpodv1alpha1.SnapshotRunningStatus,
) (string, error) {
	legacySecret := sp.Spec.ImageSaveOptions.RegistrySecretRef
	if _, ok := sp.Annotations[snapshotpodpkgv1alpha1.RoundRequestAnnotation]; !ok {
		// 未接入新协议的对象不额外查询任务，保留旧调用方更新长期 Secret 后恢复的行为。
		return legacySecret, nil
	}
	if run.TaskName == "" {
		klog.Warningf(
			"snapshot run for container %s in round %d has no task name; falling back to the long-term registry credential for SnapshotPod %s/%s",
			run.ContainerName, run.TriggerRound, sp.Namespace, sp.Name,
		)
		return legacySecret, nil
	}
	task := snapshotpodv1alpha1.SnapshotPodTask{}
	if err := p.Get(ctx, client.ObjectKey{Namespace: sp.Namespace, Name: run.TaskName}, &task); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Warningf("snapshot task %s/%s is missing; falling back to the long-term registry credential", sp.Namespace, run.TaskName)
			return legacySecret, nil
		}
		// 认证、网络等非 NotFound 错误不能伪装成历史任务缺失并静默换用其他凭据。
		return "", fmt.Errorf("get snapshot task %s/%s for restore credentials: %w", sp.Namespace, run.TaskName, err)
	}
	if !metav1.IsControlledBy(&task, sp) {
		return "", fmt.Errorf("snapshot task %s/%s is not owned by SnapshotPod %s", task.Namespace, task.Name, sp.Name)
	}
	if task.Annotations[snapshotpodpkgv1alpha1.RestoreCredentialSourceAnnotation] == snapshotpodpkgv1alpha1.RestoreCredentialSourceTask {
		// 新任务中的显式空值表示不额外指定凭据，不能回退长期 Secret。
		return task.Spec.RegistrySecretRef, nil
	}
	return legacySecret, nil
}
