package usage

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

const observationBytes = 1024 * 1024

// observer 有界旁观原始 JSON/SSE，事件超过预算时记为不可读，不缓冲完整回答或改变透传字节。
type observer struct {
	protocol                                       string
	streaming                                      bool
	buffer, line, event                            []byte
	overflow, lineOverflow, eventOverflow, afterCR bool
	terminal                                       string
	tokens                                         Tokens
	reported, totalReported                        bool
}

// feed 接受任意拆包，SSE 支持 CRLF、多行 data 和 usage-only 帧；快照覆盖而不是逐事件累加。
func (o *observer) feed(data []byte) {
	if !o.streaming {
		if !o.overflow && len(o.buffer)+len(data) <= observationBytes {
			o.buffer = append(o.buffer, data...)
		} else {
			o.overflow = true
			o.buffer = nil
		}
		return
	}
	for _, b := range data {
		if o.afterCR {
			o.afterCR = false
			if b == '\n' {
				continue
			}
		}
		if b == '\n' || b == '\r' {
			o.afterCR = b == '\r'
			line := bytes.TrimSuffix(o.line, []byte{'\r'})
			if len(line) == 0 {
				if !o.eventOverflow && len(o.event) > 0 {
					o.accept(bytes.TrimSpace(o.event))
				}
				if o.eventOverflow {
					o.overflow = true
				}
				o.event = o.event[:0]
				o.eventOverflow = false
			} else if bytes.HasPrefix(line, []byte("data:")) {
				if o.lineOverflow || len(o.event)+len(line) > observationBytes {
					o.eventOverflow = true
					o.event = nil
				} else if !o.eventOverflow {
					o.event = append(o.event, bytes.TrimSpace(line[5:])...)
					o.event = append(o.event, '\n')
				}
			}
			o.line = o.line[:0]
			o.lineOverflow = false
			continue
		}
		if len(o.line) < observationBytes {
			o.line = append(o.line, b)
		} else {
			o.lineOverflow = true
		}
	}
}

// finish 只解析完整非流式 JSON；尾部残缺 SSE 不冒充完整事件，结束状态与用量质量分别保留。
func (o *observer) finish() {
	if !o.streaming && !o.overflow {
		o.accept(o.buffer)
	}
	o.buffer = nil
	o.line = nil
	o.event = nil
}

