package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
)

// progressFrames 构造先正文后工具的合成流；两段之间由测试门闩隔开，不能等待终态才转发正文。
func progressFrames(protocol string) (string, string) {
	switch protocol {
	case "compatible":
		return `data: {"id":"chat1","choices":[{"index":0,"delta":{"role":"assistant","content":"Checking mock setup"}}]}` + "\n\n",
			`data: {"id":"chat1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_local","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}` + "\n\n" +
				`data: {"id":"chat1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"demo.go\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	case "gemini":
		return `data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Checking mock setup"}]}}]}` + "\n\n",
			`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"demo.go"}}}]},"finishReason":"STOP"}]}` + "\n\n"
	case "anthropic":
		return `event: message_start
data: {"type":"message_start","message":{"id":"msg1","type":"message","role":"assistant","model":"mock-model","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Checking mock setup"}}

`, `event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_local","name":"read_file","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"demo.go\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}

event: message_stop
data: {"type":"message_stop"}

`
	default:
		message := shared.Object{"id": "msg1", "type": "message", "role": "assistant", "phase": "commentary", "status": "completed", "content": []any{shared.Object{"type": "output_text", "text": "Checking mock setup", "annotations": []any{}}}}
		call := shared.Object{"id": "fc1", "type": "function_call", "call_id": "call_local", "name": "read_file", "arguments": `{"path":"demo.go"}`, "status": "completed"}
		first := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg1\",\"output_index\":0,\"content_index\":0,\"delta\":\"Checking mock setup\"}\n\n"
		rest := fmt.Sprintf("event: response.output_item.done\ndata: %s\n\nevent: response.output_item.done\ndata: %s\n\nevent: response.completed\ndata: %s\n\n", shared.Marshal(shared.Object{"type": "response.output_item.done", "output_index": 0, "item": message}), shared.Marshal(shared.Object{"type": "response.output_item.done", "output_index": 1, "item": call}), shared.Marshal(shared.Object{"type": "response.completed", "response": shared.Object{"id": "resp1", "status": "completed", "output": []any{message, call}}}))
		return first, rest
	}
}

// TestProgressBeforeToolCompletion 用真实本地 HTTP 门闩验证四协议及时转发正文、完整工具参数和带工具结果的续轮；不调用真实模型。
func TestProgressBeforeToolCompletion(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			first, rest := progressFrames(protocol)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if requests.Add(1) > 1 {
					// 第二轮必须同时携带前一轮正文、工具调用及工具结果，不能把进度当作对话结束。
					for _, expected := range []string{"Checking mock setup", "read_file", "synthetic-tool-result"} {
						if !strings.Contains(string(body), expected) {
							t.Errorf("continuation lost %q: %s", expected, body)
						}
					}
					io.WriteString(w, upstreamReply(protocol, true))
					return
				}
				io.WriteString(w, first)
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-req.Context().Done():
					return
				}
				io.WriteString(w, rest)
			}))
			defer server.Close()
			defer unblock()
			r := testRelay(t, protocol, server.URL, true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			input := shared.Object{"model": "api-subagents", "stream": true, "input": "Inspect mock setup", "tools": []any{shared.Object{"type": "function", "name": "read_file", "parameters": shared.Object{"type": "object", "properties": shared.Object{"path": shared.Object{"type": "string"}}}}}}
			req, err := http.NewRequestWithContext(ctx, "POST", r.Snapshot().Address+"/responses", strings.NewReader(string(shared.Marshal(input))))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Api-Subagents-Token", r.token)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal("progress was buffered until completion:", err)
			}
			defer res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatal(res.StatusCode)
			}
			progress := ""
			completed := 0
			toolDone := 0
			var final shared.Object
			decoder := providers.NewEventDecoder(func(_ string, data string) error {
				var event shared.Object
				if err := json.Unmarshal([]byte(data), &event); err != nil {
					return err
				}
				switch event["type"] {
				case "response.failed":
					return fmt.Errorf("stream failed: %s", data)
				case "response.output_text.delta":
					progress += shared.Str(event["delta"])
					if progress == "Checking mock setup" {
						unblock()
					}
				case "response.output_item.done":
					item := shared.Obj(event["item"])
					if item["type"] == "function_call" {
						toolDone++
						if progress != "Checking mock setup" {
							t.Error("tool arrived before progress")
						}
						if item["arguments"] != `{"path":"demo.go"}` {
							t.Error("partial or corrupt tool arguments", item)
						}
					}
				case "response.completed":
					if progress != "Checking mock setup" || toolDone != 1 {
						t.Error("response completed before progress and tool call")
					}
					completed++
					final = shared.Obj(event["response"])
				}
				return nil
			})
			buffer := make([]byte, 4096)
			for {
				n, err := res.Body.Read(buffer)
				if n > 0 {
					if e := decoder.Feed(buffer[:n], false); e != nil {
						t.Fatal(e)
					}
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal("progress must arrive before releasing upstream:", err)
				}
			}
			if err := decoder.Feed(nil, true); err != nil {
				t.Fatal(err)
			}
			if completed != 1 || toolDone != 1 || progress != "Checking mock setup" {
				t.Fatalf("progress=%q completed=%d tools=%d", progress, completed, toolDone)
			}
			messages, calls := 0, 0
			for _, raw := range shared.Arr(final["output"]) {
				item := shared.Obj(raw)
				switch item["type"] {
				case "message":
					messages++
					if !strings.Contains(string(shared.Marshal(item)), "Checking mock setup") {
						t.Error("progress lost from final output")
					}
					if protocol == "responses" && item["phase"] != "commentary" {
						t.Error("commentary phase lost")
					}
				case "function_call":
					calls++
					if item["name"] != "read_file" || item["arguments"] != `{"path":"demo.go"}` || shared.Str(item["call_id"]) == "" {
						t.Error("tool call corrupted", item)
					}
				}
			}
			if messages != 1 || calls != 1 {
				t.Fatal("message/tool lost or duplicated", final)
			}
			history := []any{shared.Object{"role": "user", "content": "Inspect mock setup"}}
			var callID string
			for _, raw := range shared.Arr(final["output"]) {
				history = append(history, raw)
				if item := shared.Obj(raw); item["type"] == "function_call" {
					callID = shared.Str(item["call_id"])
				}
			}
			history = append(history, shared.Object{"type": "function_call_output", "call_id": callID, "output": "synthetic-tool-result"})
			input["input"] = history
			status, body := requestRelay(t, r, input)
			answer := completedRelayResponse(t, status, body)
			if !strings.Contains(string(shared.Marshal(answer["output"])), `"text":"OK"`) {
				t.Fatal("continuation answer lost", answer)
			}
			if requests.Load() != 2 {
				t.Fatal("unexpected retry", requests.Load())
			}
		})
	}
}
