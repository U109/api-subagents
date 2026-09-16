// Package relay 提供 Codex 主模型的本地 Responses 网关；转换委托给固定版本的 CPA SDK。
package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/builtin"
)

type State struct {
	Enabled     bool   `json:"enabled"`
	Model       string `json:"model"`
	ActiveModel string `json:"activeModel"`
	Address     string `json:"address"`
	Message     string `json:"message"`
	Requests    uint64 `json:"requests"`
}
type Relay struct {
	Store       *configstore.ConfigStore
	Codex       codexconfig.CodexConfig
	Client      *http.Client
	OnChange    func()
	mu          sync.RWMutex
	operation   sync.Mutex
	state       State
	token, host string
	server      *http.Server
	cancel      context.CancelFunc
	slots       chan struct{}
}

// New 创建默认关闭的网关，模型密钥只在请求时从本机配置解析。
func New(store *configstore.ConfigStore, codex codexconfig.CodexConfig) *Relay {
	return &Relay{Store: store, Codex: codex, Client: providers.NewProvider().Client, state: State{Message: "未开启：Codex 使用自己的模型配置"}, slots: make(chan struct{}, 8)}
}

// Snapshot 返回不含 Key、令牌和 Codex 备份的状态，供左侧连接标识显示。
func (r *Relay) Snapshot() State { r.mu.RLock(); defer r.mu.RUnlock(); return r.state }

// notify 发布网关状态变化，不对模型输出或请求内容做日志记录。
func (r *Relay) notify() {
	if r.OnChange != nil {
		r.OnChange()
	}
}

// Recover 在 App 上次意外退出后恢复自己的托管设置，保留其他用户编辑。
func (r *Relay) Recover() error {
	err := r.Codex.Restore()
	if err != nil {
		r.mu.Lock()
		r.state.Message = "上次的 Codex 配置需要恢复：" + err.Error()
		r.mu.Unlock()
	}
	return err
}

// Enable 验证已保存连接，先启动受鉴权保护的回环服务，再备份并接管 Codex 的模型设置。
func (r *Relay) Enable(model string) error {
	r.operation.Lock()
	defer r.operation.Unlock()
	config, err := r.Store.Read()
	if err != nil {
		return err
	}
	if _, err = configstore.ResolveProfile(config, model); err != nil {
		return err
	}
	r.mu.Lock()
	if r.state.Enabled {
		r.mu.Unlock()
		if err = r.Codex.WriteCatalog(config); err != nil {
			return err
		}
		r.mu.Lock()
		r.state.Model = model
		r.state.ActiveModel = ""
		r.state.Message = "已切换默认连接；Codex 选择“跟随 App 选择”即可使用"
		r.mu.Unlock()
		r.notify()
		return nil
	}
	r.mu.Unlock()
	if err = r.Codex.Restore(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return errors.New("无法启动本地模型网关。")
	}
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		listener.Close()
		return err
	}
	token := hex.EncodeToString(tokenBytes)
	host := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	r.mu.Lock()
	r.token = token
	r.host = host
	r.server = server
	r.cancel = cancel
	r.state = State{Enabled: true, Model: model, Address: "http://" + host + "/v1", Message: "已开启。首次使用请重启 Codex，在模型列表选择连接。"}
	r.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
	if err = r.Codex.Enable(config, model, port, token); err != nil {
		cancel()
		server.Close()
		r.mu.Lock()
		r.state = State{Message: "开启失败：" + err.Error()}
		r.server = nil
		r.cancel = nil
		r.mu.Unlock()
		return err
	}
	r.notify()
	return nil
}

// Disable 先安全恢复 Codex 设置，再关闭网关；恢复冲突时保持服务，避免让 Codex 指向失效地址。
func (r *Relay) Disable() error {
	r.operation.Lock()
	defer r.operation.Unlock()
	if err := r.Codex.Restore(); err != nil {
		return err
	}
	r.mu.Lock()
	server, cancel := r.server, r.cancel
	r.server = nil
	r.cancel = nil
	r.state = State{Message: "已关闭，已恢复 Codex 原有模型设置；请重启 Codex"}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if server != nil {
		server.Close()
	}
	r.notify()
	return nil
}

// RefreshCatalog 保存模型配置后更新可选列表，Codex 重启后读取；当前连接被删除时自动关闭并恢复。
func (r *Relay) RefreshCatalog() error {
	state := r.Snapshot()
	if !state.Enabled {
		return nil
	}
	config, err := r.Store.Read()
	if err != nil {
		return err
	}
	if _, exists := config.Models[state.Model]; !exists {
		return r.Disable()
	}
	return r.Codex.WriteCatalog(config)
}

