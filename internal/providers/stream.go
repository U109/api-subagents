package providers

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/U109/api-subagents/internal/shared"
)

// EventDecoder 为本地 Responses 网关复用同一套有边界的 SSE 事件解码。
type EventDecoder struct{ decoder sseDecoder }

// NewEventDecoder 接收完整事件回调，调用者只负责协议转换，无需自行处理 UTF-8 分块。
func NewEventDecoder(accept func(string, string) error) *EventDecoder {
	return &EventDecoder{decoder: sseDecoder{accept: accept}}
}

// Feed 输入网络字节；final 表示 EOF，刷新没有空行结尾的最后一个事件。
func (d *EventDecoder) Feed(data []byte, final bool) error { return d.decoder.push(data, final) }

type sseDecoder struct {
	pending string
	event   string
	data    []string
	events  int
	accept  func(string, string) error
}

// line 消费单个 SSE 字段；忽略心跳，将多行 data 合成一个完整事件。
func (d *sseDecoder) line(line string) error {
	if line == "" {
		if len(d.data) > 0 {
			d.events++
			if err := d.accept(d.event, strings.Join(d.data, "\n")); err != nil {
				return err
			}
		}
		d.event = ""
		d.data = nil
		return nil
	}
	if strings.HasPrefix(line, ":") {
		return nil
	}
	key, value, found := strings.Cut(line, ":")
	if !found {
		value = ""
	}
	value = strings.TrimPrefix(value, " ")
	if key == "event" {
		d.event = value
	}
	if key == "data" {
		d.data = append(d.data, value)
	}
	return nil
}

// push 逐字节累积 UTF-8 和 CRLF 边界，EOF 时处理没有空行结尾的最后事件。
func (d *sseDecoder) push(data []byte, final bool) error {
	d.pending += string(data)
	for {
		index := strings.IndexAny(d.pending, "\r\n")
		if index < 0 {
			break
		}
		if !final && d.pending[index] == '\r' && index == len(d.pending)-1 {
			break
		}
		width := 1
		if d.pending[index] == '\r' && index+1 < len(d.pending) && d.pending[index+1] == '\n' {
			width = 2
		}
		if err := d.line(d.pending[:index]); err != nil {
			return err
		}
		d.pending = d.pending[index+width:]
	}
	if final {
		if d.pending != "" {
			if err := d.line(d.pending); err != nil {
				return err
			}
			d.pending = ""
		}
		return d.line("")
	}
	return nil
}

type contentBlock struct {
	value            shared.Object
	partial          string
	hasJSON, stopped bool
}

type accumulator struct {
	protocol string
	done     bool
	result   shared.Object
	message  shared.Object
	reason   string
	calls    map[int]shared.Object
	blocks   map[int]*contentBlock
	claude   shared.Object
	parts    []any
}

// newAccumulator 创建仅保存当前一轮响应的状态，工具调用须等明确结束事件后才能取出。
func newAccumulator(protocol string) *accumulator {
	return &accumulator{protocol: protocol, message: shared.Object{"role": "assistant", "content": ""}, calls: map[int]shared.Object{}, blocks: map[int]*contentBlock{}, parts: []any{}}
}

