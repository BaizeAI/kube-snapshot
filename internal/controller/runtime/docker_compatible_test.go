package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDockerCommit(t *testing.T) {
	for _, tc := range []struct {
		name, message, author string
		pause, fail           bool
		wantArgs              []string
	}{
		{
			name:     "default options",
			wantArgs: []string{"--debug", "commit", "--pause=false", "container-id", "registry.test/snapshot:tag"},
		},
		{
			name: "options precede positional arguments", pause: true, message: "snapshot message", author: "snapshot author",
			wantArgs: []string{
				"--debug", "commit", "--pause=true", "--message", "snapshot message", "--author", "snapshot author",
				"container-id", "registry.test/snapshot:tag",
			},
		},
		{
			name: "command failure includes commit context", fail: true,
			wantArgs: []string{"--debug", "commit", "--pause=false", "container-id", "registry.test/snapshot:tag"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, recordedArgs := newRecordingRuntime(t, "--debug")
			if tc.fail {
				t.Setenv("SNAPSHOT_COMMAND_FAIL", "1")
			}
			err := rt.Commit(t.Context(), "container-id", "registry.test/snapshot:tag", tc.pause, tc.message, tc.author, false)
			if tc.fail {
				if err == nil || !strings.Contains(err.Error(), "commit image: exit status 1") {
					t.Fatalf("expected contextual commit error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := recordedArgs(); !slices.Equal(got, tc.wantArgs) {
				t.Fatalf("commit arguments = %q, want %q", got, tc.wantArgs)
			}
		})
	}
}

func TestImageExists(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{{name: "image found"}, {name: "inspect failure", fail: true}} {
		t.Run(tc.name, func(t *testing.T) {
			rt, recordedArgs := newRecordingRuntime(t)
			if tc.fail {
				t.Setenv("SNAPSHOT_COMMAND_FAIL", "1")
			}
			if got := rt.ImageExists(t.Context(), "registry.test/snapshot:tag"); got == tc.fail {
				t.Fatalf("ImageExists = %t, want %t", got, !tc.fail)
			}
			want := []string{"image", "inspect", "registry.test/snapshot:tag"}
			if got := recordedArgs(); !slices.Equal(got, want) {
				t.Fatalf("inspect arguments = %q, want %q", got, want)
			}
		})
	}
}

func TestDockerConfigTempDirectoryFailure(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(tmpFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, tmpFile)
	}
	t.Setenv("DOCKER_CONFIG", "original-config")
	rt := NewDockerCompatibleRuntime("must-not-run")
	err := rt.Push(t.Context(), "registry.test/snapshot:tag", &Auth{ConfigJSON: `{}`}, true)
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "mkdir" {
		t.Fatalf("expected temporary directory creation error, got %v", err)
	}
	if got := os.Getenv("DOCKER_CONFIG"); got != "original-config" {
		t.Fatalf("parent Docker config changed to %q", got)
	}
}

// TestDockerConfigCommands 验证推拉命令直接读取凭据配置，不经过交互登录；
// 子进程代替 Docker/nerdctl，因此不连接 daemon，也不写入真实镜像仓库。
// 配置内容只需验证原样传递，具体认证格式由运行时处理。
func TestDockerConfigCommands(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, config string
		pull, fail   bool
		insecure     bool
	}{
		{name: "unrelated credentials do not block push invocation", config: `{"auths":{"other.example":{"auth":"dXNlcjpwYXNz"}}}`},
		{name: "pull preserves runtime arguments", config: `{"auths":{}}`, pull: true},
		{name: "no secret keeps existing config"},
		{name: "push failure is returned", config: `{"auths":{}}`, fail: true},
		{name: "authenticated insecure push", config: `{"auths":{}}`, insecure: true},
		{name: "authenticated insecure pull", config: `{"auths":{}}`, pull: true, insecure: true},
		{name: "anonymous insecure push", insecure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DOCKER_CONFIG", dir)
			t.Setenv("SNAPSHOT_RUNTIME_HELPER", "1")
			t.Setenv("SNAPSHOT_RUNTIME_RESULT", filepath.Join(dir, "result"))
			op := "push"
			if tc.pull {
				op = "pull"
			}
			args := []string{"--namespace", "k8s.io"}
			if tc.insecure {
				args = append(args, "--insecure-registry")
			}
			args = append(args, op, "registry.example/repo:tag")
			if tc.pull {
				args = append(args, "--unpack=false")
			}
			want, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("SNAPSHOT_RUNTIME_ARGS", string(want))
			t.Setenv("SNAPSHOT_RUNTIME_FAIL", "")
			if tc.fail {
				t.Setenv("SNAPSHOT_RUNTIME_FAIL", "1")
			}
			var auth *Auth
			t.Setenv("SNAPSHOT_RUNTIME_CONFIG", `{}`)
			if tc.config != "" {
				auth = &Auth{ConfigJSON: tc.config}
				t.Setenv("SNAPSHOT_RUNTIME_CONFIG", tc.config)
			}
			rt := NewDockerCompatibleRuntime(exe, "-test.run=^TestDockerRuntimeHelper$", "--", "--namespace", "k8s.io")
			if tc.pull {
				err = rt.Pull(t.Context(), "registry.example/repo:tag", auth, tc.insecure, "--unpack=false")
			} else {
				err = rt.Push(t.Context(), "registry.example/repo:tag", auth, tc.insecure)
			}
			if (err != nil) != tc.fail {
				t.Fatalf("command error = %v, want failure = %v", err, tc.fail)
			}
			if os.Getenv("DOCKER_CONFIG") != dir {
				t.Fatal("runtime changed the parent process Docker config")
			}
			used, err := os.ReadFile(filepath.Join(dir, "result"))
			if err != nil {
				t.Fatal(err)
			}
			if auth == nil {
				if string(used) != dir {
					t.Fatalf("inherited Docker config changed to %q", used)
				}
			} else if _, err := os.Stat(string(used)); !os.IsNotExist(err) {
				t.Fatalf("temporary Docker config was not removed: %v", err)
			}
		})
	}
}

// TestDockerRuntimeHelper 在测试子进程中校验实际参数和配置；任何 login 命令都会失败。
// 只记录配置路径以检查清理行为，不记录或输出凭据内容。
func TestDockerRuntimeHelper(t *testing.T) {
	if os.Getenv("SNAPSHOT_RUNTIME_HELPER") != "1" {
		return
	}
	i := slices.Index(os.Args, "--")
	var want []string
	if err := json.Unmarshal([]byte(os.Getenv("SNAPSHOT_RUNTIME_ARGS")), &want); err != nil {
		t.Fatal(err)
	}
	if i < 0 || !slices.Equal(os.Args[i+1:], want) {
		t.Fatal("unexpected runtime command; login must not be invoked")
	}
	dir := os.Getenv("DOCKER_CONFIG")
	file := filepath.Join(dir, "config.json")
	config, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(config) != os.Getenv("SNAPSHOT_RUNTIME_CONFIG") {
		t.Fatal("runtime did not receive the supplied Docker config")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Docker config must be readable only by its owner: %v", err)
	}
	if err := os.WriteFile(os.Getenv("SNAPSHOT_RUNTIME_RESULT"), []byte(dir), 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SNAPSHOT_RUNTIME_FAIL") == "1" {
		t.Fatal("simulated registry authorization failure")
	}
}
