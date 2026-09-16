package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
	workfiles "github.com/U109/api-subagents/internal/workspace"
)

// modelReply 构造四种协议的本地模拟响应，同时携带下一轮必须保留的签名。
func modelReply(protocol string, call *shared.Call, text string) shared.Object {
	switch protocol {
	case "responses":
		output := []any{shared.Object{"type": "reasoning", "id": "r1", "encrypted_content": "opaque-signature"}}
		if call != nil {
			output = append(output, shared.Object{"type": "function_call", "id": "fc_" + call.ID, "call_id": call.ID, "name": call.Name, "arguments": string(shared.Marshal(call.Args))})
		} else {
			output = append(output, shared.Object{"type": "message", "id": "msg1", "role": "assistant", "content": []any{shared.Object{"type": "output_text", "text": text}}})
		}
		return shared.Object{"status": "completed", "output": output}
	case "anthropic":
		content := []any{shared.Object{"type": "thinking", "thinking": "brief", "signature": "opaque-signature"}}
		reason := "end_turn"
		if call != nil {
			content = append(content, shared.Object{"type": "tool_use", "id": call.ID, "name": call.Name, "input": call.Args})
			reason = "tool_use"
		} else {
			content = append(content, shared.Object{"type": "text", "text": text})
		}
		return shared.Object{"content": content, "stop_reason": reason}
	case "gemini":
		parts := []any{}
		if call != nil {
			parts = append(parts, shared.Object{"functionCall": shared.Object{"id": call.ID, "name": call.Name, "args": call.Args}, "thoughtSignature": "opaque-signature"})
		} else {
			parts = append(parts, shared.Object{"text": text})
		}
		return shared.Object{"candidates": []any{shared.Object{"content": shared.Object{"role": "model", "parts": parts}, "finishReason": "STOP"}}}
	default:
		message := shared.Object{"role": "assistant", "content": text}
		reason := "stop"
		if call != nil {
			message["tool_calls"] = []any{shared.Object{"id": call.ID, "type": "function", "function": shared.Object{"name": call.Name, "arguments": string(shared.Marshal(call.Args))}}}
			reason = "tool_calls"
		}
		return shared.Object{"choices": []any{shared.Object{"message": message, "finish_reason": reason}}}
	}
}

// streamReply 把同一模拟回复拆成厂商 SSE 事件，用于验证分片参数与结束边界。
func streamReply(protocol string, reply shared.Object) string {
	var out strings.Builder
	send := func(kind string, value any) {
		if kind != "" {
			fmt.Fprintf(&out, "event: %s\r\n", kind)
		}
		fmt.Fprintf(&out, "data: %s\r\n\r\n", shared.Marshal(value))
	}
	chat := func(delta shared.Object, reason any) {
		choice := shared.Object{"index": 0, "delta": delta}
		if reason != nil {
			choice["finish_reason"] = reason
		}
		send("", shared.Object{"choices": []any{choice}})
	}
	switch protocol {
	case "responses":
		send("response.completed", shared.Object{"type": "response.completed", "response": reply})
	case "anthropic":
		send("message_start", shared.Object{"type": "message_start", "message": shared.Object{"role": "assistant", "content": []any{}}})
		for index, value := range shared.Arr(reply["content"]) {
			block := shared.Obj(value)
			start := shared.Object{}
			for key, value := range block {
				start[key] = value
			}
			if block["type"] == "tool_use" {
				start["input"] = shared.Object{}
			}
			send("content_block_start", shared.Object{"type": "content_block_start", "index": index, "content_block": start})
			if block["type"] == "tool_use" {
				args := string(shared.Marshal(block["input"]))
				for _, part := range []string{args[:len(args)/2], args[len(args)/2:]} {
					delta := shared.Object{"type": "input_json_delta", "partial_json": part}
					send("content_block_delta", shared.Object{"type": "content_block_delta", "index": index, "delta": delta})
				}
			}
			send("content_block_stop", shared.Object{"type": "content_block_stop", "index": index})
		}
		send("message_delta", shared.Object{"type": "message_delta", "delta": shared.Object{"stop_reason": reply["stop_reason"]}})
		send("message_stop", shared.Object{"type": "message_stop"})
	case "gemini":
		send("", reply)
	default:
		choice := shared.Obj(shared.Arr(reply["choices"])[0])
		message := shared.Obj(choice["message"])
		delta := shared.Object{"role": "assistant", "content": message["content"]}
		calls := shared.Arr(message["tool_calls"])
		if len(calls) > 0 {
			call := shared.Obj(calls[0])
			function := shared.Obj(call["function"])
			args := shared.Str(function["arguments"])
			partial := shared.Object{"index": 0, "id": call["id"], "function": shared.Object{"name": function["name"], "arguments": args[:len(args)/2]}}
			delta["tool_calls"] = []any{partial}
		}
		chat(delta, nil)
		if len(calls) > 0 {
			args := shared.Str(shared.Obj(shared.Obj(calls[0])["function"])["arguments"])
			partial := shared.Object{"index": 0, "function": shared.Object{"arguments": args[len(args)/2:]}}
			chat(shared.Object{"tool_calls": []any{partial}}, nil)
		}
		chat(shared.Object{}, choice["finish_reason"])
		out.WriteString("data: [DONE]\r\n\r\n")
	}
	return out.String()
}

