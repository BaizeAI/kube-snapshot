package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	snapshotv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
	criruntime "github.com/baizeai/kube-snapshot/internal/controller/runtime"
	snapshotpkg "github.com/baizeai/kube-snapshot/pkg/apis/snapshotpod/v1alpha1"
)

type registryCommand struct {
	Args   []string
	Config string
}

func TestSetupRuntimesFailure(t *testing.T) {
	wantErr := errors.New("containerd unavailable")
	r := &SnapshotPodTaskReconciler{}
	err := r.setupRuntimes(func(cmd string, args ...string) (criruntime.Runtime, error) {
		if cmd != "nerdctl" || !reflect.DeepEqual(args, []string{"--debug", "-n", "k8s.io"}) {
			t.Fatalf("unexpected runtime command: %s %v", cmd, args)
		}
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "failed to create containerd compatible runtime") {
		t.Fatalf("expected wrapped initialization error, got %v", err)
	}
	if r.runtimeMap != nil {
		t.Fatal("runtime map initialized after containerd setup failed")
	}
}

func TestSetupWithManagerRuntimeFailure(t *testing.T) {
	// Initialization must fail before the manager is used when nerdctl is absent.
	t.Setenv("PATH", t.TempDir())
	r := &SnapshotPodTaskReconciler{}
	if err := r.SetupWithManager(nil); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("expected missing nerdctl error, got %v", err)
	}
	if r.runtimeMap != nil {
		t.Fatal("runtime map initialized after containerd setup failed")
	}
}

type registrySetupManager struct {
	ctrl.Manager
	controllers []manager.Runnable
}

func (m *registrySetupManager) Add(runnable manager.Runnable) error {
	m.controllers = append(m.controllers, runnable)
	return m.Manager.Add(runnable)
}

