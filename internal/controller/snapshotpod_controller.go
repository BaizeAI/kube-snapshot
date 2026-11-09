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

package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/docker/reference"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	snapshotpodv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
	"github.com/baizeai/kube-snapshot/internal/metrics"
	snapshotpodpkgv1alpha1 "github.com/baizeai/kube-snapshot/pkg/apis/snapshotpod/v1alpha1"
	"github.com/baizeai/kube-snapshot/pkg/imagetag"
)

// SnapshotPodReconciler 按触发轮次创建各容器的快照任务，并汇总任务执行结果。
type SnapshotPodReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// getPod 优先按名称读取目标；使用标签选择器时，必须唯一匹配一个 Running Pod。
func (r *SnapshotPodReconciler) getPod(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod) (*corev1.Pod, error) {
	if sp.Spec.Target.Name == "" && sp.Spec.Target.Selector == nil {
		return nil, fmt.Errorf("target pod name or selector is required")
	}
	if sp.Spec.Target.Name != "" {
		pod := corev1.Pod{}
		err := r.Get(ctx, client.ObjectKey{
			Namespace: sp.Namespace,
			Name:      sp.Spec.Target.Name,
		}, &pod)
		return &pod, err
	}
	if sp.Spec.Target.Selector != nil {
		podList := corev1.PodList{}
		err := r.List(ctx, &podList, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(sp.Spec.Target.Selector),
			Namespace:     sp.Namespace,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list pods: %w", err)
		}

		var runningPods []corev1.Pod
		for _, pod := range podList.Items {
			if pod.Status.Phase == corev1.PodRunning {
				runningPods = append(runningPods, pod)
			}
		}
		switch len(runningPods) {
		case 0:
			return nil, fmt.Errorf("no running pod found with selector %v", sp.Spec.Target.Selector)
		case 1:
			return &runningPods[0], nil
		default:
			podNames := make([]string, 0, len(runningPods))
			for _, pod := range runningPods {
				podNames = append(podNames, pod.Name)
			}
			return nil, fmt.Errorf("multiple running pods found with selector %v: %v",
				sp.Spec.Target.Selector, strings.Join(podNames, ", "))
		}
	}
	return nil, fmt.Errorf("target selector not specified")
}

// selectedContainers 优先使用本轮请求固定的容器清单，否则沿用旧配置的选择范围。
// 只有需要从 Pod 推导全部普通容器时，调用方才必须提供非 nil 的 pod。
func selectedContainers(pod *corev1.Pod, sp *snapshotpodv1alpha1.SnapshotPod, request *snapshotpodpkgv1alpha1.RoundRequest) []string {
	if request != nil {
		// 排序消除 map 遍历顺序的不确定性，使补建任务及记录状态的顺序稳定。
		return slices.Sorted(maps.Keys(request.Images))
	}
	if len(sp.Spec.Target.Containers) != 0 {
		return slices.Clone(sp.Spec.Target.Containers)
	}
	return lo.Map(pod.Spec.Containers, func(item corev1.Container, _ int) string {
		return item.Name
	})
}

// snapshotPodOwner 为任务建立控制器归属，用于所有权校验及随 SnapshotPod 级联删除。
func snapshotPodOwner(s *snapshotpodv1alpha1.SnapshotPod) metav1.OwnerReference {
	return *metav1.NewControllerRef(s, snapshotpodv1alpha1.GroupVersion.WithKind("SnapshotPod"))
}

