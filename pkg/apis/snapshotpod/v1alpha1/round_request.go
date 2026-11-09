package v1alpha1

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// RoundRequestAnnotation 在 SnapshotPod 元数据中保存最近一次手动触发的完整请求。
	RoundRequestAnnotation = "snapshot-pod.baizeai.io/round-request"
	// BaizeImageConfigAnnotation 保存 Baize 展示任务时使用的镜像元数据。
	BaizeImageConfigAnnotation = "baize.io/image-config"
	// RestoreCredentialSourceAnnotation 标记恢复镜像时应读取任务自身凭据的任务。
	RestoreCredentialSourceAnnotation = "snapshot-pod.baizeai.io/restore-credential-source"
	// RestoreCredentialSourceTask 表示凭据引用取自 Task.Spec，不继承当前长期配置。
	RestoreCredentialSourceTask = "task"
)

// RoundRequest 通过注解将一次触发轮次与各容器最终地址、凭据引用绑定，无需扩展 CRD schema。
// 调用方须将本请求与 Spec.TriggerRound 在同一次写入中提交；它不覆盖长期镜像配置。
type RoundRequest struct {
	// Round 标识所属的 TriggerRound；仅与当前轮次匹配时才作为任务创建输入。
	Round int32 `json:"round"`
	// Images 同时固定本轮容器清单及各容器最终完整地址，控制器不能再次生成 tag。
	Images map[string]string `json:"images"`
	// RegistrySecretRef 只保存本轮 Secret 名称引用。nil 表示请求缺字段，显式空串表示不指定凭据。
	RegistrySecretRef *string `json:"registrySecretRef"`
	// TaskAnnotations 随任务创建一并复制，避免异步补写导致展示元数据与实际 tag 不一致。
	TaskAnnotations map[string]map[string]string `json:"taskAnnotations,omitempty"`
}

// DecodeRoundRequest 在元数据入口解码并校验协议结构，不查询 Pod 或远端镜像仓库。
// 合法的历史轮次仍可返回，由调用方判断是否生效；损坏或超前请求必须显式报错。
func DecodeRoundRequest(raw string, triggerRound int32) (RoundRequest, error) {
	var request RoundRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return RoundRequest{}, fmt.Errorf("decode round request: %w", err)
	}
	if request.Round <= 0 {
		return RoundRequest{}, fmt.Errorf("round request round must be positive")
	}
	if request.Round > triggerRound {
		return RoundRequest{}, fmt.Errorf("round request %d is ahead of trigger round %d", request.Round, triggerRound)
	}
	if len(request.Images) == 0 {
		return RoundRequest{}, fmt.Errorf("round request must include at least one image")
	}
	// 指针区分“未提供凭据字段”与“明确不指定凭据”，禁止后者意外继承长期 Secret。
	if request.RegistrySecretRef == nil {
		return RoundRequest{}, fmt.Errorf("round request must specify registrySecretRef; use an empty string for no secret")
	}
	for container, image := range request.Images {
		if strings.TrimSpace(container) == "" || strings.TrimSpace(image) == "" {
			return RoundRequest{}, fmt.Errorf("round request contains an empty container or image reference")
		}
	}
	// 只允许复制协议约定的展示信息，避免透传任意注解覆盖任务自身的控制标记。
	for container, annotations := range request.TaskAnnotations {
		if _, ok := request.Images[container]; !ok {
			return RoundRequest{}, fmt.Errorf("task annotations reference unselected container %q", container)
		}
		for key, value := range annotations {
			if key != BaizeImageConfigAnnotation || strings.TrimSpace(value) == "" {
				return RoundRequest{}, fmt.Errorf("round request contains unsupported task annotation %q", key)
			}
		}
	}
	return request, nil
}