// ServeHTTP 限定回环 Host 和固定路径，拒绝浏览器跨站访问及未持有本地随机令牌的请求。
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	state, host, token := r.state, r.host, r.token
	r.mu.RUnlock()
	w.Header().Set("Cache-Control", "no-store")
	if req.Host != host || req.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(req.Header.Get("X-Api-Subagents-Token")), []byte(token)) != 1 {
		replyError(w, 403, "本地网关拒绝未授权请求。")
		return
	}
	if !state.Enabled {
		replyError(w, 503, "挟持模式已关闭。")
		return
	}
	if req.Method == "GET" && req.URL.Path == "/v1/models" {
		config, err := r.Store.Read()
		if err != nil {
			replyError(w, 500, err.Error())
			return
		}
		items := []any{shared.Object{"id": "api-subagents", "object": "model", "owned_by": "local"}}
		for name := range config.Models {
			items = append(items, shared.Object{"id": "api-subagents/" + name, "object": "model", "owned_by": "local"})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(shared.Object{"object": "list", "data": items})
		return
	}
	if req.Method != "POST" || (req.URL.Path != "/v1/responses" && req.URL.Path != "/v1/responses/compact") {
		replyError(w, 404, "未提供该操作。")
		return
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		replyError(w, 429, "本地网关并发已满，请稍后重试。")
		return
	}
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		replyError(w, 415, "请求必须是 JSON。")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 16*1024*1024))
	if err != nil {
		replyError(w, 413, "请求超过 16 MB 或无法读取。")
		return
	}
	var input shared.Object
	if json.Unmarshal(data, &input) != nil || input == nil {
		replyError(w, 400, "请求 JSON 无效。")
		return
	}
	model := shared.Str(input["model"])
	connection := state.Model
	if model != "api-subagents" && model != "" {
		if !strings.HasPrefix(model, "api-subagents/") {
			replyError(w, 400, "请选择 API Subagents 模型列表中的连接。")
			return
		}
		connection = strings.TrimPrefix(model, "api-subagents/")
	}
	config, err := r.Store.Read()
	if err != nil {
		replyError(w, 500, err.Error())
		return
	}
	profile, err := configstore.ResolveProfile(config, connection)
	if err != nil {
		replyError(w, 400, err.Error())
		return
	}
	if req.URL.Path == "/v1/responses/compact" && profile.Protocol != "responses" {
		replyError(w, 400, "此连接不支持服务端压缩，请使用 Codex 的本地上下文压缩。")
		return
	}
	r.mu.Lock()
	r.state.ActiveModel = connection
	r.state.Requests++
	r.mu.Unlock()
	r.notify()
	r.forward(w, req, input, profile)
}

// format 返回 CPA 的正式协议标识，保持模型配置的四种接口与转换注册表一致。
func format(protocol string) translator.Format {
	switch protocol {
	case "compatible":
		return translator.FormatOpenAI
	case "anthropic":
		return translator.FormatClaude
	case "gemini":
		return translator.FormatGemini
	default:
		return translator.FormatOpenAIResponse
	}
}

// replyError 使用固定 OpenAI 风格错误结构，不转发可能包含 Key 的上游错误正文。
func replyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(shared.Object{"error": shared.Object{"message": message, "type": "api_subagents_error", "code": strconv.Itoa(status)}})
}