// TestMultiTurnProtocols 通过真实本地 HTTP 验证四协议的读取、建议、签名历史及精简结果。
func TestMultiTurnProtocols(t *testing.T) {
	for _, protocol := range []string{"compatible", "responses", "anthropic", "gemini"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", protocol, streaming), func(t *testing.T) {
				root := t.TempDir()
				original := "const greeting = 'hello 世界';\n"
				os.WriteFile(filepath.Join(root, "sample.js"), []byte(original), 0644)
				count := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, _ := io.ReadAll(r.Body)
					if count > 0 && protocol != "compatible" && !strings.Contains(string(data), "opaque-signature") {
						t.Error("provider signature lost")
					}
					var call *shared.Call
					switch count {
					case 0:
						call = &shared.Call{ID: "call1", Name: "read_file", Args: shared.Object{"path": "sample.js"}}
					case 1:
						if !strings.Contains(string(data), shared.Hash([]byte(original))) {
							t.Error("file tool result missing")
						}
						call = &shared.Call{ID: "call2", Name: "propose_edit", Args: shared.Object{"path": "sample.js", "expectedSha256": shared.Hash([]byte(original)), "edits": []workfiles.Edit{{OldText: "hello", NewText: "你好"}}}}
					}
					count++
					reply := modelReply(protocol, call, "已完成")
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, streamReply(protocol, reply))
					} else {
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(reply)
					}
				}))
				defer server.Close()
				store, _ := testutil.Config(t, protocol, server.URL)
				manager := NewManager(store, t.TempDir(), nil)
				defer manager.Close()
				submitted, err := manager.Submit(TaskRequest{Model: "demo", Task: "修改问候", Workspace: root})
				if err != nil {
					t.Fatal(err)
				}
				value, err := manager.Wait(context.Background(), shared.Str(submitted["task_id"]), 25000)
				if err != nil || value["status"] != "completed" {
					t.Fatal(value, err)
				}
				if count != 3 {
					t.Fatalf("requests=%d", count)
				}
				compact, _ := manager.Present(value, "")
				if strings.Contains(string(shared.Marshal(compact)), original) {
					t.Fatal("full source in compact result")
				}
				changes := shared.Arr(value["changes"])
				if len(changes) != 1 || shared.Obj(changes[0])["redacted"] == true {
					t.Fatal("ordinary proposal incorrectly redacted")
				}
				data, _ := os.ReadFile(filepath.Join(root, "sample.js"))
				if string(data) != original {
					t.Fatal("worker wrote project")
				}
				if _, err = workfiles.ApplyProposals(shared.Str(value["task_id"]), root, nil, manager.Storage); err != nil {
					t.Fatal(err)
				}
				data, _ = os.ReadFile(filepath.Join(root, "sample.js"))
				if !strings.Contains(string(data), "你好") {
					t.Fatal("proposal not applied")
				}
			})
		}
	}
}

// TestIncompleteAndTruncatedStreams 断流和输出截断都不能提交历史或执行未完成调用。
func TestIncompleteAndTruncatedStreams(t *testing.T) {
	for _, protocol := range []string{"compatible", "responses", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			reply := modelReply(protocol, &shared.Call{ID: "call", Name: "read_file", Args: shared.Object{"path": "x"}}, "")
			complete := streamReply(protocol, reply)
			var partial string
			switch protocol {
			case "compatible":
				partial = strings.Split(complete, "data: [DONE]")[0]
			case "anthropic":
				partial = strings.Split(complete, "event: message_stop")[0]
			case "responses":
				partial = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
			case "gemini":
				partial = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, partial)
			}))
			defer server.Close()
			_, p := testutil.Config(t, protocol, server.URL)
			history := providers.InitialHistory(p, "task")
			before := string(shared.Marshal(history))
			if _, err := providers.NewProvider().Turn(context.Background(), p, &history, "system", nil, nil); err == nil {
				t.Fatal("partial stream accepted")
			}
			if string(shared.Marshal(history)) != before {
				t.Fatal("partial history committed")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	_, p := testutil.Config(t, "compatible", server.URL)
	history := providers.InitialHistory(p, "task")
	if _, err := providers.NewProvider().Turn(context.Background(), p, &history, "", nil, nil); err == nil || len(history) != 1 {
		t.Fatal("truncation accepted")
	}
}

