package runtime

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"github.com/Masterminds/semver/v3"
	"github.com/containerd/containerd/v2/defaults"
	"github.com/containerd/nerdctl/v2/pkg/api/types"
	"github.com/containerd/nerdctl/v2/pkg/clientutil"
	"github.com/containerd/nerdctl/v2/pkg/infoutil"
)

type containerdCompatibleRuntime struct {
	dockerCompatibleRuntime
	IsNerdctlVersionSufficient    bool
	IsContainerdVersionSufficient bool
}

const (
	MinNerdctlVersion    = "2.1.3"
	MinContainerdVersion = "2.0.0"
)

func parseVersion(str string) string {
	return strings.TrimPrefix(str, "nerdctl version ")
}

func NewContainerdCompatibleRuntime(cmd string, globalArgs ...string) (Runtime, error) {
	dc := dockerCompatibleRuntime{
		dockerCompatibleCommand: cmd,
		globalArgs:              globalArgs,
	}
	ctx := context.Background()
	out, err := dc.execCommandPOut(ctx, []string{"-v"}, nil)
	if err != nil {
		klog.Errorf("failed to get nerdctl version: %v", err)
		return nil, err
	}
	nerdctlVersion := semver.MustParse(parseVersion(strings.TrimSpace(out.String()))).GreaterThanEqual(semver.MustParse(MinNerdctlVersion))
	if !nerdctlVersion {
		klog.Warningf("`nerdctl commit --compression expects nerdctl %s or later, got nerdctl %v", MinNerdctlVersion, out.String())
	}
	client, ctx, cancel, err := clientutil.NewClient(ctx, "k8s.io", defaults.DefaultAddress)
	if err != nil {
		klog.Errorf("failed to create containerd client: %v", err)
		return nil, err
	}
	defer cancel()
	containerdVersion := true
	var sv *semver.Version
	if sv, err = infoutil.ServerSemVer(ctx, client); err != nil {
		klog.Errorf("failed to get containerd version: %v", err)
		return nil, err
	} else if sv.LessThan(semver.MustParse(MinContainerdVersion)) {
		klog.Warningf("`nerdctl commit --compression expects containerd %s or later, got containerd %v", MinContainerdVersion, sv)
		containerdVersion = false
	}
	klog.Infof("IsNerdctlVersionSufficient: %v, nerdctl version: %s , "+
		"IsContainerdVersionSufficient: %v, containerd version: %s",
		nerdctlVersion, out.String(), containerdVersion, sv.String())
	return &containerdCompatibleRuntime{
		IsNerdctlVersionSufficient:    nerdctlVersion,
		IsContainerdVersionSufficient: containerdVersion,
		dockerCompatibleRuntime:       dc,
	}, nil
}

func (c *containerdCompatibleRuntime) Commit(ctx context.Context, containerID, image string, pause bool, message, author string) error {
	args := []string{"commit", containerID, image, fmt.Sprintf("--pause=%v", pause)}
	if message != "" {
		args = append(args, "--message", message)
	}
	if author != "" {
		args = append(args, "--author", author)
	}
	if c.IsNerdctlVersionSufficient && c.IsContainerdVersionSufficient {
		args = append(args, "--compression", string(types.Zstd))
	}
	return c.execCommand(ctx, args, nil)
}
