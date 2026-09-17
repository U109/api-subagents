package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
)

// vendorMessage 构造带思考、工具调用和原始签名的厂商消息，第二轮返回普通正文。
func vendorMessage(mode string, turn int) shared.Object {
	if turn > 1 {
		return shared.Object{"role": "assistant", "content": "OK"}
	}
	message := shared.Object{"role": "assistant", "content": "", "tool_calls": []any{shared.Object{"id": "call_local", "type": "function", "function": shared.Object{"name": "read_file", "arguments": `{"path":"demo.go"}`}, "extra_content": shared.Object{"google": shared.Object{"thought_signature": "synthetic-signature"}}}}}
	if mode == "minimax" {
		message["reasoning_details"] = []any{shared.Object{"index": 0, "type": "reasoning.text", "text": "先检查文件", "signature": "synthetic-detail"}}
	} else {
		message["reasoning_content"] = "先检查文件"
	}
	return message
}

// writeVendorReply 输出真实本地 HTTP JSON 或 SSE；流将参数拆分成两帧，验证中间片段不会提早执行。
func writeVendorReply(w http.ResponseWriter, mode string, turn int, stream bool) {
	message := vendorMessage(mode, turn)
	finish := "tool_calls"
	if turn > 1 {
		finish = "stop"
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(shared.Object{"id": "chat-local", "choices": []any{shared.Object{"index": 0, "message": message, "finish_reason": finish}}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if turn == 1 {
		call := shared.Obj(shared.Arr(message["tool_calls"])[0])
		call["index"] = 0
		shared.Obj(call["function"])["arguments"] = `{"path":`
	}
	fmt.Fprintf(w, "data: %s\n\n", shared.Marshal(shared.Object{"id": "chat-local", "choices": []any{shared.Object{"index": 0, "delta": message, "finish_reason": nil}}}))
	if turn == 1 {
		fmt.Fprint(w, "data: {\"id\":\"chat-local\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"demo.go\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
	}
	fmt.Fprintf(w, "data: %s\n\n", shared.Marshal(shared.Object{"id": "chat-local", "choices": []any{shared.Object{"index": 0, "delta": shared.Object{}, "finish_reason": finish}}}))
	io.WriteString(w, "data: [DONE]\n\n")
}

// assertVendorRequest 检查工具回合原字段、实际 model 和凭据，保证策略不串到其他连接，也不重复生成。
func assertVendorRequest(t *testing.T, req *http.Request, mode, model string, turn int) {
	t.Helper()
	var body shared.Object
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Error(err)
		return
	}
	if body["model"] != model || req.Header.Get("Authorization") != "Bearer synthetic-relay-key" {
		t.Error("model or credential changed")
	}
	if mode == "minimax" && body["reasoning_split"] != true {
		t.Error("missing split switch")
	}
	if mode == "generic" && (body["thinking"] != nil || body["reasoning_split"] != nil) {
		t.Error("gateway got native parameters")
	}
	if turn > 2 {
		t.Error("unexpected retry")
	}
	if turn != 2 {
		return
	}
	found := false
	for _, raw := range shared.Arr(body["messages"]) {
		message := shared.Obj(raw)
		calls := shared.Arr(message["tool_calls"])
		if message["role"] != "assistant" || len(calls) == 0 {
			continue
		}
		found = true
		if message["reasoning_content"] != "先检查文件" {
			t.Errorf("%s reasoning lost: %v", mode, message["reasoning_content"])
		}
		if mode == "minimax" {
			details := shared.Arr(message["reasoning_details"])
			if len(details) != 1 || shared.Obj(details[0])["signature"] != "synthetic-detail" {
				t.Error("MiniMax details lost")
			}
		}
		call := shared.Obj(calls[0])
		if shared.Obj(shared.Obj(call["extra_content"])["google"])["thought_signature"] != "synthetic-signature" {
			t.Error("tool signature lost")
		}
		if shared.Obj(call["function"])["arguments"] != `{"path":"demo.go"}` {
			t.Error("arguments corrupted")
		}
	}
	if !found {
		t.Error("tool history missing")
	}
}

// vendorRelayResponse 同时检查 JSON 与 SSE 下游输出，确保每种厂商模式都覆盖两种响应形式。
func vendorRelayResponse(t *testing.T, status int, body string, stream bool) shared.Object {
	t.Helper()
	if stream {
		return completedRelayResponse(t, status, body)
	}
	var response shared.Object
	if err := json.Unmarshal([]byte(body), &response); err != nil || status != 200 || response["status"] != "completed" {
		t.Fatal("invalid JSON response", status, body)
	}
	return response
}

// TestVendorCompatibilityRoundTrip 验证所有策略在插件与挟持两条路径的 JSON、分块 SSE 和第二轮工具回放。
func TestVendorCompatibilityRoundTrip(t *testing.T) {
	models := map[string]string{"gemini": "gemini-3.8-flash", "deepseek": "deepseek-v4-pro", "kimi": "kimi-k2.5", "kimi-coding": "k3", "doubao": "doubao-seed-1-6", "minimax": "MiniMax-M3", "glm": "glm-4.7", "generic": "vendor-custom-alias"}
	for mode, model := range models {
		for _, stream := range []bool{false, true} {
			for _, worker := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%v/worker=%v", mode, stream, worker), func(t *testing.T) {
					var count atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						turn := int(count.Add(1))
						assertVendorRequest(t, req, mode, model, turn)
						writeVendorReply(w, mode, turn, stream)
					}))
					defer server.Close()
					if worker {
						p := configstore.Profile{Protocol: "compatible", Model: model, BaseURL: server.URL, APIKey: "synthetic-relay-key", Stream: stream, MaxTokens: 4096, FirstTimeout: 3, IdleTimeout: 3, ReasoningEffort: "high", ModelCompatibility: map[string]string{model: mode}}
						client := providers.NewProvider()
						history := providers.InitialHistory(p, "Read demo.go")
						tool := []shared.Tool{{Name: "read_file", Schema: shared.Object{"type": "object", "properties": shared.Object{"path": shared.Object{"type": "string"}}}}}
						reply, err := client.Turn(context.Background(), p, &history, "", tool, nil)
						if err != nil || len(reply.Calls) != 1 {
							t.Fatal("worker tool turn", err, reply)
						}
						reply.Calls[0].Output = "synthetic file"
						providers.AddResults(p, &history, reply.Calls)
						reply, err = client.Turn(context.Background(), p, &history, "", tool, nil)
						if err != nil || reply.Text != "OK" {
							t.Fatal("worker second turn", err, reply)
						}
					} else {
						r := testRelay(t, "compatible", server.URL, stream)
						c, _ := r.Store.Read()
						p := c.Models["demo"]
						p.Model, p.ReasoningEffort = model, "high"
						p.ModelCompatibility = map[string]string{model: mode}
						c.Models["demo"] = p
						if err := r.Store.Save(c); err != nil {
							t.Fatal(err)
						}
						input := []any{shared.Object{"role": "user", "content": "Read demo.go"}}
						body := shared.Object{"model": "api-subagents", "stream": stream, "input": input, "tools": []any{shared.Object{"type": "function", "name": "read_file", "parameters": shared.Object{"type": "object", "properties": shared.Object{"path": shared.Object{"type": "string"}}}}}}
						status, text := requestRelay(t, r, body)
						response := vendorRelayResponse(t, status, text, stream)
						input = append(input, shared.Arr(response["output"])...)
						input = append(input, shared.Object{"type": "function_call_output", "call_id": "call_local", "output": "synthetic file"})
						body["input"] = input
						status, text = requestRelay(t, r, body)
						vendorRelayResponse(t, status, text, stream)
					}
					if count.Load() != 2 {
						t.Fatal("wrong request count", count.Load())
					}
				})
			}
		}
	}
}

