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
	args := []string{"commit", fmt.Sprintf("--pause=%v", pause)}
	if message != "" {
		args = append(args, "--message", message)
	}
	if author != "" {
		args = append(args, "--author", author)
	}
	args = append(args, containerID, image)
	if err := d.execCommand(ctx, args); err != nil {
		return fmt.Errorf("commit image: %s", err)
	}
	return nil
}

func (d *dockerCompatibleRuntime) ImageExists(ctx context.Context, image string) bool {
	err := d.execCommand(ctx, []string{"image", "inspect", image})
	return err == nil
}

// Pull 使用提供的 Docker 配置拉取镜像，保留 nerdctl 等运行时的附加参数。
func (d *dockerCompatibleRuntime) Pull(ctx context.Context, image string, auth *Auth, args ...string) error {
	return d.withDockerConfig(ctx, auth, append([]string{"pull", image}, args...))
}

// Push 由运行时按目标仓库选择配置中的凭据，或尝试匿名推送。
func (d *dockerCompatibleRuntime) Push(ctx context.Context, image string, auth *Auth) error {
	return d.withDockerConfig(ctx, auth, []string{"push", image})
}

// withDockerConfig 临时写入 Secret 中的 Docker 配置，供 Docker 和 nerdctl 直接读取。
// 不再单独执行 `docker login <registry>`：该命令本身不是推拉镜像的前置步骤，
// 而 Docker/nerdctl 会在 push/pull 时根据镜像地址，从此 config.json 选择目标仓库凭据。
// 若配置没有目标仓库凭据但仓库允许匿名访问，预先 login 可能转入用户名/密码交互；
// runner 没有 TTY，因而会在 push/pull 尚未尝试前失败。直接推拉则可使用匹配凭据，
// 或由仓库按实际权限返回匿名访问结果/认证错误，避免把交互式 login 错误误作推拉结果。
func (d *dockerCompatibleRuntime) withDockerConfig(ctx context.Context, auth *Auth, args []string) error {
	if auth == nil {
		return d.execCommand(ctx, args)
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
	return d.execCommand(ctx, args)
}

func (d *dockerCompatibleRuntime) execCommand(ctx context.Context, args []string) error {
	c := exec.CommandContext(ctx, d.dockerCompatibleCommand, append(d.globalArgs, args...)...)
	out := bytes.NewBuffer(nil)
	errOut := bytes.NewBuffer(nil)
	c.Stdout = out
	c.Stderr = errOut
	err := c.Run()
	klog.Infof("execCommand: %s %v, out: %s, errOut: %s, err: %v", d.dockerCompatibleCommand, args, out.String(), errOut.String(), err)
	return err
}

func (d *dockerCompatibleRuntime) execCommandPOut(ctx context.Context, args []string, stdin io.Reader) (*bytes.Buffer, error) {
	c := exec.CommandContext(ctx, d.dockerCompatibleCommand, append(d.globalArgs, args...)...)
	out := bytes.NewBuffer(nil)
	errOut := bytes.NewBuffer(nil)
	c.Stdin = stdin
	c.Stdout = out
	c.Stderr = errOut
	err := c.Run()
	klog.Infof("execCommand: %s %v, out: %s, errOut: %s, err: %v", d.dockerCompatibleCommand, args, out.String(), errOut.String(), err)
	return out, err
}