// sortedKeys 按服务端索引恢复工具调用和 Claude 内容块的原始顺序。
func sortedKeys[T any](items map[int]T) []int {
	keys := []int{}
	for k := range items {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

// accept 合并协议事件并核对结束标记，保留思考签名；服务端错误仅返回固定描述。
func (a *accumulator) accept(event, text string) error {
	if a.done {
		return nil
	}
	if strings.TrimSpace(text) == "[DONE]" {
		if a.protocol != "compatible" || a.reason == "" {
			return errors.New("模型流缺少完整结束信息。")
		}
		calls := []any{}
		for _, key := range sortedKeys(a.calls) {
			call := a.calls[key]
			if shared.Str(call["id"]) == "" || shared.Str(shared.Obj(call["function"])["name"]) == "" {
				return errors.New("模型流工具调用缺少 ID 或名称。")
			}
			calls = append(calls, call)
		}
		if len(calls) > 0 {
			a.message["tool_calls"] = calls
			if a.message["content"] == "" {
				a.message["content"] = nil
			}
		}
		a.result = shared.Object{"choices": []any{shared.Object{"message": a.message, "finish_reason": a.reason}}}
		a.done = true
		return nil
	}
	var data shared.Object
	if json.Unmarshal([]byte(text), &data) != nil || data == nil {
		return errors.New("模型流包含无效 JSON。")
	}
	kind := shared.Str(data["type"])
	if kind == "" {
		kind = event
	}
	if data["error"] != nil || kind == "error" || kind == "response.failed" {
		return errors.New("模型流报告请求失败，请检查服务状态、模型和额度。")
	}
	switch a.protocol {
	case "responses":
		if kind == "response.completed" || kind == "response.incomplete" {
			a.result = shared.Obj(data["response"])
			if _, ok := a.result["output"].([]any); !ok {
				return errors.New("Responses 流缺少完整 output。")
			}
			if kind == "response.incomplete" {
				a.result["status"] = "incomplete"
			}
			a.done = true
		}
	case "anthropic":
		switch kind {
		case "message_start":
			a.claude = shared.Obj(data["message"])
		case "content_block_start":
			index := shared.Int(data["index"])
			if data["index"] == nil || index < 0 || index > 100 || a.blocks[index] != nil {
				return errors.New("Claude 流内容索引无效。")
			}
			a.blocks[index] = &contentBlock{value: shared.Obj(data["content_block"])}
		case "content_block_delta", "content_block_stop":
			block := a.blocks[shared.Int(data["index"])]
			if block == nil || block.stopped {
				return errors.New("Claude 流内容块顺序无效。")
			}
			if kind == "content_block_stop" {
				if block.hasJSON {
					var args shared.Object
					if json.Unmarshal([]byte(block.partial), &args) != nil || args == nil {
						return errors.New("Claude 流工具参数不完整。")
					}
					block.value["input"] = args
				}
				block.stopped = true
			} else {
				delta := shared.Obj(data["delta"])
				field := map[string]string{"text_delta": "text", "thinking_delta": "thinking", "signature_delta": "signature"}[shared.Str(delta["type"])]
				if field != "" {
					block.value[field] = shared.Str(block.value[field]) + shared.Str(delta[field])
				}
				if delta["type"] == "input_json_delta" {
					block.hasJSON = true
					block.partial += shared.Str(delta["partial_json"])
				}
			}
		case "message_delta":
			for k, v := range shared.Obj(data["delta"]) {
				if a.claude != nil {
					a.claude[k] = v
				}
			}
		case "message_stop":
			if shared.Str(a.claude["stop_reason"]) == "" {
				return errors.New("Claude 流缺少结束信息。")
			}
			content := []any{}
			for _, index := range sortedKeys(a.blocks) {
				block := a.blocks[index]
				if !block.stopped {
					return errors.New("Claude 流内容块未完成。")
				}
				content = append(content, block.value)
			}
			a.claude["content"] = content
			a.result = a.claude
			a.done = true
		}
	case "gemini":
		if shared.Obj(data["promptFeedback"])["blockReason"] != nil {
			return errors.New("Gemini 请求被过滤。")
		}
		for _, v := range shared.Arr(data["candidates"]) {
			candidate := shared.Obj(v)
			if shared.Int(candidate["index"]) != 0 {
				continue
			}
			for _, item := range shared.Arr(shared.Obj(candidate["content"])["parts"]) {
				part := shared.Obj(item)
				if len(a.parts) > 0 {
					previous := shared.Obj(a.parts[len(a.parts)-1])
					pt, pok := previous["text"].(string)
					nt, nok := part["text"].(string)
					if pok && nok && (previous["thought"] == true) == (part["thought"] == true) && previous["thoughtSignature"] == nil && part["thoughtSignature"] == nil {
						previous["text"] = pt + nt
						continue
					}
				}
				a.parts = append(a.parts, part)
			}
			if shared.Str(candidate["finishReason"]) != "" {
				candidate["content"] = shared.Object{"role": "model", "parts": a.parts}
				a.result = shared.Object{"candidates": []any{candidate}}
				a.done = true
			}
			break
		}
	default:
		for _, v := range shared.Arr(data["choices"]) {
			choice := shared.Obj(v)
			if shared.Int(choice["index"]) != 0 {
				continue
			}
			delta := shared.Obj(choice["delta"])
			for _, field := range []string{"content", "refusal", "reasoning_content", "reasoning"} {
				if value, ok := delta[field].(string); ok {
					a.message[field] = shared.Str(a.message[field]) + value
				}
			}
			for _, v := range shared.Arr(delta["tool_calls"]) {
				part := shared.Obj(v)
				index := shared.Int(part["index"])
				if part["index"] == nil || index < 0 || index >= 24 {
					return errors.New("模型流工具调用索引无效或超过 24 次。")
				}
				call := a.calls[index]
				if call == nil {
					call = shared.Object{"id": "", "type": "function", "function": shared.Object{"name": "", "arguments": ""}}
					a.calls[index] = call
				}
				call["id"] = shared.Str(call["id"]) + shared.Str(part["id"])
				if extra := part["extra_content"]; extra != nil {
					call["extra_content"] = extra
				}
				f := shared.Obj(call["function"])
				for _, field := range []string{"name", "arguments"} {
					f[field] = shared.Str(f[field]) + shared.Str(shared.Obj(part["function"])[field])
				}
			}
			if shared.Str(choice["finish_reason"]) != "" {
				a.reason = shared.Str(choice["finish_reason"])
			}
			break
		}
	}
	return nil
}