// TestSSEByteBoundaries 验证一字节 UTF-8、CRLF、多行 data 和注释心跳的增量解析。
func TestSSEByteBoundaries(t *testing.T) {
	text := ": heartbeat\r\nevent: result\r\ndata: {\r\ndata: \"text\":\"中文\"}\r\n\r\n"
	calls := 0
	decoder := providers.NewEventDecoder(func(event, data string) error {
		calls++
		var value shared.Object
		if event != "result" || json.Unmarshal([]byte(data), &value) != nil || value["text"] != "中文" {
			t.Fatal(event, data)
		}
		return nil
	})
	for _, b := range []byte(text) {
		if err := decoder.Feed([]byte{b}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := decoder.Feed(nil, true); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

// TestRequestTimeouts 首个数据等待涵盖仅有响应头的情况，接收中断使用独立错误码。
func TestRequestTimeouts(t *testing.T) {
	for _, kind := range []string{"no-headers", "headers-only", "idle", "heartbeat"} {
		t.Run(kind, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				io.Copy(io.Discard, r.Body)
				if kind != "no-headers" {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				if kind == "idle" {
					io.WriteString(w, ": ping\n\n")
					w.(http.Flusher).Flush()
				}
				if kind == "heartbeat" {
					for i := 0; i < 8; i++ {
						io.WriteString(w, ": ping\n\n")
						w.(http.Flusher).Flush()
						time.Sleep(15 * time.Millisecond)
					}
					io.WriteString(w, streamReply("compatible", modelReply("compatible", nil, "OK")))
					return
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			_, p := testutil.Config(t, "compatible", server.URL)
			provider := providers.NewProvider()
			provider.FirstWait = 80 * time.Millisecond
			provider.IdleWait = 80 * time.Millisecond
			history := providers.InitialHistory(p, "task")
			_, err := provider.Turn(context.Background(), p, &history, "", nil, nil)
			if kind == "heartbeat" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var op *shared.OpError
			if !errors.As(err, &op) {
				t.Fatal(err)
			}
			want := "FIRST_RESPONSE_TIMEOUT"
			if kind == "idle" {
				want = "STREAM_IDLE_TIMEOUT"
			}
			if op.Code != want || requests.Load() != 1 {
				t.Fatal(op, requests.Load())
			}
		})
	}
}

// TestTaskCancellationAndCapacity 并发提交不会越过活动任务上限，取消队列不发送付费请求。
func TestTaskCancellationAndCapacity(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		requests.Add(1)
		<-r.Context().Done()
	}))
	defer server.Close()
	store, _ := testutil.Config(t, "compatible", server.URL)
	config, _ := store.Read()
	config.MaxConcurrent = 1
	store.Save(config)
	m := NewManager(store, t.TempDir(), nil)
	defer m.Close()
	root := t.TempDir()
	var wg sync.WaitGroup
	var accepted atomic.Int32
	ids := make(chan string, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := m.Submit(TaskRequest{Model: "demo", Task: "task", Workspace: root})
			if err == nil {
				accepted.Add(1)
				ids <- shared.Str(v["task_id"])
			}
		}()
	}
	wg.Wait()
	close(ids)
	if accepted.Load() != 24 {
		t.Fatal(accepted.Load())
	}
	for id := range ids {
		value, _ := m.Get(id)
		if value["status"] == "queued" {
			m.Cancel(id)
		}
	}
	m.Close()
	if requests.Load() > 1 {
		t.Fatal("cancelled queue sent network requests")
	}
}

// TestTaskDeadlineAndStorageFailure 总时限可以停止持续心跳，结果无法保存时仍完成等待并保留错误。
func TestTaskDeadlineAndStorageFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
				io.WriteString(w, ": ping\n\n")
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer server.Close()
	store, _ := testutil.Config(t, "compatible", server.URL)
	storage := filepath.Join(t.TempDir(), "file")
	os.WriteFile(storage, []byte("not a directory"), 0600)
	m := NewManager(store, storage, nil)
	m.Deadline = 70 * time.Millisecond
	defer m.Close()
	v, err := m.Submit(TaskRequest{Model: "demo", Task: "task", Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	value, err := m.Wait(context.Background(), shared.Str(v["task_id"]), 2000)
	if err != nil || value["status"] != "failed" || value["errorCode"] != "TASK_TIMEOUT" || value["storageWarning"] == nil {
		t.Fatal(value, err)
	}
	compact, _ := m.Present(value, "")
	if compact["applyCommand"] != nil || compact["resultFile"] != nil {
		t.Fatal("advertised unavailable result")
	}
}