// count 严格接受非负整数；缺失、分数、溢出与负数保持未知，不转成零。
func count(value any) *int64 {
	text, ok := value.(json.Number)
	if !ok {
		return nil
	}
	n, err := strconv.ParseInt(string(text), 10, 64)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

// object 仅取 JSON 对象字段，不尝试在响应正文或字符串中搜索伪造的 usage 文本。
func object(value any) map[string]any { result, _ := value.(map[string]any); return result }

// addKnown 在所有组成项都已上报且不溢出时归一化包含式口径，任何缺项都不猜成零。
func addKnown(values ...*int64) *int64 {
	var total int64
	for _, value := range values {
		if value == nil || *value > int64(^uint64(0)>>1)-total {
			return nil
		}
		total += *value
	}
	return &total
}

// assign 只覆盖本次事件实际给出的数值，保留之前的累计值；重复终态不会重复计量。
func assign(target **int64, value any) {
	if n := count(value); n != nil {
		*target = n
	}
}

// accept 映射四种原始协议；缓存和推理作为子集，Claude 的缓存计数先并入统一输入。
func (o *observer) accept(data []byte) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		if o.protocol == "compatible" && o.terminal != "failed" {
			o.terminal = "success"
		}
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil {
		return
	}
	kind, _ := value["type"].(string)
	root := value
	if response := object(value["response"]); response != nil {
		root = response
	}
	usage := object(root["usage"])
	if o.protocol == "anthropic" {
		if message := object(value["message"]); message != nil {
			root = message
			usage = object(message["usage"])
		}
		if kind == "message_stop" && o.terminal != "failed" {
			o.terminal = "success"
		}
		if kind == "error" {
			o.terminal = "failed"
		}
		if usage != nil {
			o.reported = true
			// Claude 普通输入不含缓存；初始 usage 的标准缓存字段不存在时保留未知。
			assign(&o.tokens.Read, usage["cache_read_input_tokens"])
			assign(&o.tokens.Write, usage["cache_creation_input_tokens"])
			if input := count(usage["input_tokens"]); input != nil {
				if o.tokens.Read != nil && o.tokens.Write != nil {
					o.tokens.Input = addKnown(input, o.tokens.Read, o.tokens.Write)
				} else {
					o.tokens.Input = nil
				}
			}
			assign(&o.tokens.Output, usage["output_tokens"])
		}
	} else if o.protocol == "gemini" {
		usage = object(value["usageMetadata"])
		if usage != nil {
			o.reported = true
			assign(&o.tokens.Input, usage["promptTokenCount"])
			assign(&o.tokens.Read, usage["cachedContentTokenCount"])
			assign(&o.tokens.Reasoning, usage["thoughtsTokenCount"])
			candidate := count(usage["candidatesTokenCount"])
			reportedTotal := count(usage["totalTokenCount"])
			if candidate != nil {
				if o.tokens.Reasoning != nil {
					o.tokens.Output = addKnown(candidate, o.tokens.Reasoning)
				} else if reportedTotal != nil && o.tokens.Input != nil && *reportedTotal >= *o.tokens.Input && *reportedTotal-*o.tokens.Input >= *candidate {
					output := *reportedTotal - *o.tokens.Input
					o.tokens.Output = &output
				} else {
					o.tokens.Output = nil
				}
			}
			if n := count(usage["totalTokenCount"]); n != nil {
				o.tokens.Total = n
				o.totalReported = true
			}
		}
		if candidates, ok := value["candidates"].([]any); ok {
			for _, item := range candidates {
				if reason, ok := object(item)["finishReason"].(string); ok && reason != "" && o.terminal != "failed" {
					o.terminal = "success"
				}
			}
		}
	} else if usage != nil {
		o.reported = true
		if o.protocol == "compatible" {
			assign(&o.tokens.Input, usage["prompt_tokens"])
			assign(&o.tokens.Output, usage["completion_tokens"])
			assign(&o.tokens.Read, object(usage["prompt_tokens_details"])["cached_tokens"])
			assign(&o.tokens.Write, object(usage["prompt_tokens_details"])["cache_write_tokens"])
			assign(&o.tokens.Reasoning, object(usage["completion_tokens_details"])["reasoning_tokens"])
		} else {
			assign(&o.tokens.Input, usage["input_tokens"])
			assign(&o.tokens.Output, usage["output_tokens"])
			assign(&o.tokens.Read, object(usage["input_tokens_details"])["cached_tokens"])
			assign(&o.tokens.Write, object(usage["input_tokens_details"])["cache_write_tokens"])
			assign(&o.tokens.Reasoning, object(usage["output_tokens_details"])["reasoning_tokens"])
		}
		if n := count(usage["total_tokens"]); n != nil {
			o.tokens.Total = n
			o.totalReported = true
		}
	}
	if !o.totalReported {
		o.tokens.Total = addKnown(o.tokens.Input, o.tokens.Output)
	}
	if (kind == "response.completed" || root["status"] == "completed") && o.terminal != "failed" {
		o.terminal = "success"
	}
	if kind == "response.failed" || kind == "response.incomplete" || kind == "error" || root["status"] == "failed" || root["status"] == "incomplete" {
		o.terminal = "failed"
	}
	if !o.streaming && value["error"] == nil && o.terminal == "" {
		o.terminal = "success"
	}
	if value["error"] != nil {
		o.terminal = "failed"
	}
	// 只保留固定终态枚举，任意上游字符串不会进入日志。
	o.terminal = strings.TrimSpace(o.terminal)
}
