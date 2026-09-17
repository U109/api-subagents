package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
)

// completedRelayResponse 读取合成 Responses 流的完整结果，失败或缺少终态时立即停止回归测试。
func completedRelayResponse(t *testing.T, status int, body string) shared.Object {
	t.Helper()
	var result shared.Object
	decoder := providers.NewEventDecoder(func(_ string, data string) error {
		var event shared.Object
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return err
		}
		if event["type"] == "response.completed" {
			result = shared.Obj(event["response"])
		}
		if event["type"] == "response.failed" {
			t.Error("relay reported failure", data)
		}
		return nil
	})
	if err := decoder.Feed([]byte(body), true); err != nil || status != 200 || result == nil {
		t.Fatal("invalid relay result", status, err, body)
	}
	return result
}

// TestCPALongToolNamesRoundTrip 验证严格兼容接口的 64 字符限制、截短重名、指定工具及历史回放始终指向同一工具。
func TestCPALongToolNamesRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			namespaces := []string{"first_" + strings.Repeat("shared_", 12), "second_" + strings.Repeat("shared_", 12)}
			local := "read_project_file"
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls++
				var body shared.Object
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				definitions := shared.Arr(body["tools"])
				if len(definitions) != 2 {
					t.Error("tool lost during conversion")
					w.WriteHeader(400)
					return
				}
				names := []string{}
				for _, item := range definitions {
					name := shared.Str(shared.Obj(shared.Obj(item)["function"])["name"])
					if len(name) == 0 || len(name) > 64 {
						t.Error("upstream tool name exceeds 64 characters")
					}
					names = append(names, name)
				}
				if names[0] == names[1] {
					t.Error("distinct tools collapsed to one name")
				}
				if shared.Obj(shared.Obj(body["tool_choice"])["function"])["name"] != names[1] {
					t.Error("forced tool routed incorrectly")
				}
				for _, value := range shared.Arr(body["messages"]) {
					for _, raw := range shared.Arr(shared.Obj(value)["tool_calls"]) {
						if shared.Obj(shared.Obj(raw)["function"])["name"] != names[1] {
							t.Error("history replay routed incorrectly")
						}
					}
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\n", shared.Marshal(shared.Object{"id": "chat1", "choices": []any{shared.Object{"index": 0, "delta": shared.Object{"tool_calls": []any{shared.Object{"index": 0, "id": "call_next", "type": "function", "function": shared.Object{"name": names[1], "arguments": `{"path":"file.go"}`}}}}, "finish_reason": "tool_calls"}}}))
					io.WriteString(w, "data: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(shared.Object{"id": "chat1", "choices": []any{shared.Object{"message": shared.Object{"role": "assistant", "tool_calls": []any{shared.Object{"id": "call_next", "type": "function", "function": shared.Object{"name": names[1], "arguments": `{"path":"file.go"}`}}}}, "finish_reason": "tool_calls"}}})
				}
			}))
			defer server.Close()
			r := testRelay(t, "compatible", server.URL, stream)
			tools := []any{}
			for _, namespace := range namespaces {
				tools = append(tools, shared.Object{"type": "namespace", "name": namespace, "tools": []any{shared.Object{"type": "function", "name": local, "parameters": shared.Object{"type": "object", "properties": shared.Object{"path": shared.Object{"type": "string"}}}}}})
			}
			input := shared.Object{"model": "api-subagents", "stream": true, "tools": tools, "tool_choice": shared.Object{"type": "function", "name": local, "namespace": namespaces[1]}, "input": []any{
				shared.Object{"type": "message", "role": "user", "content": "read the synthetic file"},
				shared.Object{"type": "function_call", "call_id": "call_before", "name": local, "namespace": namespaces[1], "arguments": `{}`},
				shared.Object{"type": "function_call_output", "call_id": "call_before", "output": "synthetic result"},
			}}
			status, text := requestRelay(t, r, input)
			response := completedRelayResponse(t, status, text)
			output := shared.Arr(response["output"])
			if len(output) != 1 {
				t.Fatal("unexpected output", response)
			}
			call := shared.Obj(output[0])
			if call["type"] != "function_call" || call["name"] != local || call["namespace"] != namespaces[1] || call["call_id"] != "call_next" {
				t.Fatal("return call lost original identity", call)
			}
			if calls != 1 {
				t.Fatal("unexpected upstream retry")
			}
		})
	}
}

// TestCPAGeminiSchemaCompatibility 验证 Gemini 不支持的 schema 元数据被清理，业务属性同名为 id 时仍保留。
func TestCPAGeminiSchemaCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body shared.Object
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		tools := shared.Arr(body["tools"])
		if len(tools) != 1 {
			t.Error("missing tool declarations")
			w.WriteHeader(400)
			return
		}
		definitions := shared.Arr(shared.Obj(tools[0])["functionDeclarations"])
		if len(definitions) != 1 {
			t.Error("missing function")
			w.WriteHeader(400)
			return
		}
		definition := shared.Obj(definitions[0])
		schema := shared.Obj(definition["parameters"])
		if len(schema) == 0 {
			schema = shared.Obj(definition["parametersJsonSchema"])
		}
		for _, key := range []string{"$schema", "$id", "id", "$anchor", "$dynamicRef", "$dynamicAnchor", "$vocabulary"} {
			if schema[key] != nil {
				t.Error("unsupported Gemini schema keyword", key)
			}
		}
		if shared.Obj(schema["properties"])["id"] == nil {
			t.Error("actual id argument was removed")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, upstreamReply("gemini", false))
	}))
	defer server.Close()
	r := testRelay(t, "gemini", server.URL, false)
	schema := shared.Object{"type": "object", "$schema": "https://json-schema.org/draft/2020-12/schema", "$id": "urn:synthetic", "id": "urn:legacy", "$anchor": "root", "$dynamicRef": "#node", "$dynamicAnchor": "node", "$vocabulary": shared.Object{}, "properties": shared.Object{"id": shared.Object{"type": "string"}}, "required": []string{"id"}}
	status, text := requestRelay(t, r, shared.Object{"model": "api-subagents", "stream": true, "input": "synthetic request", "tools": []any{shared.Object{"type": "function", "name": "lookup", "parameters": schema}}})
	completedRelayResponse(t, status, text)
}