// reconcileTasks 复用本轮已有任务，先准备所有缺失任务的输入，再逐个补建并记录实际镜像。
// LastTriggerRound 仅表示任务准备完成，不表示 commit 或 push 已成功。
func (r *SnapshotPodReconciler) reconcileTasks(
	ctx context.Context,
	sp *snapshotpodv1alpha1.SnapshotPod,
	request *snapshotpodpkgv1alpha1.RoundRequest,
	pod *corev1.Pod,
) error {
	if pod == nil && request == nil && len(sp.Spec.Target.Containers) == 0 {
		// 旧协议若未显式选择容器，Pod 删除后就无法还原预期清单。
		// 此时只对已标记准备完成的轮次沿用旧行为；执行结果仍由 TaskStatus 同步。
		if sp.Status.LastTriggerRound == sp.Spec.TriggerRound {
			return nil
		}
		return fmt.Errorf("target Pod was not loaded for trigger round %d; container scope is unknown", sp.Spec.TriggerRound)
	}
	containers := selectedContainers(pod, sp, request)

	taskList := snapshotpodv1alpha1.SnapshotPodTaskList{}
	if err := r.List(ctx, &taskList, client.InNamespace(sp.Namespace), client.MatchingLabels{
		snapshotpodpkgv1alpha1.SnapshotNameLabel: sp.Name,
	}); err != nil {
		return fmt.Errorf("list snapshot tasks: %w", err)
	}
	tasks := make(map[string]snapshotpodv1alpha1.SnapshotPodTask, len(taskList.Items))
	for _, task := range taskList.Items {
		tasks[task.Name] = task
	}

	type pendingTask struct {
		container string
		image     string
		status    corev1.ContainerStatus
	}
	missing := make([]pendingTask, 0, len(containers))
	var missingPod bool
	for _, container := range containers {
		name := fmt.Sprintf("%s-%d-%s", sp.Name, sp.Spec.TriggerRound, container)
		if task, ok := tasks[name]; ok {
			// 已有任务的输入已固定，恢复状态不再依赖源 Pod 或容器仍然存在。
			if !metav1.IsControlledBy(&task, sp) {
				return fmt.Errorf("snapshot task %s/%s is not owned by SnapshotPod %s", task.Namespace, task.Name, sp.Name)
			}
			if request != nil {
				// 同轮请求与真实任务冲突时显式报错，避免将其他目标误记为本次请求已执行。
				if task.Spec.CommitImage != request.Images[container] {
					return fmt.Errorf("snapshot task %s already targets %s, not requested image %s", task.Name, task.Spec.CommitImage, request.Images[container])
				}
				if task.Annotations[snapshotpodpkgv1alpha1.RestoreCredentialSourceAnnotation] != snapshotpodpkgv1alpha1.RestoreCredentialSourceTask {
					return fmt.Errorf("snapshot task %s lacks round-specific credential metadata", task.Name)
				}
			}
			upsertSnapshotRun(sp, container, task)
			continue
		}
		if pod == nil {
			missingPod = true
			continue
		}
		if !slices.ContainsFunc(pod.Spec.Containers, func(item corev1.Container) bool {
			return item.Name == container
		}) {
			return fmt.Errorf("container %q not found in pod %s/%s", container, pod.Namespace, pod.Name)
		}
		status, ok := lo.Find(pod.Status.ContainerStatuses, func(item corev1.ContainerStatus) bool {
			return item.Name == container
		})
		if !ok {
			return fmt.Errorf("container status of %s/%s/%s not found", pod.Namespace, pod.Name, container)
		}
		if status.ContainerID == "" {
			return fmt.Errorf("container %s not started", container)
		}

		var image string
		if request != nil {
			// 本轮请求已固定最终地址；仅校验而不重新命名，保证与调用方预检使用相同目标。
			image = request.Images[container]
			parsed, err := docker.ParseReference("//" + image)
			if err != nil {
				return fmt.Errorf("invalid requested image for container %s: %w", container, err)
			}
			if _, ok := parsed.DockerReference().(reference.NamedTagged); !ok {
				return fmt.Errorf("requested image for container %s must include a tag", container)
			}
		} else {
			// 没有本轮请求时沿用长期模板；只有模板包含 {tag} 才会继承源容器镜像的版本。
			appendSuffix := true
			if sp.Spec.ImageSaveOptions.AppendSnappedSuffix != nil {
				appendSuffix = *sp.Spec.ImageSaveOptions.AppendSnappedSuffix
			}
			var err error
			image, err = imagetag.ResolveImageRef(status.Image, sp.Spec.ImageSaveOptions.ImageRefFormat, imagetag.Options{
				ContainerCount:      len(containers),
				AppendSnappedSuffix: appendSuffix,
			})
			if err != nil {
				return fmt.Errorf("resolve snapshot image for container %s: %w", container, err)
			}
		}
		missing = append(missing, pendingTask{container: container, image: image, status: status})
	}
	if missingPod {
		return fmt.Errorf("target Pod was not loaded for trigger round %d; missing tasks cannot be created", sp.Spec.TriggerRound)
	}

	// 所有缺失容器的输入齐备后才开始创建，避免输入错误造成不必要的半成品轮次。
	// API 创建中途失败仍可能留下部分任务，下次调谐会复用它们并补齐其余任务。
	for _, pending := range missing {
		container := pending.container
		annotations := map[string]string(nil)
		if request != nil {
			annotations = maps.Clone(request.TaskAnnotations[container])
			if annotations == nil {
				annotations = make(map[string]string)
			}
			// 恢复时凭此标记读取任务中的 Secret 引用；未标记的旧任务仍使用长期配置。
			annotations[snapshotpodpkgv1alpha1.RestoreCredentialSourceAnnotation] = snapshotpodpkgv1alpha1.RestoreCredentialSourceTask
		}
		secretRef := sp.Spec.ImageSaveOptions.RegistrySecretRef
		if request != nil {
			// 协议入口已校验指针非 nil；显式空字符串表示本轮不指定凭据，不回退长期值。
			secretRef = *request.RegistrySecretRef
		}
		task := snapshotpodv1alpha1.SnapshotPodTask{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: sp.Namespace,
				Name:      fmt.Sprintf("%s-%d-%s", sp.Name, sp.Spec.TriggerRound, container),
				OwnerReferences: []metav1.OwnerReference{
					snapshotPodOwner(sp),
				},
				Labels: map[string]string{
					snapshotpodpkgv1alpha1.SnapshotNameLabel: sp.Name,
				},
				Annotations: annotations,
			},
			Spec: snapshotpodv1alpha1.SnapshotPodTaskSpec{
				NodeName:          pod.Spec.NodeName,
				PodName:           pod.Name,
				ContainerID:       pending.status.ContainerID,
				CommitOptions:     sp.Spec.CommitOptions,
				OriginImage:       pending.status.Image,
				CommitImage:       pending.image,
				RegistrySecretRef: secretRef,
				MaxRetries:        sp.Spec.ImageSaveOptions.MaxRetries,
				RetryDelaySeconds: sp.Spec.ImageSaveOptions.RetryDelaySeconds,
			},
		}
		if err := r.Create(ctx, &task); err != nil {
			if !errors.IsAlreadyExists(err) {
				return err
			}
			// List 缓存可能尚未看到任务；读取已存在的真实对象，避免记录未被采用的本地输入。
			if err := r.Get(ctx, client.ObjectKeyFromObject(&task), &task); err != nil {
				return fmt.Errorf("get existing snapshot task %s: %w", task.Name, err)
			}
		}
		if !metav1.IsControlledBy(&task, sp) {
			return fmt.Errorf("snapshot task %s/%s is not owned by SnapshotPod %s", task.Namespace, task.Name, sp.Name)
		}
		if request != nil &&
			(task.Spec.CommitImage != pending.image ||
				task.Annotations[snapshotpodpkgv1alpha1.RestoreCredentialSourceAnnotation] != snapshotpodpkgv1alpha1.RestoreCredentialSourceTask) {
			return fmt.Errorf("existing snapshot task %s does not match the current round request", task.Name)
		}
		tasks[task.Name] = task
		upsertSnapshotRun(sp, container, task)
	}

	sp.Status.Snapshots = compactSnapshotRuns(sp.Status.Snapshots, sp.Spec.TriggerRound)
	// 仅在所有预期任务均存在且 runs 已补齐后推进标记，随本次 Status.Update 一起持久化。
	sp.Status.LastTriggerRound = sp.Spec.TriggerRound
	return nil
}

