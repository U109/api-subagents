package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/cpacompat"
	"github.com/U109/api-subagents/internal/platform"
	"github.com/U109/api-subagents/internal/shared"
)

const maxResponse = 8 * 1024 * 1024

type Provider struct {
	Client              *http.Client
	FirstWait, IdleWait time.Duration
}

// NewProvider 复用连接池，模型请求禁止跟随重定向，避免凭据转发到其他主机。
func NewProvider() *Provider {
	return &Provider{Client: &http.Client{Transport: platform.DefaultTransport(), CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("拒绝模型接口重定向") }}}
}

// InitialHistory 按协议创建用户消息，后续续接复用同一历史格式。
func InitialHistory(p configstore.Profile, text string) []any {
	if p.Protocol == "gemini" {
		return []any{shared.Object{"role": "user", "parts": []any{shared.Object{"text": text}}}}
	}
	return []any{shared.Object{"role": "user", "content": text}}
}

// Endpoint 同时接受 API 根地址或完整端点，避免重复拼接路径。
func Endpoint(base, suffix string) string {
	if strings.HasSuffix(base, "/"+suffix) {
		return base
	}
	return base + "/" + suffix
}

// RequestSpec 将统一配置转换成各厂商协议，并保留推理签名所需选项。
func RequestSpec(p configstore.Profile, history []any, system string, tools []shared.Tool) (string, http.Header, shared.Object) {
	h := http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"text/event-stream, application/json"}}
	functions := []any{}
	for _, t := range tools {
		functions = append(functions, shared.Object{"name": t.Name, "description": t.Description, "parameters": t.Schema})
	}
	switch p.Protocol {
	case "anthropic":
		h.Set("x-api-key", p.APIKey)
		h.Set("anthropic-version", "2023-06-01")
		b := shared.Object{"model": p.Model, "stream": p.Stream, "max_tokens": p.MaxTokens, "system": system, "messages": history}
		if len(tools) > 0 {
			items := []any{}
			for _, t := range tools {
				items = append(items, shared.Object{"name": t.Name, "description": t.Description, "input_schema": t.Schema})
			}
			b["tools"] = items
		}
		return Endpoint(p.BaseURL, "messages"), h, b
	case "gemini":
		h.Set("x-goog-api-key", p.APIKey)
		base := p.BaseURL
		if i := strings.Index(base, "/models/"); i >= 0 && (strings.HasSuffix(base, "GenerateContent") || strings.HasSuffix(base, "generateContent")) {
			base = base[:i]
		}
		suffix := "generateContent"
		if p.Stream {
			suffix = "streamGenerateContent?alt=sse"
		}
		b := shared.Object{"systemInstruction": shared.Object{"parts": []any{shared.Object{"text": system}}}, "contents": history, "generationConfig": shared.Object{"maxOutputTokens": p.MaxTokens}}
		if len(tools) > 0 {
			items := []any{}
			for _, t := range tools {
				items = append(items, shared.Object{"name": t.Name, "description": t.Description, "parametersJsonSchema": t.Schema})
			}
			b["tools"] = []any{shared.Object{"functionDeclarations": items}}
		}
		return Endpoint(base, "models/"+url.PathEscape(strings.TrimPrefix(p.Model, "models/"))+":"+suffix), h, b
	case "responses":
		if p.APIKey != "" {
			h.Set("Authorization", "Bearer "+p.APIKey)
		}
		b := shared.Object{"model": p.Model, "stream": p.Stream, "instructions": system, "input": history, "store": false, "include": []string{"reasoning.encrypted_content"}, "max_output_tokens": p.MaxTokens}
		if len(tools) > 0 {
			items := []any{}
			for _, item := range functions {
				f := shared.Obj(item)
				f["type"] = "function"
				f["strict"] = false
				items = append(items, f)
			}
			b["tools"] = items
		}
		return Endpoint(p.BaseURL, "responses"), h, b
	default:
		if p.APIKey != "" {
			h.Set("Authorization", "Bearer "+p.APIKey)
		}
		messages := append([]any{shared.Object{"role": "system", "content": system}}, history...)
		b := shared.Object{"model": p.Model, "stream": p.Stream, "messages": messages, "max_tokens": p.MaxTokens}
		if len(tools) > 0 {
			items := []any{}
			for _, f := range functions {
				items = append(items, shared.Object{"type": "function", "function": f})
			}
			b["tools"] = items
		}
		return Endpoint(p.BaseURL, "chat/completions"), h, b
	}
}

