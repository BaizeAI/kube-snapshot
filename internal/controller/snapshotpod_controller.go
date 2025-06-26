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
	"strings"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/google/uuid"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/containers/image/v5/docker"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	snapshotpodv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
	"github.com/baizeai/kube-snapshot/pkg/apis/snapshotpod/v1alpha1"
)

// SnapshotPodReconciler reconciles a SnapshotPod object
type SnapshotPodReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *SnapshotPodReconciler) getPod(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod) (*corev1.Pod, error) {
	if sp.Spec.Target.Name == "" && sp.Spec.Target.Selector == nil {
		return nil, fmt.Errorf("target pod name or selector is required")
	}
	if sp.Spec.Target.Name != "" {
		pod := corev1.Pod{}
		err := r.Client.Get(ctx, client.ObjectKey{
			Namespace: sp.Namespace,
			Name:      sp.Spec.Target.Name,
		}, &pod)
		return &pod, err
	}
	if sp.Spec.Target.Selector != nil {
		podList := corev1.PodList{}
		err := r.Client.List(ctx, &podList, &client.ListOptions{
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

func (r *SnapshotPodReconciler) validate(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod) error {
	if sp.Status.LastTriggerRound == sp.Spec.TriggerRound {
		return nil
	}
	// validate
	pod, err := r.getPod(ctx, sp)
	if err != nil {
		return err
	}
	if len(sp.Spec.Target.Containers) != 0 {
		if len(lo.Without(sp.Spec.Target.Containers, lo.Map(pod.Spec.Containers, func(item corev1.Container, index int) string {
			return item.Name
		})...)) != 0 {
			return fmt.Errorf("containers %v not valid", sp.Spec.Target.Containers)
		}
	}
	return nil
}

func snapshotPodOwner(s *snapshotpodv1alpha1.SnapshotPod) metav1.OwnerReference {
	return *metav1.NewControllerRef(s, snapshotpodv1alpha1.GroupVersion.WithKind("SnapshotPod"))
}

func (r *SnapshotPodReconciler) reconcileTasks(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod) error {
	if sp.Status.LastTriggerRound == sp.Spec.TriggerRound {
		return nil
	}
	if lo.ContainsBy(sp.Status.Snapshots, func(item snapshotpodv1alpha1.SnapshotRunningStatus) bool {
		return item.TriggerRound == sp.Spec.TriggerRound
	}) {
		// already exists, skip
		sp.Status.LastTriggerRound = sp.Spec.TriggerRound
		return nil
	}
	pod, err := r.getPod(ctx, sp)
	if err != nil {
		return err
	}
	containers := lo.Map(pod.Spec.Containers, func(item corev1.Container, index int) string {
		return item.Name
	})
	if len(sp.Spec.Target.Containers) != 0 {
		containers = sp.Spec.Target.Containers
	}
	for _, c := range containers {
		css := lo.Filter(pod.Status.ContainerStatuses, func(item corev1.ContainerStatus, index int) bool {
			return item.Name == c
		})
		if len(css) == 0 {
			return fmt.Errorf("container status of %s/%s/%s not found", pod.Namespace, pod.Name, c)
		}
		cs := css[0]
		if cs.ContainerID == "" {
			return fmt.Errorf("container %s not started", c)
		}
		newImage, err := renderNewImageName(cs.Image, sp.Spec.ImageSaveOptions.ImageRefFormat)
		if err != nil {
			return err
		}
		spt := snapshotpodv1alpha1.SnapshotPodTask{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: sp.Namespace,
				Name:      fmt.Sprintf("%s-%d-%s", sp.Name, sp.Spec.TriggerRound, c),
				OwnerReferences: []metav1.OwnerReference{
					snapshotPodOwner(sp),
				},
				Labels: map[string]string{
					v1alpha1.SnapshotNameLabel: sp.Name,
				},
			},
			Spec: snapshotpodv1alpha1.SnapshotPodTaskSpec{
				NodeName:          pod.Spec.NodeName,
				PodName:           pod.Name,
				ContainerID:       cs.ContainerID,
				CommitOptions:     sp.Spec.CommitOptions,
				OriginImage:       cs.Image,
				CommitImage:       newImage,
				RegistrySecretRef: sp.Spec.ImageSaveOptions.RegistrySecretRef,
				MaxRetries:        sp.Spec.ImageSaveOptions.MaxRetries,
				RetryDelaySeconds: sp.Spec.ImageSaveOptions.RetryDelaySeconds,
			},
			Status: snapshotpodv1alpha1.SnapshotPodTaskStatus{},
		}
		err = r.Client.Create(ctx, &spt)
		if err != nil && !errors.IsAlreadyExists(err) {
			return err
		}
		sp.Status.Snapshots = append(sp.Status.Snapshots, snapshotpodv1alpha1.SnapshotRunningStatus{
			TriggerRound: sp.Spec.TriggerRound,
			TriggerTime: metav1.Time{
				Time: time.Now(),
			},
			TaskName:      spt.Name,
			ImageRef:      spt.Spec.CommitImage,
			ContainerName: c,
			Result:        snapshotpodv1alpha1.SnapshotRunningResultCreated,
		})
		// only keep last 20 snapshots
		if len(sp.Status.Snapshots) > 20 {
			sp.Status.Snapshots = sp.Status.Snapshots[len(sp.Status.Snapshots)-5:]
		}
	}
	return nil
}

func (r *SnapshotPodReconciler) reconcileTasksStatus(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod) error {
	sptList := snapshotpodv1alpha1.SnapshotPodTaskList{}
	selector, _ := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: map[string]string{
			v1alpha1.SnapshotNameLabel: sp.Name,
		},
	})
	err := r.Client.List(ctx, &sptList, &client.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
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

func withRandomTag(tag string) string {
	fixed := "snapped-"
	if strings.Contains(tag, fixed) {
		return tag
	}
	gen := func() string {
		return fmt.Sprintf("%s%d-%s", fixed, time.Now().Unix(), strings.ReplaceAll(uuid.New().String(), "-", "")[:8])
	}
	if tag == "" || tag == "latest" {
		return gen()
	}
	return fmt.Sprintf("%s-%s", tag, gen())
}

func renderNewImageName(originImage, format string) (string, error) {
	ref, err := docker.ParseReference("//" + originImage)
	if err != nil {
		return "", err
	}
	r := ref.DockerReference()
	domain := reference.Domain(r)
	p := reference.Path(r)
	tag := ""
	switch v := r.(type) {
	case reference.NamedTagged:
		tag = v.Tag()
	case reference.Canonical:
		tag = v.Digest().Hex()
	}
	lr, _ := lo.Last(strings.Split(p, "/"))
	m := map[string]string{
		"domain":          domain,
		"repository":      p,
		"last_repository": lr,
		"tag":             tag,
	}
	for k, v := range m {
		format = strings.ReplaceAll(format, fmt.Sprintf("{%s}", k), v)
	}
	ref, err = docker.ParseReference("//" + format)
	if err != nil {
		return "", err
	}
	newTag := ""
	if v, ok := ref.DockerReference().(reference.NamedTagged); !ok {
		newTag = withRandomTag("")
	} else {
		newTag = withRandomTag(v.Tag())
	}
	v, err := reference.WithTag(ref.DockerReference(), newTag)
	if err != nil {
		return "", err
	}
	format = v.String()
	return format, nil
}

// +kubebuilder:rbac:groups=snapshot-pod.baizeai.io,resources=snapshotpods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapshot-pod.baizeai.io,resources=snapshotpods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=snapshot-pod.baizeai.io,resources=snapshotpods/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// the SnapshotPod object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.18.2/pkg/reconcile
func (r *SnapshotPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	sp := snapshotpodv1alpha1.SnapshotPod{}
	err := r.Client.Get(ctx, req.NamespacedName, &sp)
	if err != nil {
		logger.Error(err, "get instance error")
		return ctrl.Result{}, err
	}
	if sp.Spec.TriggerRound <= 0 {
		return ctrl.Result{}, nil
	}
	type reconciler func(ctx context.Context, sp *snapshotpodv1alpha1.SnapshotPod) error
	type rec struct {
		typ string
		r   reconciler
	}
	recs := []rec{
		{
			typ: "Accept",
			r:   r.validate,
		},
		{
			typ: "Tasks",
			r:   r.reconcileTasks,
		},
		{
			typ: "TaskStatus",
			r:   r.reconcileTasksStatus,
		},
	}
	for _, rec := range recs {
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
			break
		} else {
			sp.Status.Conditions[i].Status = metav1.ConditionTrue
		}
		if !cmpConditionWithoutTime(old, sp.Status.Conditions[i]) {
			// update LastTransitionTime if condition changed.
			sp.Status.Conditions[i].LastTransitionTime = metav1.Now()
		}
	}
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
		return ctrl.Result{RequeueAfter: time.Second * 15}, nil
	}
	return ctrl.Result{}, nil
}

func cmpConditionWithoutTime(c1, c2 metav1.Condition) bool {
	return c1.Status == c2.Status && c1.Message == c2.Message && c1.Reason == c2.Reason && c1.Type == c2.Type
}

// SetupWithManager sets up the controller with the Manager.
func (r *SnapshotPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&snapshotpodv1alpha1.SnapshotPod{}).
		Complete(r)
}
