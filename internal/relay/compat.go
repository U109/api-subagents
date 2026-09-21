package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/U109/api-subagents/internal/cpacompat"
	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
)

// isTerminal 解析事件类型判断流是否结束，避免回答正文中的协议名触发提前截断。
func isTerminal(chunk []byte) bool {
	terminal := false
	decoder := providers.NewEventDecoder(func(event, data string) error {
		var value shared.Object
		if json.Unmarshal([]byte(data), &value) == nil {
			kind := shared.Str(value["type"])
			terminal = terminal || kind == "response.completed" || kind == "response.incomplete"
		}
		return nil
	})
	_ = decoder.Feed(chunk, true)
	return terminal
}

// normalizeUsage 补齐 Codex 严格解析的用量计数与明细；未知统计记为零，不推算或改写实际计费数量。
func normalizeUsage(response shared.Object) {
	usage := shared.Obj(response["usage"])
	if len(usage) == 0 {
		return
	}
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if usage[key] == nil {
			usage[key] = 0
		}
	}
	input := shared.Obj(usage["input_tokens_details"])
	output := shared.Obj(usage["output_tokens_details"])
	if input["cached_tokens"] == nil {
		input["cached_tokens"] = 0
	}
	if output["reasoning_tokens"] == nil {
		output["reasoning_tokens"] = 0
	}
	usage["input_tokens_details"], usage["output_tokens_details"] = input, output
}

// normalizeEvents 适配部分兼容服务省略的用量字段与 output_item.done，确保 Codex 能接收最终消息。
// 已发出的完成项不会重复；回答正文只作为 JSON 内容处理，不参与协议状态判断。
func normalizeEvents(chunk []byte, completed map[string]bool, patchAlias string, aliases cpacompat.ToolAliases) []byte {
	var output bytes.Buffer
	decoder := providers.NewEventDecoder(func(event, data string) error {
		var value shared.Object
		if json.Unmarshal([]byte(data), &value) != nil {
			return errors.New("响应事件无效")
		}
		restorePatchTool(value, patchAlias)
		aliases.Restore(value)
		kind := shared.Str(value["type"])
		if kind == "" {
			kind = event
		}
		if kind == "response.output_item.done" {
			completed[shared.Str(shared.Obj(value["item"])["id"])] = true
		}
		if response := shared.Obj(value["response"]); len(response) > 0 {
			normalizeUsage(response)
			if kind == "response.completed" || kind == "response.incomplete" {
				for index, raw := range shared.Arr(response["output"]) {
					item := shared.Obj(raw)
					id := shared.Str(item["id"])
					if !completed[id] {
						fmt.Fprintf(&output, "event: response.output_item.done\ndata: %s\n\n", shared.Marshal(shared.Object{"type": "response.output_item.done", "output_index": index, "item": item}))
						completed[id] = true
					}
				}
			}
		}
		fmt.Fprintf(&output, "event: %s\ndata: %s\n\n", kind, shared.Marshal(value))
		return nil
	})
	if decoder.Feed(chunk, true) != nil {
		return chunk
	}
	return output.Bytes()
}

// claudeTranscript 将完整 Claude JSON 还原为事件序列，适配 CPA 非流式转换器的输入契约。
// 保留工具参数、思考签名、引用及停止原因；不再次请求上游，也不丢弃无法识别的内容块。
func claudeTranscript(data []byte) ([]byte, error) {
	var message shared.Object
	if json.Unmarshal(data, &message) != nil || message["type"] != "message" || shared.Str(message["id"]) == "" || shared.Str(message["stop_reason"]) == "" {
		return nil, errors.New("Claude 没有返回完整消息。")
	}
	var output bytes.Buffer
	send := func(event shared.Object) {
		fmt.Fprintf(&output, "event: %s\ndata: %s\n\n", event["type"], shared.Marshal(event))
	}
	start := shared.Object{}
	for k, v := range message {
		start[k] = v
	}
	start["content"] = []any{}
	start["stop_reason"] = nil
	send(shared.Object{"type": "message_start", "message": start})
	for index, raw := range shared.Arr(message["content"]) {
		block := shared.Obj(raw)
		delta := shared.Object{}
		switch block["type"] {
		case "text":
			delta = shared.Object{"type": "text_delta", "text": block["text"]}
		case "thinking":
			delta = shared.Object{"type": "thinking_delta", "thinking": block["thinking"]}
		case "tool_use":
			delta = shared.Object{"type": "input_json_delta", "partial_json": string(shared.Marshal(block["input"]))}
		case "redacted_thinking", "server_tool_use", "web_search_tool_result":
		default:
			return nil, errors.New("Claude 返回了尚不支持的内容类型。")
		}
		send(shared.Object{"type": "content_block_start", "index": index, "content_block": block})
		if len(delta) > 0 {
			send(shared.Object{"type": "content_block_delta", "index": index, "delta": delta})
		}
		for _, citation := range shared.Arr(block["citations"]) {
			send(shared.Object{"type": "content_block_delta", "index": index, "delta": shared.Object{"type": "citations_delta", "citation": citation}})
		}
		send(shared.Object{"type": "content_block_stop", "index": index})
	}
	send(shared.Object{"type": "message_delta", "delta": shared.Object{"stop_reason": message["stop_reason"], "stop_sequence": message["stop_sequence"]}, "usage": message["usage"]})
	send(shared.Object{"type": "message_stop"})
	return output.Bytes(), nil
}
