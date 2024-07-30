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

func (d *dockerCompatibleRuntime) Commit(ctx context.Context, containerID, image string, pause bool, message, author string) error {
	args := []string{"commit", containerID, image, fmt.Sprintf("--pause=%v", pause)}
	if message != "" {
		args = append(args, "--message", message)
	}
	if author != "" {
		args = append(args, "--author", author)
	}
	if err := d.execCommand(ctx, args, nil); err != nil {
		return fmt.Errorf("commit image: %s", err)
	}
	return nil
}

func (d *dockerCompatibleRuntime) ImageExists(ctx context.Context, image string) bool {
	err := d.execCommand(ctx, []string{"image", "inspect", image}, nil)
	return err == nil
}

func (d *dockerCompatibleRuntime) Pull(ctx context.Context, image string, auth *Auth, args ...string) error {
	return d.withLoginContextCommands(ctx, auth, append([]string{"pull", image}, args...), nil)
}

func (d *dockerCompatibleRuntime) Push(ctx context.Context, image string, auth *Auth) error {
	return d.withLoginContextCommands(ctx, auth, []string{"push", image}, nil)
}

func (d *dockerCompatibleRuntime) withLoginContextCommands(ctx context.Context, auth *Auth, args []string, stdin io.Reader) error {
	if auth == nil {
		err := d.execCommand(ctx, args, stdin)
		return err
	}
	tmp, err := os.MkdirTemp("", ".docker-config-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	os.Setenv("DOCKER_CONFIG", tmp)
	defer os.Unsetenv("DOCKER_CONFIG")
	if err := os.WriteFile(tmp+"/config.json", []byte(auth.ConfigJSON), 0o600); err != nil {
		return err
	}
	err = d.requireLogin(ctx, auth)
	if err != nil {
		return err
	}
	if err = d.execCommand(ctx, args, stdin); err != nil {
		return fmt.Errorf("after login: %s", err)
	}
	return nil
}

func (d *dockerCompatibleRuntime) requireLogin(ctx context.Context, auth *Auth) error {
	if auth == nil {
		return nil
	}
	if err := d.execCommand(ctx, []string{"login", auth.Registry}, nil); err != nil {
		return fmt.Errorf("login '%s': %s", auth.Registry, err)
	}
	return nil
}

func (d *dockerCompatibleRuntime) execCommand(ctx context.Context, args []string, stdin io.Reader) error {
	c := exec.CommandContext(ctx, d.dockerCompatibleCommand, append(d.globalArgs, args...)...)
	out := bytes.NewBuffer(nil)
	errOut := bytes.NewBuffer(nil)
	c.Stdin = stdin
	c.Stdout = out
	c.Stderr = errOut
	err := c.Run()
	klog.Infof("execCommand: %s %v, out: %s, errOut: %s, err: %v", d.dockerCompatibleCommand, args, out.String(), errOut.String(), err)
	return err
}
