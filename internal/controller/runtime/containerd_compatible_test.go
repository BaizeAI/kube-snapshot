package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// Only the executable is replaced: argument construction and command execution
// still go through containerdCompatibleRuntime.Commit and execCommand.
func TestContainerdCommit(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		nerdctlOK, containerdOK bool
		insecure, pause, fail   bool
		message, author         string
		wantArgs                []string
	}{
		{
			name:     "old versions default to secure",
			wantArgs: []string{"commit", "container-id", "registry.test/snapshot:tag", "--pause=false"},
		},
		{
			name: "old versions allow insecure opt-in", insecure: true,
			wantArgs: []string{"--insecure-registry", "commit", "container-id", "registry.test/snapshot:tag", "--pause=false"},
		},
		{
			name: "old nerdctl disables compression", containerdOK: true,
			wantArgs: []string{"commit", "container-id", "registry.test/snapshot:tag", "--pause=false"},
		},
		{
			name: "old nerdctl still allows insecure opt-in", containerdOK: true, insecure: true,
			wantArgs: []string{"--insecure-registry", "commit", "container-id", "registry.test/snapshot:tag", "--pause=false"},
		},
		{
			name: "old containerd disables compression", nerdctlOK: true,
			wantArgs: []string{"commit", "container-id", "registry.test/snapshot:tag", "--pause=false"},
		},
		{
			name: "old containerd still allows insecure opt-in", nerdctlOK: true, insecure: true,
			wantArgs: []string{"--insecure-registry", "commit", "container-id", "registry.test/snapshot:tag", "--pause=false"},
		},
		{
			name: "supported versions enable compression", nerdctlOK: true, containerdOK: true,
			wantArgs: []string{"commit", "container-id", "registry.test/snapshot:tag", "--pause=false", "--compression", "zstd"},
		},
		{
			name: "insecure commit preserves options and compression", nerdctlOK: true, containerdOK: true,
			insecure: true, pause: true, message: "snapshot message", author: "snapshot author",
			wantArgs: []string{
				"--insecure-registry", "commit", "container-id", "registry.test/snapshot:tag", "--pause=true",
				"--message", "snapshot message", "--author", "snapshot author", "--compression", "zstd",
			},
		},
		{
			name: "command failure is returned", nerdctlOK: true, containerdOK: true, insecure: true, fail: true,
			wantArgs: []string{"--insecure-registry", "commit", "container-id", "registry.test/snapshot:tag", "--pause=false", "--compression", "zstd"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docker, recordedArgs := newRecordingRuntime(t, "--debug", "-n", "k8s.io")
			rt := &containerdCompatibleRuntime{
				dockerCompatibleRuntime:       *docker,
				IsNerdctlVersionSufficient:    tc.nerdctlOK,
				IsContainerdVersionSufficient: tc.containerdOK,
			}
			if tc.fail {
				t.Setenv("SNAPSHOT_COMMAND_FAIL", "1")
			}
			err := rt.Commit(t.Context(), "container-id", "registry.test/snapshot:tag", tc.pause, tc.message, tc.author, tc.insecure)
			if tc.fail {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
					t.Fatalf("expected command exit status 1, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := append([]string{"--debug", "-n", "k8s.io"}, tc.wantArgs...)
			if got := recordedArgs(); !slices.Equal(got, want) {
				t.Fatalf("commit arguments = %q, want %q", got, want)
			}
		})
	}
}

func newRecordingRuntime(t *testing.T, globalArgs ...string) (*dockerCompatibleRuntime, func() []string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "command.json")
	t.Setenv("SNAPSHOT_COMMAND_HELPER", "1")
	t.Setenv("SNAPSHOT_COMMAND_TRACE", trace)
	t.Setenv("SNAPSHOT_COMMAND_FAIL", "")
	rt := &dockerCompatibleRuntime{
		dockerCompatibleCommand: executable,
		globalArgs:              append([]string{"-test.run=^TestRuntimeCommandHelper$", "--"}, globalArgs...),
	}
	return rt, func() []string {
		t.Helper()
		data, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		var args []string
		if err := json.Unmarshal(data, &args); err != nil {
			t.Fatal(err)
		}
		return args
	}
}

func TestRuntimeCommandHelper(t *testing.T) {
	if os.Getenv("SNAPSHOT_COMMAND_HELPER") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		t.Fatal("missing command argument separator")
	}
	data, err := json.Marshal(os.Args[separator+1:])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("SNAPSHOT_COMMAND_TRACE"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SNAPSHOT_COMMAND_FAIL") == "1" {
		t.Fatal("simulated runtime command failure")
	}
}
