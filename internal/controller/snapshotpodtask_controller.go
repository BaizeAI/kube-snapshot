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

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/docker/reference"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	snapshotpodv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
	criruntime "github.com/baizeai/kube-snapshot/internal/controller/runtime"
)

const (
	dockerRuntime     = "docker"
	containerdRuntime = "containerd"
)

var defaultCommitTimeout = "5m"

// SnapshotPodTaskReconciler reconciles a SnapshotPodTask object
type SnapshotPodTaskReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	NodeName   string
	runtimeMap map[string]criruntime.Runtime
}

func (r *SnapshotPodTaskReconciler) getRuntimeAndContainerID(containerID string) (string, criruntime.Runtime, string, error) {
	rtName := strings.Split(containerID, "://")[0]
	rt, ok := r.runtimeMap[rtName]
	if !ok {
		return "", nil, "", fmt.Errorf("unsupported for runtime: %s, current supported list: %v", rtName, lo.Keys(r.runtimeMap))
	}
	cid := strings.Split(containerID, "://")[1]
	return rtName, rt, cid, nil
}

func (r *SnapshotPodTaskReconciler) getImageAuthWithSecret(ctx context.Context, namespace, sec, image string) (*criruntime.Auth, error) {
	if sec == "" {
		return nil, nil
	}
	secret := corev1.Secret{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      sec,
	}, &secret)
	if err != nil {
		return nil, err
	}
	ref, err := docker.ParseReference("//" + image)
	if err != nil {
		return nil, err
	}
	a := criruntime.Auth{
		Registry:   reference.Domain(ref.DockerReference()),
		ConfigJSON: string(secret.Data[".dockerconfigjson"]),
	}
	return &a, nil
}

func (r *SnapshotPodTaskReconciler) reconcilePushImage(ctx context.Context, spt *snapshotpodv1alpha1.SnapshotPodTask) error {
	rtName, rt, _, err := r.getRuntimeAndContainerID(spt.Spec.ContainerID)
	if err != nil {
		return err
	}
	if !rt.ImageExists(ctx, spt.Spec.CommitImage) {
		return fmt.Errorf("image %s not exists", spt.Spec.CommitImage)
	}
	auth, err := r.getImageAuthWithSecret(ctx, spt.Namespace, spt.Spec.RegistrySecretRef, spt.Spec.CommitImage)
	if err != nil {
		return err
	}
	if rtName == containerdRuntime {
		// see https://github.com/containerd/nerdctl/issues/3026
		// we just force pull the image from the origin registry
		a, _ := r.getImageAuthWithSecret(ctx, spt.Namespace, spt.Spec.OriginRegistrySecretRef, spt.Spec.OriginImage)
		_ = rt.Pull(ctx, spt.Spec.OriginImage, a, "--unpack=false")
	}
	return rt.Push(ctx, spt.Spec.CommitImage, auth)
}

func (r *SnapshotPodTaskReconciler) reconcileCommit(ctx context.Context, spt *snapshotpodv1alpha1.SnapshotPodTask) error {
	_, rt, cid, err := r.getRuntimeAndContainerID(spt.Spec.ContainerID)
	if err != nil {
		return err
	}
	timeout := defaultCommitTimeout
	if len(spt.Spec.CommitOptions.Timeout) > 0 {
		timeout = spt.Spec.CommitOptions.Timeout
	}
	d, err := time.ParseDuration(timeout)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	err = rt.Commit(ctx, cid, spt.Spec.CommitImage, spt.Spec.CommitOptions.PauseContainer, spt.Spec.CommitOptions.Message, spt.Spec.CommitOptions.Author)
	return err
}

func (r *SnapshotPodTaskReconciler) reconcileAccept(ctx context.Context, spt *snapshotpodv1alpha1.SnapshotPodTask) error {
	return nil
}