// FetchReply 按首次数据与空闲时间计时，心跳也刷新活跃时间；不重发失败或断流请求。
func (p *Provider) FetchReply(ctx context.Context, profile configstore.Profile, endpoint string, headers http.Header, body shared.Object, progress func(shared.Object)) (shared.Object, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	first := p.FirstWait
	if first == 0 {
		first = time.Duration(profile.FirstTimeout) * time.Second
	}
	idle := p.IdleWait
	if idle == 0 {
		idle = time.Duration(profile.IdleTimeout) * time.Second
	}
	var mu sync.Mutex
	var timer *time.Timer
	finished := false
	sequence := 0
	// 阶段计时器只取消当前请求，整个任务时限由任务管理器独立控制。
	arm := func(wait time.Duration, code, message string) {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		if finished {
			return
		}
		sequence++
		generation := sequence
		timer = time.AfterFunc(wait, func() {
			mu.Lock()
			defer mu.Unlock()
			if !finished && generation == sequence {
				cancel(&shared.OpError{Code: code, Message: message})
			}
		})
	}
	defer func() {
		mu.Lock()
		finished = true
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
	}()
	if progress == nil {
		progress = func(shared.Object) {}
	}
	started := time.Now().UTC().Format(time.RFC3339Nano)
	progress(shared.Object{"phase": "waiting_response", "requestStartedAt": started, "receivedBytes": 0})
	arm(first, "FIRST_RESPONSE_TIMEOUT", "等待首个响应数据超时，可在高级设置中调整。")
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(shared.Marshal(body)))
	if err != nil {
		return nil, errors.New("API 请求地址无效。")
	}
	req.Header = headers
	res, err := p.Client.Do(req)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		return nil, fmt.Errorf("连接模型服务失败：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("模型 API 返回 HTTP %d：%s", res.StatusCode, readUpstreamError(res.Body))
	}
	streaming := strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream")
	acc := newAccumulator(profile.Protocol)
	decoder := sseDecoder{accept: acc.accept}
	if profile.Protocol == "compatible" {
		chat := NewChatStream(profile)
		acc = chat.acc
		decoder.accept = func(_ string, text string) error { _, err := chat.Accept(text); return err }
	}
	var raw bytes.Buffer
	buf := make([]byte, 16384)
	received := 0
	events := 0
	for {
		n, readErr := res.Body.Read(buf)
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		if n > 0 {
			received += n
			if received > maxResponse {
				return nil, errors.New("模型响应超过 8 MB。")
			}
			arm(idle, "STREAM_IDLE_TIMEOUT", "接收响应期间长时间没有新数据，可在高级设置中调整。")
			if streaming {
				if err = decoder.push(buf[:n], false); err != nil {
					return nil, err
				}
				events = decoder.events
			} else {
				raw.Write(buf[:n])
			}
			phase := "receiving_json"
			if streaming {
				phase = "streaming"
			}
			progress(shared.Object{"phase": phase, "requestStartedAt": started, "receivedBytes": received, "events": events})
			if acc.done {
				break
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, errors.New("模型连接中断，未执行未完成的工具调用。")
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	var result shared.Object
	if streaming {
		if err = decoder.push(nil, true); err != nil {
			return nil, err
		}
		if !acc.done {
			return nil, errors.New("模型流在完成前中断，未执行本轮工具调用。")
		}
		result = acc.result
	} else if json.Unmarshal(raw.Bytes(), &result) != nil || result == nil {
		return nil, errors.New("API 没有返回有效 JSON 或 SSE 流。")
	}
	if !streaming && profile.Protocol == "compatible" {
		data, _, err := NormalizeChatJSON(raw.Bytes(), profile)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal(data, &result)
	}
	progress(shared.Object{"phase": "response_received", "receivedBytes": received, "events": events})
	return result, nil
}

// readUpstreamError 只读取错误响应中的有限诊断字段，不把完整响应体写入日志或任务记录。
func readUpstreamError(body io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(body, 32*1024))
	if err != nil || len(data) == 0 {
		return "上游未提供错误详情，请检查地址、Key、模型和额度。"
	}
	var value shared.Object
	if json.Unmarshal(data, &value) == nil {
		if message := responseErrorMessage(value); message != "" && !strings.HasPrefix(message, "上游未提供") {
			return message
		}
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "上游未提供错误详情，请检查地址、Key、模型和额度。"
	}
	return shared.Clip(text, 500)
}

// Turn 只在完整、未截断的响应通过校验后提交历史，工具参数交给受限文件层处理。
func (p *Provider) Turn(ctx context.Context, profile configstore.Profile, history *[]any, system string, tools []shared.Tool, progress func(shared.Object)) (shared.Reply, error) {
	endpoint, headers, body := RequestSpec(profile, *history, system, tools)
	if profile.Protocol == "gemini" {
		cpacompat.CleanGeminiSchemas(body)
	}
	if err := ApplyReasoning(body, profile, profile.ReasoningEffort); err != nil {
		return shared.Reply{}, err
	}
	data, err := p.FetchReply(ctx, profile, endpoint, headers, body, progress)
	if err != nil {
		return shared.Reply{}, err
	}
	reply := shared.Reply{Calls: []shared.Call{}}
	additions := []any{}
	truncated := false
	switch profile.Protocol {
	case "responses":
		if data["error"] != nil || data["status"] == "failed" {
			return reply, errors.New("Responses API 报告请求失败：" + responseErrorMessage(data))
		}
		output, ok := data["output"].([]any)
		if !ok {
			return reply, errors.New("API 缺少 Responses output。")
		}
		additions = output
		for _, v := range output {
			x := shared.Obj(v)
			if x["type"] == "function_call" {
				reply.Calls = append(reply.Calls, shared.Call{ID: shared.Str(x["call_id"]), Name: shared.Str(x["name"]), Args: x["arguments"]})
			}
			if x["type"] == "message" {
				for _, part := range shared.Arr(x["content"]) {
					c := shared.Obj(part)
					if c["type"] == "output_text" {
						reply.Text += shared.Str(c["text"])
					}
					if c["type"] == "refusal" {
						reply.Text += shared.Str(c["refusal"])
					}
				}
			}
		}
		truncated = data["status"] == "incomplete"
	case "anthropic":
		content, ok := data["content"].([]any)
		if !ok {
			return reply, errors.New("API 缺少 Claude content。")
		}
		additions = append(additions, shared.Object{"role": "assistant", "content": content})
		for _, v := range content {
			x := shared.Obj(v)
			if x["type"] == "tool_use" {
				reply.Calls = append(reply.Calls, shared.Call{ID: shared.Str(x["id"]), Name: shared.Str(x["name"]), Args: x["input"]})
			}
			if x["type"] == "text" {
				reply.Text += shared.Str(x["text"])
			}
		}
		truncated = data["stop_reason"] == "max_tokens"
	case "gemini":
		candidates := shared.Arr(data["candidates"])
		if len(candidates) == 0 {
			return reply, errors.New("Gemini 未返回可用内容。")
		}
		candidate := shared.Obj(candidates[0])
		content := shared.Obj(candidate["content"])
		parts, ok := content["parts"].([]any)
		if !ok {
			return reply, errors.New("Gemini 缺少内容。")
		}
		additions = append(additions, content)
		for _, v := range parts {
			x := shared.Obj(v)
			if x["functionCall"] != nil {
				f := shared.Obj(x["functionCall"])
				reply.Calls = append(reply.Calls, shared.Call{ID: shared.Str(f["id"]), Name: shared.Str(f["name"]), Args: shared.Obj(f["args"])})
			}
			if x["thought"] != true {
				reply.Text += shared.Str(x["text"])
			}
		}
		truncated = candidate["finishReason"] == "MAX_TOKENS"
	default:
		choices := shared.Arr(data["choices"])
		if len(choices) == 0 {
			return reply, errors.New("API 缺少 Chat Completions choices。")
		}
		choice := shared.Obj(choices[0])
		message := shared.Obj(choice["message"])
		if len(message) == 0 {
			return reply, errors.New("API 缺少 assistant message。")
		}
		additions = append(additions, message)
		reply.Text = shared.Str(message["content"])
		if reply.Text == "" {
			reply.Text = shared.Str(message["refusal"])
		}
		for _, v := range shared.Arr(message["tool_calls"]) {
			x := shared.Obj(v)
			f := shared.Obj(x["function"])
			reply.Calls = append(reply.Calls, shared.Call{ID: shared.Str(x["id"]), Name: shared.Str(f["name"]), Args: f["arguments"]})
		}
		truncated = choice["finish_reason"] == "length"
	}
	if truncated {
		return reply, errors.New("模型输出被截断，请提高输出上限或缩小任务范围。")
	}
	if len(reply.Calls) > 24 {
		return reply, errors.New("单轮工具调用超过 24 次。")
	}
	for _, call := range reply.Calls {
		if call.Name == "" || (profile.Protocol != "gemini" && call.ID == "") {
			return reply, errors.New("工具调用缺少名称或 ID。")
		}
	}
	*history = append(*history, additions...)
	return reply, nil
}

// AddResults 按原调用 ID 回填工具结果，保留 Gemini 签名与 Responses 加密推理历史。
func AddResults(profile configstore.Profile, history *[]any, calls []shared.Call) {
	items := []any{}
	for _, c := range calls {
		switch profile.Protocol {
		case "anthropic":
			items = append(items, shared.Object{"type": "tool_result", "tool_use_id": c.ID, "content": string(shared.Marshal(c.Output))})
		case "gemini":
			f := shared.Object{"name": c.Name, "response": c.Output}
			if c.ID != "" {
				f["id"] = c.ID
			}
			items = append(items, shared.Object{"functionResponse": f})
		case "responses":
			items = append(items, shared.Object{"type": "function_call_output", "call_id": c.ID, "output": string(shared.Marshal(c.Output))})
		default:
			items = append(items, shared.Object{"role": "tool", "tool_call_id": c.ID, "content": string(shared.Marshal(c.Output))})
		}
	}
	if profile.Protocol == "anthropic" {
		*history = append(*history, shared.Object{"role": "user", "content": items})
	} else if profile.Protocol == "gemini" {
		*history = append(*history, shared.Object{"role": "user", "parts": items})
	} else {
		*history = append(*history, items...)
	}
}

// Probe 用明确的短请求测试连通性，不读取项目，也不宣称已验证模型工具能力。
func (p *Provider) Probe(ctx context.Context, profile configstore.Profile) (shared.Object, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	profile.MaxTokens = min(1024, profile.MaxTokens)
	history := InitialHistory(profile, "Reply with OK.")
	r, err := p.Turn(ctx, profile, &history, "This is a connection test.", nil, nil)
	if err != nil {
		return nil, err
	}
	return shared.Object{"ok": true, "reply": shared.Clip(configstore.Redact(r.Text, profile), 500)}, nil
}

// ListModels 有界查询模型目录，分页游标仅作为参数，不改变目标主机。
func (p *Provider) ListModels(ctx context.Context, profile configstore.Profile) (shared.Object, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	base := profile.BaseURL
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
		base = strings.TrimSuffix(base, suffix)
	}
	endpoint := Endpoint(base, "models")
	models := map[string]shared.Object{}
	cursor := ""
	seen := map[string]bool{}
	truncated := false
	for page := 0; page < 10; page++ {
		u, _ := url.Parse(endpoint)
		q := u.Query()
		if profile.Protocol == "anthropic" {
			q.Set("limit", "100")
			if cursor != "" {
				q.Set("after_id", cursor)
			}
		}
		if profile.Protocol == "gemini" {
			q.Set("pageSize", "100")
			if cursor != "" {
				q.Set("pageToken", cursor)
			}
		}
		u.RawQuery = q.Encode()
		req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		req.Header.Set("Accept", "application/json")
		switch profile.Protocol {
		case "anthropic":
			req.Header.Set("x-api-key", profile.APIKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		case "gemini":
			req.Header.Set("x-goog-api-key", profile.APIKey)
		default:
			if profile.APIKey != "" {
				req.Header.Set("Authorization", "Bearer "+profile.APIKey)
			}
		}
		res, err := p.Client.Do(req)
		if err != nil {
			return nil, errors.New("拉取模型列表失败或超时，请重试或手动输入。")
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
		res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return nil, fmt.Errorf("模型列表 HTTP %d，可检查连接或切换手动输入。", res.StatusCode)
		}
		var value shared.Object
		if readErr != nil || len(data) > maxResponse || json.Unmarshal(data, &value) != nil {
			return nil, errors.New("模型列表未返回有效的有界 JSON。")
		}
		key := "data"
		if profile.Protocol == "gemini" {
			key = "models"
		}
		entries, ok := value[key].([]any)
		if !ok {
			return nil, errors.New("接口未返回标准模型列表，可切换为手动输入。")
		}
		for _, v := range entries {
			entry := shared.Obj(v)
			id := shared.Str(entry["id"])
			if profile.Protocol == "gemini" {
				id = strings.TrimPrefix(shared.Str(entry["name"]), "models/")
				if methods, ok := entry["supportedGenerationMethods"].([]any); ok {
					supports := false
					for _, m := range methods {
						if m == "generateContent" {
							supports = true
						}
					}
					if !supports {
						continue
					}
				}
			}
			if strings.TrimSpace(id) == "" || len([]rune(id)) > 200 {
				continue
			}
			name := shared.Str(entry["display_name"])
			if name == "" {
				name = shared.Str(entry["displayName"])
			}
			if name == "" {
				name = id
			}
			models[id] = shared.Object{"id": id, "name": shared.Clip(name, 160)}
			if len(models) >= 1000 {
				truncated = true
				break
			}
		}
		cursor = ""
		if profile.Protocol == "anthropic" && value["has_more"] == true {
			cursor = shared.Str(value["last_id"])
			if cursor == "" {
				return nil, errors.New("模型列表分页信息不完整。")
			}
		}
		if profile.Protocol == "gemini" {
			cursor = shared.Str(value["nextPageToken"])
		}
		if cursor == "" || truncated {
			break
		}
		if len(cursor) > 4096 || seen[cursor] {
			return nil, errors.New("模型列表分页异常。")
		}
		seen[cursor] = true
		if page == 9 {
			truncated = true
		}
	}
	ids := []string{}
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := []any{}
	for _, id := range ids {
		items = append(items, models[id])
	}
	return shared.Object{"models": items, "truncated": truncated}, nil
}
