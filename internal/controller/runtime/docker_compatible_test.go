package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

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
	}{
		{name: "unrelated credentials do not block push invocation", config: `{"auths":{"other.example":{"auth":"dXNlcjpwYXNz"}}}`},
		{name: "pull preserves runtime arguments", config: `{"auths":{}}`, pull: true},
		{name: "no secret keeps existing config"},
		{name: "push failure is returned", config: `{"auths":{}}`, fail: true},
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
			args := []string{"--namespace", "k8s.io", op, "registry.example/repo:tag"}
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
				err = rt.Pull(t.Context(), "registry.example/repo:tag", auth, "--unpack=false")
			} else {
				err = rt.Push(t.Context(), "registry.example/repo:tag", auth)
			}
			if (err != nil) != tc.fail {
				t.Fatalf("command error = %v, want failure = %v", err, tc.fail)
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