// +kubebuilder:rbac:groups=snapshot-pod.baizeai.io,resources=snapshotpodtasks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapshot-pod.baizeai.io,resources=snapshotpodtasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=snapshot-pod.baizeai.io,resources=snapshotpodtasks/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// the SnapshotPodTask object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.18.2/pkg/reconcile
func (r *SnapshotPodTaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	spt := snapshotpodv1alpha1.SnapshotPodTask{}
	err := r.Client.Get(ctx, req.NamespacedName, &spt)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if spt.Spec.NodeName != r.NodeName {
		// not my job
		return ctrl.Result{}, nil
	}
	if spt.Status.Phase == snapshotpodv1alpha1.SnapshotPodTaskPhaseCompleted ||
		spt.Status.Phase == snapshotpodv1alpha1.SnapshotPodTaskPhaseFailed {
		return ctrl.Result{}, nil
	}
	if spt.Status.RetryCount < 0 {
		spt.Status.RetryCount = 0
	}

	type reconciler func(ctx context.Context, spt *snapshotpodv1alpha1.SnapshotPodTask) error
	type rec struct {
		typ string
		r   reconciler
	}
	recs := []rec{
		{
			typ: "Accept",
			r:   r.reconcileAccept,
		},
		{
			typ: "Commit",
			r:   r.reconcileCommit,
		},
		{
			typ: "Push",
			r:   r.reconcilePushImage,
		},
	}
	var lastError error
	for _, rec := range recs {
		_, i, ok := lo.FindIndexOf(spt.Status.Conditions, func(item metav1.Condition) bool {
			return item.Type == rec.typ
		})
		if !ok {
			c := metav1.Condition{
				Type:               rec.typ,
				Status:             metav1.ConditionUnknown,
				Reason:             rec.typ + "Ready",
				LastTransitionTime: metav1.Now(),
			}
			i = len(spt.Status.Conditions)
			spt.Status.Conditions = append(spt.Status.Conditions, c)
		}
		old := spt.Status.Conditions[i]
		if old.Status == metav1.ConditionTrue {
			// already done
			continue
		}
		if err := rec.r(ctx, &spt); err != nil {
			logger.Error(err, "run reconcile task error", "type", rec.typ, "namespace", spt.Namespace, "name", spt.Name)
			spt.Status.Conditions[i].Status = metav1.ConditionFalse
			spt.Status.Conditions[i].Message = err.Error()
			lastError = err
			// Update status immediately after error
			if updateErr := r.Status().Update(ctx, &spt); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			break
		} else {
			spt.Status.Conditions[i].Status = metav1.ConditionTrue
			spt.Status.Conditions[i].Message = ""
		}
		if !cmpConditionWithoutTime(old, spt.Status.Conditions[i]) || spt.Status.Conditions[i].LastTransitionTime.IsZero() {
			// update LastTransitionTime if condition changed.
			spt.Status.Conditions[i].LastTransitionTime = metav1.Now()
		}
		// Update status after each successful step
		if updateErr := r.Status().Update(ctx, &spt); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
	}

	// Update phase based on conditions
	switch {
	case lo.EveryBy(spt.Status.Conditions, func(item metav1.Condition) bool {
		return item.Status == metav1.ConditionTrue
	}):
		spt.Status.Phase = snapshotpodv1alpha1.SnapshotPodTaskPhaseCompleted
	case lo.SomeBy(spt.Status.Conditions, func(item metav1.Condition) bool {
		if item.Status != metav1.ConditionTrue {
			if lastError != nil || item.LastTransitionTime.Add(time.Minute*10).Before(time.Now()) {
				return true
			}
		}
		return false
	}):
		spt.Status.RetryCount++
		spt.Status.LastRetryTime = lo.ToPtr(metav1.Now())
		maxRetries := spt.Spec.MaxRetries
		if maxRetries == 0 {
			maxRetries = 3
		}
		if spt.Status.RetryCount >= maxRetries {
			spt.Status.Phase = snapshotpodv1alpha1.SnapshotPodTaskPhaseFailed
			logger.Info("task failed after max retries",
				"retryCount", spt.Status.RetryCount,
				"maxRetries", maxRetries)
		}
	default:
		spt.Status.Phase = snapshotpodv1alpha1.SnapshotPodTaskPhaseCreated
	}

	// Final status update for phase change
	if err := r.Status().Update(ctx, &spt); err != nil {
		return ctrl.Result{}, err
	}

	switch spt.Status.Phase {
	case snapshotpodv1alpha1.SnapshotPodTaskPhaseFailed, snapshotpodv1alpha1.SnapshotPodTaskPhaseCompleted:
		return ctrl.Result{}, nil
	}
	retryDelay := time.Second * 30
	if spt.Spec.RetryDelaySeconds > 0 {
		retryDelay = time.Second * time.Duration(spt.Spec.RetryDelaySeconds)
	}
	return ctrl.Result{RequeueAfter: retryDelay}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SnapshotPodTaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.runtimeMap = map[string]criruntime.Runtime{
		dockerRuntime:     criruntime.NewDockerCompatibleRuntime("docker", "--debug"),
		containerdRuntime: criruntime.NewDockerCompatibleRuntime("nerdctl", "--debug", "-n", "k8s.io"),
		// for kind
		// containerdRuntime: criruntime.NewDockerCompatibleRuntime("docker", "exec", "-i", "kind-control-plane", "nerdctl", "-n", "k8s.io"),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&snapshotpodv1alpha1.SnapshotPodTask{}).
		Complete(r)
}