// TestChatHistoryIsolationAndExpiry 验证缓存按线路、会话和实际历史隔离，过期、关闭与尺寸限制都生效。
func TestChatHistoryIsolationAndExpiry(t *testing.T) {
	h := &chatHistory{}
	p := configstore.Profile{Model: "one", APIKey: "synthetic"}
	scope := chatScope(p, "session-one")
	request := shared.Object{"messages": []any{shared.Object{"role": "user", "content": "first"}}}
	message := vendorMessage("minimax", 1)
	h.remember(scope, request, message)
	for _, variation := range []string{"same", "session", "key", "model", "user", "args", "expired", "clear"} {
		var body shared.Object
		_ = json.Unmarshal(shared.Marshal(request), &body)
		copy := vendorMessage("generic", 1)
		delete(copy, "reasoning_content")
		delete(shared.Obj(shared.Arr(copy["tool_calls"])[0]), "extra_content")
		body["messages"] = append(shared.Arr(body["messages"]), copy)
		candidate := p
		session := "session-one"
		switch variation {
		case "session":
			session = "session-two"
		case "key":
			candidate.APIKey = "another"
		case "model":
			candidate.Model = "other"
		case "user":
			shared.Obj(shared.Arr(body["messages"])[0])["content"] = "other"
		case "args":
			shared.Obj(shared.Obj(shared.Arr(copy["tool_calls"])[0])["function"])["arguments"] = `{}`
		case "expired":
			for key, entry := range h.items {
				entry.expires = time.Now().Add(-time.Second)
				h.items[key] = entry
			}
		case "clear":
			h.remember(scope, request, message)
			h.clear()
		}
		h.restore(chatScope(candidate, session), body)
		if (copy["reasoning_details"] != nil) != (variation == "same") {
			t.Fatal("unsafe metadata replay", variation)
		}
	}
	for i := 0; i < 300; i++ {
		p.Model = fmt.Sprint(i)
		h.remember(chatScope(p, ""), request, message)
	}
	if len(h.items) > 256 || h.bytes > 16*1024*1024 {
		t.Fatal("cache unbounded")
	}
}