// upsertSnapshotRun 按本轮轮次和容器更新记录，镜像始终取真实任务的 CommitImage。
// 任务与镜像未变化时保留原结果和时间，避免重复调谐将终态重置为 CREATED。
func upsertSnapshotRun(sp *snapshotpodv1alpha1.SnapshotPod, container string, task snapshotpodv1alpha1.SnapshotPodTask) {
	for index := range sp.Status.Snapshots {
		run := &sp.Status.Snapshots[index]
		if run.TriggerRound != sp.Spec.TriggerRound || run.ContainerName != container {
			continue
		}
		if run.TaskName == task.Name && run.ImageRef == task.Spec.CommitImage {
			return
		}
		*run = snapshotpodv1alpha1.SnapshotRunningStatus{
			TriggerRound:  sp.Spec.TriggerRound,
			TriggerTime:   metav1.Now(),
			TaskName:      task.Name,
			ImageRef:      task.Spec.CommitImage,
			ContainerName: container,
			Result:        snapshotpodv1alpha1.SnapshotRunningResultCreated,
		}
		return
	}
	sp.Status.Snapshots = append(sp.Status.Snapshots, snapshotpodv1alpha1.SnapshotRunningStatus{
		TriggerRound:  sp.Spec.TriggerRound,
		TriggerTime:   metav1.Now(),
		TaskName:      task.Name,
		ImageRef:      task.Spec.CommitImage,
		ContainerName: container,
		Result:        snapshotpodv1alpha1.SnapshotRunningResultCreated,
	})
}

