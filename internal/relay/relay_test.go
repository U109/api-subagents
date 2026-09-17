package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// testRelay 创建隔离的 Codex 配置与合成连接，启停测试不会改动当前用户设置。
func testRelay(t *testing.T, protocol, endpoint string, stream bool) *Relay {
	t.Helper()
	store := configstore.NewConfigStore(filepath.Join(t.TempDir(), "models.json"))
	c, err := configstore.ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": protocol, "model": "mock-model", "baseUrl": endpoint, "apiKey": "synthetic-relay-key", "stream": stream}}}), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(c); err != nil {
		t.Fatal(err)
	}
	r := New(store, codexconfig.CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()})
	if err = r.Enable("demo"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Disable(); err != nil {
			t.Error(err)
		}
	})
	return r
}

// requestRelay 通过真实本地 HTTP 访问网关，验证认证和转换后的 SSE 数据。
func requestRelay(t *testing.T, r *Relay, body shared.Object) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", r.Snapshot().Address+"/responses", strings.NewReader(string(shared.Marshal(body))))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Subagents-Token", r.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(data)
}

// upstreamReply 提供各厂商的合成普通或流式结果，所有请求止于本机模拟服务。
func upstreamReply(protocol string, stream bool) string {
	if stream {
		switch protocol {
		case "compatible":
			return "data: {\"id\":\"chat1\",\"model\":\"mock-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		case "anthropic":
			events := []shared.Object{{"type": "message_start", "message": shared.Object{"id": "msg1", "type": "message", "role": "assistant", "model": "mock-model", "content": []any{}, "usage": shared.Object{"input_tokens": 5, "output_tokens": 0}}}, {"type": "content_block_start", "index": 0, "content_block": shared.Object{"type": "text", "text": ""}}, {"type": "content_block_delta", "index": 0, "delta": shared.Object{"type": "text_delta", "text": "OK"}}, {"type": "content_block_stop", "index": 0}, {"type": "message_delta", "delta": shared.Object{"stop_reason": "end_turn"}, "usage": shared.Object{"output_tokens": 1}}, {"type": "message_stop"}}
			var out strings.Builder
			for _, event := range events {
				fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", event["type"], shared.Marshal(event))
			}
			return out.String()
		case "gemini":
			return "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"OK\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":1}}\n\n"
		default:
			return "event: response.completed\ndata: " + string(shared.Marshal(shared.Object{"type": "response.completed", "response": jsonResponse()})) + "\n\n"
		}
	}
	switch protocol {
	case "compatible":
		return `{"id":"chat1","model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`
	case "anthropic":
		return `{"id":"msg1","type":"message","role":"assistant","model":"mock-model","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`
	case "gemini":
		return `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}`
	default:
		return string(shared.Marshal(jsonResponse()))
	}
}

// jsonResponse 构造完整的 Responses 消息，供原生兼容连接和普通 JSON 网关复用。
func jsonResponse() shared.Object {
	return shared.Object{"id": "resp_1", "object": "response", "created_at": 1, "status": "completed", "model": "mock-model", "output": []any{shared.Object{"type": "message", "id": "msg1", "role": "assistant", "status": "completed", "content": []any{shared.Object{"type": "output_text", "text": "OK", "annotations": []any{}}}}}, "usage": shared.Object{"input_tokens": 5, "output_tokens": 1, "total_tokens": 6}}
}

// TestRelayProtocols 覆盖四种连接的 JSON 与 SSE 上游，并验证 Codex 身份不被转发给外部服务。
func TestRelayProtocols(t *testing.T) {
	for _, protocol := range []string{"compatible", "responses", "anthropic", "gemini"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", protocol, stream), func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					requests++
					if req.Header.Get("X-Api-Subagents-Token") != "" {
						t.Error("local token forwarded")
					}
					body, _ := io.ReadAll(req.Body)
					if strings.Contains(string(body), "api-subagents/") {
						t.Error("model alias not replaced")
					}
					if protocol == "compatible" && !strings.Contains(string(body), "messages") {
						t.Error("request not translated")
					}
					w.Header().Set("Content-Type", "application/json")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					io.WriteString(w, upstreamReply(protocol, stream))
				}))
				defer server.Close()
				r := testRelay(t, protocol, server.URL, stream)
				status, body := requestRelay(t, r, shared.Object{"model": "api-subagents/demo", "input": "Reply OK", "stream": true})
				if status != 200 || !strings.Contains(body, "response.completed") || !strings.Contains(body, "OK") || strings.Contains(body, "response.failed") {
					t.Fatalf("status %d: %s", status, body)
				}
				if requests != 1 || r.Snapshot().ActiveModel != "demo" {
					t.Fatal("routing or retries")
				}
			})
		}
	}
}

