// Package imagetag 为调用方预检与控制器提供共享的快照镜像命名规则，不执行仓库查询。
package imagetag

import (
	"fmt"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/google/uuid"
)

// Options 描述模板渲染后的本次命名策略，手动 tag 的优先级高于自动后缀开关。
type Options struct {
	// ManualTag 非空时替换模板 tag，不附加时间或随机串。
	ManualTag string
	// ContainerName 仅在本次选择多个容器且指定 ManualTag 时追加到 tag 末尾。
	ContainerName string
	// ContainerCount 是本次实际选择的容器数，而非 Pod 的容器总数。
	ContainerCount int
	// AppendSnappedSuffix 仅在 ManualTag 为空时生效；旧调用关闭它即可保留模板 tag。
	AppendSnappedSuffix bool
}

// ResolveImageRef 渲染镜像模板，再按手动优先或自动后缀规则生成完整目标地址。
// 自动命名包含随机值，调用方必须保存返回结果并复用于预检、提交和任务重试。
func ResolveImageRef(originImage, format string, options Options) (string, error) {
	originRef, err := parseReference(originImage)
	if err != nil {
		return "", fmt.Errorf("parse origin image %q: %w", originImage, err)
	}

	domain := reference.Domain(originRef)
	repository := reference.Path(originRef)
	lastRepository := repository
	if _, after, ok := strings.Cut(repository, "/"); ok {
		lastRepository = after
	}
	if index := strings.LastIndex(lastRepository, "/"); index >= 0 {
		lastRepository = lastRepository[index+1:]
	}

	originTag := ""
	// 沿用已有模板语义：digest 来源可通过 {tag} 使用其十六进制部分。
	switch image := originRef.(type) {
	case reference.NamedTagged:
		originTag = image.Tag()
	case reference.Canonical:
		originTag = image.Digest().Hex()
	}

	// 只替换模板明确引用的变量；例如源镜像为 app:v0.31.1 时，:{tag} 才会带入 v0.31.1。
	// 未写 {tag} 时不额外继承源版本；手动指定的 tag 则在模板渲染后覆盖它。
	format = strings.NewReplacer(
		"{domain}", domain,
		"{repository}", repository,
		"{last_repository}", lastRepository,
		"{tag}", originTag,
	).Replace(format)
	target, err := parseReference(format)
	if err != nil {
		return "", fmt.Errorf("parse target image format %q: %w", format, err)
	}

	tag := options.ManualTag
	if tag != "" {
		// 单容器保留用户输入；多容器追加名称，避免同仓库中各任务覆盖同一 tag。
		if options.ContainerCount > 1 {
			if options.ContainerName == "" {
				return "", fmt.Errorf("container name is required for a multi-container snapshot")
			}
			tag += "-" + options.ContainerName
		}
	} else {
		baseTag := ""
		if named, ok := target.(reference.NamedTagged); ok {
			baseTag = named.Tag()
		}
		if options.AppendSnappedSuffix {
			tag = appendGeneratedSuffix(baseTag)
		} else {
			// 保留解析器补全 latest 的旧行为，不因引入手动命名协议改变旧调用。
			tag = baseTag
		}
	}
	if tag == "" {
		return "", fmt.Errorf("target image format %q must render a tag when automatic suffixes are disabled", format)
	}

	// 统一交由镜像引用库校验 tag 字符与长度；非法结果报错，不截断用户输入。
	tagged, err := reference.WithTag(target, tag)
	if err != nil {
		return "", fmt.Errorf("apply tag %q to target image: %w", tag, err)
	}
	return tagged.String(), nil
}

// parseReference 延续 Docker 解析语义：补全默认域名与 latest，并拒绝同时带 tag 和 digest 的引用。
func parseReference(raw string) (reference.Named, error) {
	parsed, err := reference.ParseNormalizedNamed(raw)
	if err != nil {
		return nil, err
	}
	_, hasTag := parsed.(reference.NamedTagged)
	_, hasDigest := parsed.(reference.Canonical)
	if hasTag && hasDigest {
		return nil, fmt.Errorf("image reference %q cannot include both tag and digest", raw)
	}
	return reference.TagNameOnly(parsed), nil
}

// appendGeneratedSuffix 剥离末尾连续的新版后缀，再追加东八区分钟时间和 6 位小写十六进制串。
// 历史格式保留为普通前缀；随机串只降低碰撞概率，不保证绝对唯一。
func appendGeneratedSuffix(baseTag string) string {
	for {
		trimmed, ok := stripGeneratedSuffix(baseTag)
		if !ok {
			break
		}
		baseTag = trimmed
	}

	// 固定使用 UTC+8，不依赖容器的 TZ 环境变量或时区数据库。
	suffix := "snapped-" + time.Now().In(time.FixedZone("UTC+8", 8*60*60)).Format("200601021504") + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:6]
	if baseTag == "" || baseTag == "latest" {
		return suffix
	}
	return baseTag + "-" + suffix
}

// stripGeneratedSuffix 只移除末尾符合新版结构的完整后缀，返回基础 tag 及是否匹配。
// 同时支持独立 snapped-... 和带前缀的 ...-snapped-...，避免从快照再次命名时叠加。
func stripGeneratedSuffix(tag string) (string, bool) {
	index := strings.LastIndex(tag, "-snapped-")
	base, suffix := "", tag
	if index >= 0 {
		base, suffix = tag[:index], tag[index+1:]
	} else if !strings.HasPrefix(tag, "snapped-") {
		return tag, false
	}

	if !strings.HasPrefix(suffix, "snapped-") {
		return tag, false
	}
	stampAndRandom := strings.TrimPrefix(suffix, "snapped-")
	// 按长度与字符匹配生成结构，不把任意包含 snapped- 的业务 tag 当成自动后缀。
	stamp, random, ok := strings.Cut(stampAndRandom, "-")
	if !ok || len(stamp) != 12 || len(random) != 6 {
		return tag, false
	}
	for _, digit := range stamp {
		if digit < '0' || digit > '9' {
			return tag, false
		}
	}
	for _, digit := range random {
		hexDigit := digit >= '0' && digit <= '9' || digit >= 'a' && digit <= 'f'
		if !hexDigit {
			return tag, false
		}
	}
	return base, true
}