// forward 保持工具调用和多轮历史，通过 CPA 转换事件；失败不自动重复付费生成请求。
func (r *Relay) forward(w http.ResponseWriter, req *http.Request, input shared.Object, profile configstore.Profile) {
	ctx, timeout := context.WithTimeout(req.Context(), time.Duration(profile.TaskTimeout)*time.Minute)
	defer timeout()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var timerMu sync.Mutex
	var timer *time.Timer
	sequence := 0
	closed := false
	// 用代数识别旧计时器，避免刚收到数据时已排队的旧回调错误取消请求。
	arm := func(wait time.Duration) {
		timerMu.Lock()
		defer timerMu.Unlock()
		sequence++
		generation := sequence
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(wait, func() {
			timerMu.Lock()
			defer timerMu.Unlock()
			if !closed && generation == sequence {
				cancel(errors.New("上游响应超时。"))
			}
		})
	}
	defer func() {
		timerMu.Lock()
		closed = true
		if timer != nil {
			timer.Stop()
		}
		timerMu.Unlock()
	}()
	arm(time.Duration(profile.FirstTimeout) * time.Second)
	wantStream := input["stream"] == true
	upstreamStream := wantStream && profile.Stream
	to := format(profile.Protocol)
	input["model"] = profile.Model
	patchAlias := ""
	if profile.Protocol == "anthropic" {
		patchAlias = aliasPatchTool(input)
	}
	original := shared.Marshal(input)
	requestBody := original
	if to != translator.FormatOpenAIResponse {
		if input["previous_response_id"] != nil {
			replyError(w, 400, "转换协议需要完整历史，不能仅使用 previous_response_id。")
			return
		}
		if !translator.HasRequestTransformer(translator.FormatOpenAIResponse, to) {
			replyError(w, 400, "缺少该协议的请求转换器。")
			return
		}
		requestBody = translator.TranslateRequest(translator.FormatOpenAIResponse, to, profile.Model, original, upstreamStream)
	}
	var converted shared.Object
	if json.Unmarshal(requestBody, &converted) != nil {
		replyError(w, 400, "请求协议转换失败。")
		return
	}
	converted["model"] = profile.Model
	if profile.Protocol != "gemini" {
		converted["stream"] = upstreamStream
	}
	if profile.Protocol == "responses" {
		converted["store"] = false
	} else if profile.Protocol == "gemini" {
		delete(converted, "model")
		generation := shared.Obj(converted["generationConfig"])
		if generation["maxOutputTokens"] == nil {
			generation["maxOutputTokens"] = profile.MaxTokens
		}
		converted["generationConfig"] = generation
	} else if converted["max_tokens"] == nil && converted["max_completion_tokens"] == nil {
		converted["max_tokens"] = profile.MaxTokens
	}
	requestBody = shared.Marshal(converted)
	profile.Stream = upstreamStream
	endpoint, headers, _ := providers.RequestSpec(profile, nil, "", nil)
	if req.URL.Path == "/v1/responses/compact" {
		endpoint = strings.TrimSuffix(endpoint, "/responses") + "/responses/compact"
		delete(converted, "stream")
		delete(converted, "store")
		requestBody = shared.Marshal(converted)
	}
	upstream, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(requestBody))
	if err != nil {
		replyError(w, 400, "API 地址无效。")
		return
	}
	upstream.Header = headers
	res, err := r.Client.Do(upstream)
	if err != nil {
		replyError(w, 502, "模型服务连接失败或超时，请检查所选连接。")
		return
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		replyError(w, res.StatusCode, fmt.Sprintf("模型服务返回 HTTP %d，请检查 Key、模型和额度。", res.StatusCode))
		return
	}
	streaming := strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream")
	sentHeaders := false
	finished := false
	var parameter any
	var jsonBody bytes.Buffer
	received := 0
	completedItems := map[string]bool{}
	// 只发送完整的 Responses 事件；错误事件由本机固定文字构造，不暴露上游响应片段。
	emit := func(data []byte) {
		data = normalizeEvents(data, completedItems, patchAlias)
		if !sentHeaders {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(200)
			sentHeaders = true
		}
		w.Write(data)
		if !bytes.HasSuffix(data, []byte("\n\n")) {
			w.Write([]byte("\n\n"))
		}
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
	}
	terminal := false
	finishReason := false
	decoder := providers.NewEventDecoder(func(event, text string) error {
		if strings.TrimSpace(text) == "[DONE]" {
			if profile.Protocol == "compatible" && !finishReason {
				return errors.New("模型流缺少结束原因。")
			}
			terminal = true
		} else {
			var value shared.Object
			if json.Unmarshal([]byte(text), &value) != nil || value == nil {
				return errors.New("模型流事件格式无效。")
			}
			kind := shared.Str(value["type"])
			if kind == "" {
				kind = event
			}
			if value["error"] != nil || kind == "error" || kind == "response.failed" {
				return errors.New("上游模型报告请求失败。")
			}
			switch profile.Protocol {
			case "responses":
				terminal = kind == "response.completed" || kind == "response.incomplete"
			case "anthropic":
				terminal = kind == "message_stop"
			case "gemini":
				for _, item := range shared.Arr(value["candidates"]) {
					if shared.Str(shared.Obj(item)["finishReason"]) != "" {
						terminal = true
					}
				}
			default:
				for _, item := range shared.Arr(value["choices"]) {
					if shared.Str(shared.Obj(item)["finish_reason"]) != "" {
						finishReason = true
					}
				}
			}
		}
		if to == translator.FormatOpenAIResponse {
			if event == "" {
				var value shared.Object
				_ = json.Unmarshal([]byte(text), &value)
				event = shared.Str(value["type"])
			}
			if strings.TrimSpace(text) != "[DONE]" {
				emit([]byte("event: " + event + "\ndata: " + text + "\n\n"))
			}
			finished = terminal
		} else {
			chunks := translator.TranslateStream(ctx, to, translator.FormatOpenAIResponse, profile.Model, original, requestBody, []byte("data: "+text), &parameter)
			for _, chunk := range chunks {
				if isTerminal(chunk) {
					finished = true
				}
				emit(chunk)
			}
		}
		return nil
	})
	buffer := make([]byte, 16384)
	for {
		n, readErr := res.Body.Read(buffer)
		if n > 0 {
			received += n
			if received > 8*1024*1024 {
				err = errors.New("上游响应超过 8 MB。")
				break
			}
			arm(time.Duration(profile.IdleTimeout) * time.Second)
			if streaming {
				err = decoder.Feed(buffer[:n], false)
			} else {
				jsonBody.Write(buffer[:n])
			}
			if err != nil || finished && terminal {
				break
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			err = errors.New("模型连接中断或响应超时。")
			break
		}
	}
	if streaming && err == nil {
		err = decoder.Feed(nil, true)
		if !terminal || !finished {
			err = errors.New("模型流在完成前中断。")
		}
	}
	if ctx.Err() != nil {
		err = errors.New("模型请求已取消或超时。")
	}
	if err != nil {
		if sentHeaders {
			emit([]byte("event: response.failed\ndata: " + string(shared.Marshal(shared.Object{"type": "response.failed", "response": shared.Object{"id": "resp_" + shared.UUID(), "status": "failed", "error": shared.Object{"code": "upstream_error", "message": err.Error()}}})) + "\n\n"))
		} else {
			replyError(w, 502, err.Error())
		}
		return
	}
	if !streaming {
		body := jsonBody.Bytes()
		if to != translator.FormatOpenAIResponse {
			if profile.Protocol == "anthropic" {
				body, err = claudeTranscript(body)
				if err != nil {
					replyError(w, 502, err.Error())
					return
				}
			}
			body = translator.TranslateNonStream(ctx, to, translator.FormatOpenAIResponse, profile.Model, original, requestBody, body, &parameter)
		}
		var response shared.Object
		if json.Unmarshal(body, &response) != nil || response == nil {
			replyError(w, 502, "上游没有返回完整 JSON 响应。")
			return
		}
		if req.URL.Path != "/v1/responses/compact" && (shared.Str(response["id"]) == "" || (response["status"] != "completed" && response["status"] != "incomplete") || response["output"] == nil) {
			replyError(w, 502, "上游没有返回完整的 Responses 结果。")
			return
		}
		normalizeUsage(response)
		restorePatchTool(response, patchAlias)
		if !wantStream {
			w.Header().Set("Content-Type", "application/json")
			w.Write(shared.Marshal(response))
			return
		}
		emitResponse(response, emit)
	}
}