// TestRelayGuards 没有令牌、跨站请求、未知模型及上游错误均不能泄露凭据或隐式重试。
func TestRelayGuards(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count++
		w.WriteHeader(401)
		io.WriteString(w, "synthetic-relay-key private-upstream-error")
	}))
	defer server.Close()
	r := testRelay(t, "compatible", server.URL, true)
	for _, tc := range []struct{ name, token, origin string }{{"missing", "", ""}, {"wrong", "wrong", ""}, {"cross-origin", r.token, "https://evil.invalid"}} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", r.Snapshot().Address+"/responses", strings.NewReader(`{}`))
			req.Header.Set("X-Api-Subagents-Token", tc.token)
			req.Header.Set("Origin", tc.origin)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 403 {
				t.Fatal(res.StatusCode)
			}
		})
	}
	if count != 0 {
		t.Fatal("unauthenticated request reached provider")
	}
	status, body := requestRelay(t, r, shared.Object{"model": "unknown", "input": "x"})
	if status != 400 || count != 0 {
		t.Fatal(status, body)
	}
	status, body = requestRelay(t, r, shared.Object{"model": "api-subagents", "input": "x", "stream": true})
	if status != 401 || strings.Contains(body, "synthetic") || strings.Contains(body, "private-upstream") || count != 1 {
		t.Fatal(status, body, count)
	}
}

// TestRelayToolTranslation 验证 Codex 工具定义、函数结果和返回调用 ID 经 Chat Completions 转换后保持可用。
func TestRelayToolTranslation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body shared.Object
		json.NewDecoder(req.Body).Decode(&body)
		tools := shared.Arr(body["tools"])
		if len(tools) != 1 {
			t.Error("tool definition missing")
		}
		function := shared.Obj(shared.Obj(tools[0])["function"])
		name := shared.Str(function["name"])
		messages := shared.Arr(body["messages"])
		found := false
		for _, value := range messages {
			message := shared.Obj(value)
			if message["role"] == "tool" && message["tool_call_id"] == "prior" {
				found = true
			}
		}
		if !found {
			t.Error("tool output history lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := shared.Object{"id": "chat", "choices": []any{shared.Object{"index": 0, "delta": shared.Object{"tool_calls": []any{shared.Object{"index": 0, "id": "next", "type": "function", "function": shared.Object{"name": name, "arguments": "{\"query\":\"hello\"}"}}}}, "finish_reason": "tool_calls"}}}
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", shared.Marshal(chunk))
	}))
	defer server.Close()
	r := testRelay(t, "compatible", server.URL, true)
	status, body := requestRelay(t, r, shared.Object{"model": "api-subagents", "stream": true, "tools": []any{shared.Object{"type": "function", "name": "lookup", "parameters": shared.Object{"type": "object", "properties": shared.Object{"query": shared.Object{"type": "string"}}}}}, "input": []any{shared.Object{"type": "function_call", "name": "lookup", "call_id": "prior", "arguments": "{}"}, shared.Object{"type": "function_call_output", "call_id": "prior", "output": "old result"}}})
	if status != 200 || !strings.Contains(body, "response.completed") || !strings.Contains(body, "lookup") || !strings.Contains(body, "next") {
		t.Fatal(status, body)
	}
}

// TestRelayStopsAndRecovers 开启不发送生成请求，关闭还原原配置，并保留模式运行期间的其他设置修改。
func TestRelayStopsAndRecovers(t *testing.T) {
	r := testRelay(t, "compatible", "http://127.0.0.1:9", true)
	path := filepath.Join(r.Codex.Home, "config.toml")
	current, _ := os.ReadFile(path)
	current = append(current, []byte("\n[features]\nmanual_change=true\n")...)
	os.WriteFile(path, current, 0600)
	if err := r.Disable(); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(path)
	if strings.Contains(string(restored), "X-Api-Subagents-Token") || !strings.Contains(string(restored), "manual_change=true") || !strings.Contains(string(restored), "127.0.0.1:0/v1") {
		t.Fatal("settings not restored")
	}
	if r.Snapshot().Enabled {
		t.Fatal("relay still enabled")
	}
	_ = context.Background()
}
