package providers

import (
	"encoding/json"
	"errors"
	"strings"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// ChatStream 为网关复用插件的完整性检查，并把结构化思考转为 CPA 能识别的字段。
type ChatStream struct {
	acc        *accumulator
	normalizer chatNormalizer
}

type chatNormalizer struct {
	details      []any
	minimax      bool
	reasonSource string
}

// NewChatStream 每次生成独立的流状态，避免并发模型互相拼接文本或签名。
func NewChatStream(p configstore.Profile) *ChatStream {
	return &ChatStream{acc: newAccumulator("compatible"), normalizer: chatNormalizer{minimax: Compatibility(p) == "minimax"}}
}

// Accept 验证并积累一帧，返回供 CPA 转换的 JSON；工具仅在 finish_reason 和 DONE 齐全后可回放。
func (s *ChatStream) Accept(text string) (string, error) {
	if s.acc.done {
		return text, nil
	}
	if strings.TrimSpace(text) != "[DONE]" {
		var data shared.Object
		if json.Unmarshal([]byte(text), &data) != nil || data == nil {
			return "", errors.New("模型流包含无效 JSON")
		}
		s.normalizer.normalize(data, true)
		text = string(shared.Marshal(data))
	}
	err := s.acc.accept("", text)
	if err == nil && s.acc.done && len(s.normalizer.details) > 0 {
		// 只在完成时快照一次，避免每个文本 token 都重新序列化整段思考历史。
		s.acc.message["reasoning_details"] = cloneDetails(s.normalizer.details)
	}
	return text, err
}

// Message 只暴露通过流终态校验的第一候选消息，供网关保留工具回合需要的原始元数据。
func (s *ChatStream) Message() shared.Object {
	if !s.acc.done || s.acc.reason == "length" || s.acc.reason == "content_filter" {
		return nil
	}
	return s.acc.message
}

// NormalizeChatJSON 将非流式结构化思考补为 reasoning_content；正文、原始 details 和工具签名均保留。
func NormalizeChatJSON(data []byte, p configstore.Profile) ([]byte, shared.Object, error) {
	var value shared.Object
	if json.Unmarshal(data, &value) != nil || value == nil {
		return nil, nil, errors.New("模型没有返回有效 JSON")
	}
	n := chatNormalizer{minimax: Compatibility(p) == "minimax"}
	n.normalize(value, false)
	for _, raw := range shared.Arr(value["choices"]) {
		choice := shared.Obj(raw)
		if shared.Int(choice["index"]) == 0 {
			if choice["finish_reason"] == nil || shared.Str(choice["finish_reason"]) == "" || choice["message"] == nil {
				return nil, nil, errors.New("模型没有返回完整结束信息")
			}
			return shared.Marshal(value), shared.Obj(choice["message"]), nil
		}
	}
	return nil, nil, errors.New("模型没有返回可用候选消息")
}

// normalize 仅把独立 reasoning_details 的文本接入思考通道；绝不按标签删除或解释正文。
func (n *chatNormalizer) normalize(data shared.Object, stream bool) {
	for _, raw := range shared.Arr(data["choices"]) {
		choice := shared.Obj(raw)
		if shared.Int(choice["index"]) != 0 {
			continue
		}
		key := "message"
		if stream {
			key = "delta"
		}
		message := shared.Obj(choice[key])
		before := reasoningDetailsText(n.details)
		n.details = mergeReasoningDetails(n.details, shared.Arr(message["reasoning_details"]), stream && !n.minimax)
		after := reasoningDetailsText(n.details)
		if n.reasonSource == "" {
			if shared.Str(message["reasoning_content"]) != "" || shared.Str(message["reasoning"]) != "" {
				n.reasonSource = "standard"
			} else if after != "" {
				n.reasonSource = "details"
			}
		}
		if n.reasonSource == "details" && strings.HasPrefix(after, before) {
			message["reasoning_content"] = strings.TrimPrefix(after, before)
			delete(message, "reasoning")
		}
	}
}

// mergeReasoningDetails 依 index 合并厂商文本与签名；MiniMax 返回累计快照，其余接口按标准 delta 拼接。
func mergeReasoningDetails(existing, incoming []any, delta bool) []any {
	for position, raw := range incoming {
		part := shared.Obj(raw)
		index := position
		if part["index"] != nil {
			index = shared.Int(part["index"])
		}
		if index < 0 || index >= 128 {
			continue
		}
		for len(existing) <= index {
			existing = append(existing, shared.Object{})
		}
		target := shared.Obj(existing[index])
		for key, value := range part {
			if delta && (key == "text" || key == "data" || key == "signature") {
				target[key] = shared.Str(target[key]) + shared.Str(value)
			} else {
				target[key] = value
			}
		}
	}
	return existing
}

// reasoningDetailsText 提取公开的思考文本；加密签名或未知结构不会当成自然语言输出。
func reasoningDetailsText(details []any) string {
	var text strings.Builder
	for _, raw := range details {
		text.WriteString(shared.Str(shared.Obj(raw)["text"]))
	}
	return text.String()
}

// cloneDetails 切断快照与后续流状态的引用，避免已保存工具回合被后续帧改写。
func cloneDetails(details []any) []any {
	var result []any
	_ = json.Unmarshal(shared.Marshal(details), &result)
	return result
}