func TestSetupWithManagerRegistration(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := snapshotv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// Construct the real manager and builder without starting their API watches.
	skipNameValidation := true
	mgr, err := ctrl.NewManager(&rest.Config{Host: "http://127.0.0.1:1"}, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered := &registrySetupManager{Manager: mgr}
	containerd := criruntime.NewDockerCompatibleRuntime("must-not-run")
	r := &SnapshotPodTaskReconciler{}
	err = r.setupWithManager(registered, func(string, ...string) (criruntime.Runtime, error) {
		return containerd, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(registered.controllers) != 1 {
		t.Fatalf("registered %d controllers, want 1", len(registered.controllers))
	}
	if len(r.runtimeMap) != 2 || r.runtimeMap[containerdRuntime] != containerd || r.runtimeMap[dockerRuntime] == nil {
		t.Fatalf("unexpected runtime registration: %v", r.runtimeMap)
	}
}

// Run the test binary as a recording docker/nerdctl executable. This also works
// on Windows, where a shell script cannot be executed directly by exec.Command.
func init() {
	if os.Getenv("KUBE_SNAPSHOT_COMMAND_HELPER") != "1" {
		return
	}
	invocation := registryCommand{Args: os.Args[1:]}
	if configDir := os.Getenv("DOCKER_CONFIG"); configDir != "" {
		contents, err := os.ReadFile(filepath.Join(configDir, "config.json"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		invocation.Config = string(contents)
	}
	trace, err := os.OpenFile(os.Getenv("KUBE_SNAPSHOT_COMMAND_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	err = json.NewEncoder(trace).Encode(invocation)
	trace.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if fail := os.Getenv("KUBE_SNAPSHOT_FAIL_COMMAND"); fail != "" {
		for _, arg := range invocation.Args {
			if arg == fail {
				os.Exit(1)
			}
		}
	}
	os.Exit(0)
}

func TestSnapshotRegistryCommands(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := snapshotv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	const (
		targetRegistry = "registry.example.test:5000"
		originRegistry = "origin.example.test:5443"
		targetImage    = targetRegistry + "/team/snapshot:v1"
		originImage    = originRegistry + "/base:v1"
		targetConfig   = `{"auths":{"registry.example.test:5000":{"auth":"dXNlcjpwYXNz"}}}`
		originConfig   = `{"auths":{"origin.example.test:5443":{"auth":"b3JpZ2luOnBhc3M="}}}`
	)
	secret := func(name, config string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(config)},
		}
	}
	r := &SnapshotPodTaskReconciler{
		Scheme: scheme,
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			secret("target", targetConfig), secret("origin", originConfig),
		).Build(),
	}
	// Use the production runtime wiring without contacting a containerd socket.
	// The containerd runtime delegates registry operations to the same wrapper.
	if err := r.setupRuntimes(func(cmd string, args ...string) (criruntime.Runtime, error) {
		return criruntime.NewDockerCompatibleRuntime(cmd, args...), nil
	}); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	for _, name := range []string{"docker", "nerdctl"} {
		if goruntime.GOOS == "windows" {
			name += ".exe"
		}
		target := filepath.Join(binDir, name)
		if err := os.Link(executable, target); err != nil {
			contents, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, contents, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KUBE_SNAPSHOT_COMMAND_HELPER", "1")
	inspect := registryCommand{Args: []string{"image", "inspect", targetImage}}
	pull := registryCommand{Args: []string{"pull", originImage, "--unpack=false"}}
	originPull := registryCommand{Args: pull.Args, Config: originConfig}
	push := registryCommand{Args: []string{"push", targetImage}}
	targetPush := registryCommand{Args: push.Args, Config: targetConfig}
	insecure := func(command registryCommand) registryCommand {
		command.Args = append([]string{"--insecure-registry"}, command.Args...)
		return command
	}
	for _, tt := range []struct {
		name, runtime, targetSecret, originSecret, fail, errorContains string
		insecureRegistry                                               bool
		commands                                                       []registryCommand
	}{
		{"anonymous containerd opt-in", containerdRuntime, "", "", "", "", true, []registryCommand{inspect, insecure(pull), insecure(push)}},
		{"anonymous containerd defaults", containerdRuntime, "", "", "", "", false, []registryCommand{inspect, pull, push}},
		{"authenticated containerd opt-in", containerdRuntime, "target", "origin", "", "", true, []registryCommand{
			inspect, insecure(originPull), insecure(targetPush),
		}},
		{"authenticated containerd defaults", containerdRuntime, "target", "origin", "", "", false, []registryCommand{
			inspect, originPull, targetPush,
		}},
		{"login is never invoked", containerdRuntime, "target", "", "login", "", true, []registryCommand{
			inspect, insecure(pull), insecure(targetPush),
		}},
		{"push failure does not retry anonymously", containerdRuntime, "target", "", "push", "exit status 1", false, []registryCommand{
			inspect, pull, targetPush,
		}},
		{"source pull failure remains nonfatal", containerdRuntime, "target", "", "pull", "", false, []registryCommand{
			inspect, pull, targetPush,
		}},
		{"anonymous Docker unchanged", dockerRuntime, "", "", "", "", false, []registryCommand{inspect, push}},
		{"Docker ignores nerdctl options", dockerRuntime, "target", "", "", "", true, []registryCommand{
			inspect, targetPush,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			trace := filepath.Join(t.TempDir(), "commands.jsonl")
			t.Setenv("KUBE_SNAPSHOT_COMMAND_TRACE", trace)
			t.Setenv("KUBE_SNAPSHOT_FAIL_COMMAND", tt.fail)
			t.Setenv("DOCKER_CONFIG", "")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			task := &snapshotv1alpha1.SnapshotPodTask{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: snapshotv1alpha1.SnapshotPodTaskSpec{
					ContainerID:             tt.runtime + "://container",
					CommitImage:             targetImage,
					OriginImage:             originImage,
					RegistrySecretRef:       tt.targetSecret,
					OriginRegistrySecretRef: tt.originSecret,
					InsecureRegistry:        tt.insecureRegistry,
				},
			}
			if err := r.reconcileCommit(ctx, task); err != nil {
				t.Fatal(err)
			}
			err := r.reconcilePushImage(ctx, task)
			if tt.errorContains == "" && err != nil {
				t.Fatal(err)
			}
			if tt.errorContains != "" && (err == nil || !strings.Contains(err.Error(), tt.errorContains)) {
				t.Fatalf("expected error containing %q, got %v", tt.errorContains, err)
			}
			file, err := os.Open(trace)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			var actual []registryCommand
			decoder := json.NewDecoder(file)
			for {
				var command registryCommand
				if err := decoder.Decode(&command); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				actual = append(actual, command)
			}
			prefix := []string{"--debug"}
			commit := registryCommand{Args: []string{"commit", "--pause=false", "container", targetImage}}
			if tt.runtime == containerdRuntime {
				prefix = append(prefix, "-n", "k8s.io")
				if tt.insecureRegistry {
					commit = insecure(commit)
				}
			}
			var expected []registryCommand
			for _, command := range append([]registryCommand{commit}, tt.commands...) {
				expected = append(expected, registryCommand{
					Args: append(append([]string{}, prefix...), command.Args...), Config: command.Config,
				})
			}
			if !reflect.DeepEqual(expected, actual) {
				t.Fatalf("unexpected commands or credentials\nexpected: %#v\nactual: %#v", expected, actual)
			}
		})
	}

	t.Run("Docker commit options precede positional arguments", func(t *testing.T) {
		trace := filepath.Join(t.TempDir(), "commands.jsonl")
		t.Setenv("KUBE_SNAPSHOT_COMMAND_TRACE", trace)
		t.Setenv("KUBE_SNAPSHOT_FAIL_COMMAND", "")
		t.Setenv("DOCKER_CONFIG", "")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := r.runtimeMap[dockerRuntime].Commit(ctx, "container-id", targetImage, false, "snapshot", "notebook", false); err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		var command registryCommand
		if err := json.Unmarshal(contents, &command); err != nil {
			t.Fatal(err)
		}
		expected := []string{"--debug", "commit", "--pause=false", "--message", "snapshot", "--author", "notebook", "container-id", targetImage}
		if !reflect.DeepEqual(command.Args, expected) {
			t.Fatalf("expected Docker commit arguments %v, got %v", expected, command.Args)
		}
	})
}

func TestSnapshotInsecureRegistryRounds(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := snapshotv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "notebook"},
		Spec: corev1.PodSpec{
			NodeName:   "worker",
			Containers: []corev1.Container{{Name: "main"}},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:        "main",
			Image:       "source.example.test/base:v1",
			ContainerID: "containerd://container",
		}}},
	}
	r := &SnapshotPodReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()}
	sp := &snapshotv1alpha1.SnapshotPod{
		ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: "snapshot", UID: "snapshot-id"},
		Spec: snapshotv1alpha1.SnapshotPodSpec{
			Target: snapshotv1alpha1.TargetPod{Name: pod.Name},
			ImageSaveOptions: snapshotv1alpha1.ImageSaveOptions{
				ImageRefFormat: "target.example.test/snapshot:v1",
			},
		},
	}
	for _, useRequest := range []bool{false, true} {
		t.Run(fmt.Sprintf("round-request=%t", useRequest), func(t *testing.T) {
			sp := sp.DeepCopy()
			sp.Name = fmt.Sprintf("snapshot-%t", useRequest)
			sp.Spec.ImageSaveOptions.RegistrySecretRef = "persistent-secret"
			for _, round := range []int32{6, 7, 8, 9} {
				sp.Spec.TriggerRound = round
				switch round {
				case 7:
					sp.Spec.ImageSaveOptions.InsecureRegistry = true
				case 9:
					sp.Spec.ImageSaveOptions.InsecureRegistry = false
				}
				var request *snapshotpkg.RoundRequest
				secretRef := "round-secret"
				if useRequest {
					request = &snapshotpkg.RoundRequest{
						Round: round, Images: map[string]string{"main": "target.example.test/manual:fixed"},
						RegistrySecretRef: &secretRef,
					}
				}
				if err := r.reconcileTasks(ctx, sp, request, pod); err != nil {
					t.Fatal(err)
				}
				task := &snapshotv1alpha1.SnapshotPodTask{}
				key := client.ObjectKey{Namespace: sp.Namespace, Name: fmt.Sprintf("%s-%d-main", sp.Name, round)}
				if err := r.Get(ctx, key, task); err != nil {
					t.Fatal(err)
				}
				// Round 8 advances the trigger without changing the setting from round 7.
				if task.Spec.InsecureRegistry != (round == 7 || round == 8) {
					t.Fatalf("round %d: unexpected insecure registry setting %t", round, task.Spec.InsecureRegistry)
				}
				if useRequest && (task.Spec.CommitImage != request.Images["main"] || task.Spec.RegistrySecretRef != secretRef ||
					task.Annotations[snapshotpkg.RestoreCredentialSourceAnnotation] != snapshotpkg.RestoreCredentialSourceTask) {
					t.Fatalf("round-specific inputs were not preserved: %+v", task)
				}
				// A later configuration change must not mutate an already created task.
				sp.Spec.ImageSaveOptions.InsecureRegistry = !task.Spec.InsecureRegistry
				if err := r.reconcileTasks(ctx, sp, request, pod); err != nil {
					t.Fatal(err)
				}
				original := task.DeepCopy()
				if err := r.Get(ctx, key, task); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(original.Spec, task.Spec) {
					t.Fatal("existing task inputs changed")
				}
				sp.Spec.ImageSaveOptions.InsecureRegistry = task.Spec.InsecureRegistry
			}
		})
	}
}
