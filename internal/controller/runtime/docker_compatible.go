package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"k8s.io/klog/v2"
)

type dockerCompatibleRuntime struct {
	dockerCompatibleCommand string
	globalArgs              []string
}

func NewDockerCompatibleRuntime(cmd string, globalArgs ...string) Runtime {
	return &dockerCompatibleRuntime{
		dockerCompatibleCommand: cmd,
		globalArgs:              globalArgs,
	}
}

func (d *dockerCompatibleRuntime) Commit(ctx context.Context, containerID string, image string, pause bool, message string, author string) error {
	args := []string{"commit", containerID, image, fmt.Sprintf("--pause=%v", pause)}
	if message != "" {
		args = append(args, "--message", message)
	}
	if author != "" {
		args = append(args, "--author", author)
	}
	_, _, err := d.execCommand(ctx, args, nil)
	return err
}

func (d *dockerCompatibleRuntime) ImageExists(ctx context.Context, image string) bool {
	_, _, err := d.execCommand(ctx, []string{"image", "inspect", image}, nil)
	return err == nil
}

func (d *dockerCompatibleRuntime) Pull(ctx context.Context, image string, auth *Auth, args ...string) error {
	_, _, err := d.withLoginContextCommands(ctx, auth, append([]string{"pull", image}, args...), nil)
	return err
}

func (d *dockerCompatibleRuntime) Push(ctx context.Context, image string, auth *Auth) error {
	_, _, err := d.withLoginContextCommands(ctx, auth, []string{"push", image}, nil)
	return err
}

func (d *dockerCompatibleRuntime) withLoginContextCommands(ctx context.Context, auth *Auth, args []string, stdin io.Reader) (string, string, error) {
	if auth == nil {
		return d.execCommand(ctx, args, stdin)
	}
	tmp, err := os.MkdirTemp("", ".docker-config-*")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmp)
	os.Setenv("DOCKER_CONFIG", tmp)
	defer os.Unsetenv("DOCKER_CONFIG")
	os.WriteFile(tmp+"/config.json", []byte(auth.ConfigJSON), 0600)
	err = d.requireLogin(ctx, auth)
	if err != nil {
		return "", "", err
	}
	return d.execCommand(ctx, args, stdin)
}

func (d *dockerCompatibleRuntime) requireLogin(ctx context.Context, auth *Auth) error {
	if auth == nil {
		return nil
	}
	_, _, err := d.execCommand(ctx, []string{"login", auth.Registry}, nil)
	return err
}

func (d *dockerCompatibleRuntime) execCommand(ctx context.Context, args []string, stdin io.Reader) (string, string, error) {
	c := exec.CommandContext(ctx, d.dockerCompatibleCommand, append(d.globalArgs[:], args...)...)
	out := bytes.NewBuffer(nil)
	errOut := bytes.NewBuffer(nil)
	c.Stdin = stdin
	c.Stdout = out
	c.Stderr = errOut
	err := c.Run()
	klog.Infof("execCommand: %s %v, out: %s, errOut: %s, err: %v", d.dockerCompatibleCommand, args, out.String(), errOut.String(), err)
	return out.String(), errOut.String(), err
}
