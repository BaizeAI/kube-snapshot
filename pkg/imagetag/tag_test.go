package imagetag

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestResolveImageRefTimeZone 按绝对时间验证自动 tag 使用东八区分钟时间，不依赖宿主机本地时区。
func TestResolveImageRefTimeZone(t *testing.T) {
	before := time.Now()
	got, err := ResolveImageRef("example.com/app:v1", "example.com/snap:{tag}", Options{AppendSnappedSuffix: true})
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(got, "-")
	if len(parts) != 4 {
		t.Fatalf("unexpected generated image reference: %s", got)
	}
	stamp, err := time.ParseInLocation("200601021504", parts[2], time.FixedZone("UTC+8", 8*60*60))
	if err != nil {
		t.Fatal(err)
	}
	if stamp.Before(before.Truncate(time.Minute)) || stamp.After(after.Truncate(time.Minute)) {
		t.Fatalf("generated time %s is not within the current UTC+8 minute", parts[2])
	}
}

// TestResolveImageRefGeneratedSuffix verifies replacement is limited to complete generated suffixes.
// 只有格式完整且位于 tag 末尾的生成后缀才会被替换；旧格式、大小写异常和部分匹配均保留为普通 tag。
func TestResolveImageRefGeneratedSuffix(t *testing.T) {
	for _, tt := range []struct {
		name, tag, prefix string
	}{
		{"standalone", "snapped-202611031737-60c830", ""},
		{"prefixed", "v1-snapped-202611031737-60c830", "v1-"},
		{"repeated standalone", "snapped-202611031737-60c830-snapped-202611041737-abcdef", ""},
		{"repeated prefixed", "v1-snapped-202611031737-60c830-snapped-202611041737-abcdef", "v1-"},
		{"legacy suffix remains part of the base", "v1-snapped-1704067200-abc12345", "v1-snapped-1704067200-abc12345-"},
		{"invalid timestamp", "snapped-20261103173x-60c830", "snapped-20261103173x-60c830-"},
		{"uppercase hex", "v1-snapped-202611031737-60C830", "v1-snapped-202611031737-60C830-"},
		{"partial marker", "v1-snapped-202611031737-60c830-extra", "v1-snapped-202611031737-60c830-extra-"},
		{"latest", "latest", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveImageRef("example.com/app:"+tt.tag, "example.com/snap:{tag}", Options{AppendSnappedSuffix: true})
			if err != nil {
				t.Fatal(err)
			}
			pattern := `^example\.com/snap:` + regexp.QuoteMeta(tt.prefix) + `snapped-[0-9]{12}-[0-9a-f]{6}$`
			if !regexp.MustCompile(pattern).MatchString(got) {
				t.Fatalf("got %q, want pattern %q", got, pattern)
			}
		})
	}
}

// TestResolveImageRefWithoutGeneratedSuffix preserves explicit and disabled-suffix tags.
// 关闭自动后缀或显式指定 ManualTag 时保留旧 tag 原值，多容器场景只追加容器名。
func TestResolveImageRefWithoutGeneratedSuffix(t *testing.T) {
	const tag = "snapped-202611031737-60c830"
	for _, tt := range []struct {
		name, format string
		opts         Options
		want         string
	}{
		{"disabled", "example.com/snap:{tag}", Options{}, tag},
		{"explicit template tag", "example.com/snap:release-2024-01", Options{}, "release-2024-01"},
		{"untagged template", "example.com/snap", Options{}, "latest"},
		{"manual", "example.com/snap:template", Options{ManualTag: tag, AppendSnappedSuffix: true}, tag},
		{
			"manual multi-container", "example.com/snap:{tag}",
			Options{ManualTag: tag, ContainerCount: 2, ContainerName: "sidecar", AppendSnappedSuffix: true},
			tag + "-sidecar",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveImageRef("example.com/app:"+tag, tt.format, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if want := "example.com/snap:" + tt.want; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
