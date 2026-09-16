package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
)

// TestCodexToolsRoundTrip 检查 Codex 命名空间函数与自由文本补丁经三种转换协议后仍保持类型及参数。
func TestCodexToolsRoundTrip(t *testing.T) {
	for _, protocol := range []string{"compatible", "anthropic", "gemini"} {
		for _, kind := range []string{"namespace", "custom"} {
			t.Run(protocol+"/"+kind, func(t *testing.T) {
				arguments := `{"cmd":"echo mock"}`
				if kind == "custom" {
					arguments = `{"input":"*** Begin Patch\n*** End Patch"}`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					var body shared.Object
					json.NewDecoder(req.Body).Decode(&body)
					definitions := shared.Arr(body["tools"])
					if len(definitions) == 0 {
						t.Error("tool definitions dropped")
						w.WriteHeader(400)
						return
					}
					if protocol == "gemini" {
						definitions = shared.Arr(shared.Obj(definitions[0])["functionDeclarations"])
					}
					definition := shared.Obj(definitions[0])
					if protocol == "compatible" {
						definition = shared.Obj(definition["function"])
					}
					name := shared.Str(definition["name"])
					var args any
					json.Unmarshal([]byte(arguments), &args)
					var result shared.Object
					switch protocol {
					case "compatible":
						result = shared.Object{"id": "chat1", "choices": []any{shared.Object{"message": shared.Object{"role": "assistant", "tool_calls": []any{shared.Object{"id": "call1", "type": "function", "function": shared.Object{"name": name, "arguments": arguments}}}}, "finish_reason": "tool_calls"}}}
					case "anthropic":
						result = shared.Object{"id": "msg1", "type": "message", "role": "assistant", "content": []any{shared.Object{"type": "tool_use", "id": "call1", "name": name, "input": args}}, "stop_reason": "tool_use", "usage": shared.Object{"input_tokens": 10, "output_tokens": 5}}
					case "gemini":
						result = shared.Object{"candidates": []any{shared.Object{"content": shared.Object{"role": "model", "parts": []any{shared.Object{"functionCall": shared.Object{"name": name, "args": args}}}}, "finishReason": "STOP"}}}
					}
					json.NewEncoder(w).Encode(result)
				}))
				defer server.Close()
				r := testRelay(t, protocol, server.URL, false)
				tool := shared.Object{"type": "namespace", "name": "functions", "tools": []any{shared.Object{"type": "function", "name": "exec_command", "parameters": shared.Object{"type": "object", "properties": shared.Object{"cmd": shared.Object{"type": "string"}}}}}}
				if kind == "custom" {
					tool = shared.Object{"type": "custom", "name": "apply_patch", "format": shared.Object{"type": "text"}}
				}
				status, body := requestRelay(t, r, shared.Object{"model": "api-subagents", "stream": true, "input": "do the mock task", "tools": []any{tool}})
				var response shared.Object
				decoder := providers.NewEventDecoder(func(_ string, data string) error {
					var v shared.Object
					json.Unmarshal([]byte(data), &v)
					if v["type"] == "response.completed" {
						response = shared.Obj(v["response"])
					}
					return nil
				})
				decoder.Feed([]byte(body), true)
				output := shared.Arr(response["output"])
				if status != 200 || len(output) != 1 {
					t.Fatal(status, body)
				}
				item := shared.Obj(output[0])
				if kind == "namespace" {
					if item["type"] != "function_call" || item["name"] != "exec_command" || item["namespace"] != "functions" || item["arguments"] != arguments {
						t.Fatal(item)
					}
				} else if item["type"] != "custom_tool_call" || item["name"] != "apply_patch" || item["input"] != "*** Begin Patch\n*** End Patch" {
					t.Fatal(item)
				}
			})
		}
	}
}

// TestRelayCancellation 验证 Codex 取消请求会关闭正在进行的上游请求，避免后台继续消耗模型额度。
func TestRelayCancellation(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.Copy(io.Discard, req.Body)
		close(started)
		<-req.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	r := testRelay(t, "compatible", server.URL, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", r.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","input":"x","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Subagents-Token", r.token)
	done := make(chan struct{})
	go func() {
		res, _ := http.DefaultClient.Do(req)
		if res != nil {
			res.Body.Close()
		}
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not start")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not reach upstream")
	}
	<-done
}

// TestTerminalDetection 回答提及 response.completed 不应被当成结束事件。
func TestTerminalDetection(t *testing.T) {
	if isTerminal([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"response.completed\"}\n\n")) {
		t.Fatal("text treated as protocol")
	}
}