// emitResponse 为忽略 stream 的普通 JSON 网关补齐 Responses 事件，使 Codex 仍能消费完整结果。
func emitResponse(response shared.Object, emit func([]byte)) {
	sequence := 0
	send := func(kind string, value shared.Object) {
		value["type"] = kind
		value["sequence_number"] = sequence
		sequence++
		emit([]byte("event: " + kind + "\ndata: " + string(shared.Marshal(value)) + "\n\n"))
	}
	initial := shared.Object{}
	for key, value := range response {
		initial[key] = value
	}
	initial["status"] = "in_progress"
	initial["output"] = []any{}
	send("response.created", shared.Object{"response": initial})
	send("response.in_progress", shared.Object{"response": initial})
	for index, value := range shared.Arr(response["output"]) {
		item := shared.Obj(value)
		initialItem := shared.Object{}
		for k, v := range item {
			initialItem[k] = v
		}
		initialItem["status"] = "in_progress"
		switch item["type"] {
		case "message":
			initialItem["content"] = []any{}
		case "function_call":
			initialItem["arguments"] = ""
		case "custom_tool_call":
			initialItem["input"] = ""
		}
		send("response.output_item.added", shared.Object{"output_index": index, "item": initialItem})
		switch item["type"] {
		case "message":
			for contentIndex, raw := range shared.Arr(item["content"]) {
				part := shared.Obj(raw)
				if part["type"] == "output_text" {
					send("response.content_part.added", shared.Object{"output_index": index, "item_id": item["id"], "content_index": contentIndex, "part": shared.Object{"type": "output_text", "text": "", "annotations": []any{}}})
					send("response.output_text.delta", shared.Object{"output_index": index, "item_id": item["id"], "content_index": contentIndex, "delta": part["text"]})
					send("response.output_text.done", shared.Object{"output_index": index, "item_id": item["id"], "content_index": contentIndex, "text": part["text"]})
				}
				send("response.content_part.done", shared.Object{"output_index": index, "item_id": item["id"], "content_index": contentIndex, "part": part})
			}
		case "function_call", "custom_tool_call":
			prefix, key := "response.function_call_arguments", "arguments"
			if item["type"] == "custom_tool_call" {
				prefix, key = "response.custom_tool_call_input", "input"
			}
			send(prefix+".delta", shared.Object{"output_index": index, "item_id": item["id"], "delta": item[key]})
			send(prefix+".done", shared.Object{"output_index": index, "item_id": item["id"], key: item[key]})
		}
		send("response.output_item.done", shared.Object{"output_index": index, "item": item})
	}
	kind := "response.completed"
	if response["status"] == "incomplete" {
		kind = "response.incomplete"
	}
	send(kind, shared.Object{"response": response})
}