// compactSnapshotRuns 超过 20 条记录时保留末尾最多 5 条历史记录及当前轮全部记录。
// 当前轮容器较多时允许超过历史裁剪阈值，避免丢失本轮任务准备完整性的判断依据。
func compactSnapshotRuns(runs []snapshotpodv1alpha1.SnapshotRunningStatus, currentRound int32) []snapshotpodv1alpha1.SnapshotRunningStatus {
	if len(runs) <= 20 {
		return runs
	}
	history := lo.Filter(runs, func(run snapshotpodv1alpha1.SnapshotRunningStatus, _ int) bool {
		return run.TriggerRound != currentRound
	})
	current := lo.Filter(runs, func(run snapshotpodv1alpha1.SnapshotRunningStatus, _ int) bool {
		return run.TriggerRound == currentRound
	})
	if len(history) > 5 {
		history = history[len(history)-5:]
	}
	return append(history, current...)
}

// reconcileTasksStatus 从同命名空间的任务同步已有运行记录，无需读取源 Pod。
// 只有任务进入最终 Completed 或 Failed 阶段才记录终态，不将准备完成视为保存成功。
func (r *SnapshotPodReconciler) reconcileTasksStatus(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod) error {
	sptList := snapshotpodv1alpha1.SnapshotPodTaskList{}
	if err := r.List(ctx, &sptList, client.InNamespace(sp.Namespace), client.MatchingLabels{
		snapshotpodpkgv1alpha1.SnapshotNameLabel: sp.Name,
	}); err != nil {
		return err
	}
	sptMap := lo.SliceToMap(sptList.Items, func(item snapshotpodv1alpha1.SnapshotPodTask) (string, snapshotpodv1alpha1.SnapshotPodTask) {
		return item.Name, item
	})
	for i, s := range sp.Status.Snapshots {
		switch s.Result {
		case snapshotpodv1alpha1.SnapshotRunningResultSuccess, snapshotpodv1alpha1.SnapshotRunningResultFailed:
			continue
		}
		spt, ok := sptMap[s.TaskName]
		if !ok {
			sp.Status.Snapshots[i].Result = snapshotpodv1alpha1.SnapshotRunningResultCreated
			sp.Status.Snapshots[i].FailedReason = "can not fetch task"
			continue
		}
		switch spt.Status.Phase {
		case snapshotpodv1alpha1.SnapshotPodTaskPhaseCompleted:
			sp.Status.Snapshots[i].Result = snapshotpodv1alpha1.SnapshotRunningResultSuccess
			sp.Status.Snapshots[i].CompletedAt = lo.ToPtr(metav1.Now())
		case snapshotpodv1alpha1.SnapshotPodTaskPhaseFailed:
			sp.Status.Snapshots[i].Result = snapshotpodv1alpha1.SnapshotRunningResultFailed
			msgs := []string{}
			for _, cond := range spt.Status.Conditions {
				if cond.Status != metav1.ConditionTrue {
					msgs = append(msgs, cond.Message)
				}
			}
			sp.Status.Snapshots[i].FailedReason = strings.Join(msgs, ", ")
		}
	}
	return nil
}

// +kubebuilder:rbac:groups=snapshot-pod.baizeai.io,resources=snapshotpods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapshot-pod.baizeai.io,resources=snapshotpods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=snapshot-pod.baizeai.io,resources=snapshotpods/finalizers,verbs=update

// Reconcile 依次执行请求接纳、任务准备及结果同步，并将阶段错误写入 Conditions。
// 准备失败时仍同步已有任务结果，后续调谐再尝试补齐缺失任务。
func (r *SnapshotPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	logger := log.FromContext(ctx)

	sp := snapshotpodv1alpha1.SnapshotPod{}
	err := r.Get(ctx, req.NamespacedName, &sp)
	if err != nil {
		logger.Error(err, "get instance error")
		metrics.RecordControllerError("snapshotpod", "get_instance_error")
		metrics.RecordControllerReconcile("snapshotpod", metrics.ControllerResultError, time.Since(startTime))
		return ctrl.Result{}, err
	}
	if sp.Spec.TriggerRound <= 0 {
		return ctrl.Result{}, nil
	}
	var roundRequest *snapshotpodpkgv1alpha1.RoundRequest
	var roundRequestErr error
	// 注解在入口解码一次，仅匹配当前轮次时生效。旧轮次继续走长期配置；
	// 损坏或超前的请求保留错误交给 Accept 阶段，不能静默切换目标。
	if raw, ok := sp.Annotations[snapshotpodpkgv1alpha1.RoundRequestAnnotation]; ok {
		request, decodeErr := snapshotpodpkgv1alpha1.DecodeRoundRequest(raw, sp.Spec.TriggerRound)
		if decodeErr != nil {
			roundRequestErr = decodeErr
		} else if request.Round == sp.Spec.TriggerRound {
			roundRequest = &request
		}
	}
	var targetPod *corev1.Pod
	var targetPodErr error
	type reconciler func(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod) error
	type rec struct {
		typ string
		r   reconciler
	}
	recs := []rec{
		{
			typ: "Accept",
			r: func(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod) error {
				if roundRequestErr != nil {
					return roundRequestErr
				}
				targetPod, targetPodErr = r.getPod(ctx, sp)
				if targetPodErr != nil {
					// 源 Pod 不可用不直接拒绝整轮，由 Tasks 判断是否确实还需要它补建任务。
					targetPod = nil
				}
				return nil
			},
		},
		{
			typ: "Tasks",
			r: func(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod) error {
				err := r.reconcileTasks(ctx, sp, roundRequest, targetPod)
				if err != nil && targetPodErr != nil {
					return fmt.Errorf("%w: %v", err, targetPodErr)
				}
				return err
			},
		},
		{
			typ: "TaskStatus",
			r:   r.reconcileTasksStatus,
		},
	}
	failed := false
	for _, rec := range recs {
		// 请求无效时不创建任务，但不能阻断已有任务的结果同步。
		if failed && rec.typ != "TaskStatus" {
			continue
		}
		_, i, ok := lo.FindIndexOf(sp.Status.Conditions, func(item metav1.Condition) bool {
			return item.Type == rec.typ
		})
		if !ok {
			c := metav1.Condition{
				Type:               rec.typ,
				Status:             metav1.ConditionUnknown,
				Reason:             rec.typ + "Ready",
				LastTransitionTime: metav1.Now(),
			}
			i = len(sp.Status.Conditions)
			sp.Status.Conditions = append(sp.Status.Conditions, c)
		}
		old := sp.Status.Conditions[i]
		if err := rec.r(ctx, &sp); err != nil {
			logger.Error(err, "run reconcile error", "type", rec.typ, "namespace", sp.Namespace, "name", sp.Name)
			sp.Status.Conditions[i].Status = metav1.ConditionFalse
			sp.Status.Conditions[i].Message = err.Error()
			failed = true
		} else {
			sp.Status.Conditions[i].Status = metav1.ConditionTrue
			sp.Status.Conditions[i].Message = ""
		}
		if !cmpConditionWithoutTime(old, sp.Status.Conditions[i]) {
			// 仅条件内容变化时更新时间，避免每轮调谐都改写该时间。
			sp.Status.Conditions[i].LastTransitionTime = metav1.Now()
		}
	}
	// 即使部分创建失败，也保存本次已恢复或新增的 runs；写入失败后可从真实任务重建。
	if err := r.Status().Update(ctx, &sp); err != nil {
		return ctrl.Result{}, err
	}
	_, find := lo.Find(sp.Status.Snapshots, func(item snapshotpodv1alpha1.SnapshotRunningStatus) bool {
		switch item.Result {
		case snapshotpodv1alpha1.SnapshotRunningResultSuccess, snapshotpodv1alpha1.SnapshotRunningResultFailed:
			return false
		}
		return true
	})
	if find || lo.ContainsBy(sp.Status.Conditions, func(item metav1.Condition) bool {
		return item.Status != metav1.ConditionTrue
	}) {
		// 未结束任务或未就绪阶段需要轮询；本控制器未注册任务或源 Pod 的事件监听。
		metrics.RecordControllerReconcile("snapshotpod", metrics.ControllerResultRequeue, time.Since(startTime))
		return ctrl.Result{RequeueAfter: time.Second * 15}, nil
	}

	metrics.RecordControllerReconcile("snapshotpod", metrics.ControllerResultSuccess, time.Since(startTime))
	return ctrl.Result{}, nil
}

// cmpConditionWithoutTime 比较条件的业务内容，忽略时间以判断是否需要更新时间。
func cmpConditionWithoutTime(c1, c2 metav1.Condition) bool {
	return c1.Status == c2.Status && c1.Message == c2.Message && c1.Reason == c2.Reason && c1.Type == c2.Type
}

// SetupWithManager 注册 SnapshotPod 事件；任务进度由 Reconcile 定时查询。
func (r *SnapshotPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&snapshotpodv1alpha1.SnapshotPod{}).
		Complete(r)
}
