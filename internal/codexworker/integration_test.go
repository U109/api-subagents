package codexworker

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
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
)

// emitMockResponse 模拟 Responses SSE，模型响应完全在测试进程内生成，不访问外部接口。
func emitMockResponse(w http.ResponseWriter, items ...shared.Object) {
	w.Header().Set("Content-Type", "text/event-stream")
	output := []any{}
	for i, item := range items {
		output = append(output, item)
		fmt.Fprintf(w, "event: response.output_item.done\ndata: %s\n\n", shared.Marshal(shared.Object{"type": "response.output_item.done", "output_index": i, "item": item}))
	}
	response := shared.Object{"id": "mock-response", "object": "response", "created_at": 1, "status": "completed", "model": "arbitrary-model", "output": output, "usage": shared.Object{"input_tokens": 5, "output_tokens": 5, "total_tokens": 10}}
	fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", shared.Marshal(shared.Object{"type": "response.completed", "response": response}))
}

// finalItem 返回通用完成消息；模型名字不参与协议或工具能力选择。
func finalItem() shared.Object {
	return shared.Object{"type": "message", "id": "final", "role": "assistant", "status": "completed", "content": []any{shared.Object{"type": "output_text", "text": "WORKER_OK", "annotations": []any{}}}}
}

// TestCodexWorkerProtocols 用可选真实 CLI 验证四类协议的独立 worker，所有 home 和上游均隔离。
func TestCodexWorkerProtocols(t *testing.T) {
	bin := os.Getenv("CODEX_TEST_BIN")
	if bin == "" {
		t.Skip("set CODEX_TEST_BIN for isolated local-model integration")
	}
	for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				count.Add(1)
				data, _ := io.ReadAll(req.Body)
				if !strings.Contains(string(data), "unique-worker-assignment") {
					t.Error("plaintext task missing")
				}
				w.Header().Set("Content-Type", "application/json")
				switch protocol {
				case "responses":
					emitMockResponse(w, finalItem())
				case "compatible":
					_, _ = io.WriteString(w, `{"id":"c","model":"arbitrary-model","choices":[{"index":0,"message":{"role":"assistant","content":"WORKER_OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`)
				case "anthropic":
					_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"arbitrary-model","content":[{"type":"text","text":"WORKER_OK"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`)
				case "gemini":
					_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"WORKER_OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}`)
				}
			}))
			defer server.Close()
			_, profile := testutil.Config(t, protocol, server.URL)
			profile.Stream, profile.Model = false, "arbitrary-vendor-model"
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			result, err := (&Runner{Binary: bin}).Run(ctx, Request{Profile: profile, Task: "unique-worker-assignment. Reply WORKER_OK without tools.", Workspace: t.TempDir(), HomeRoot: t.TempDir(), Access: "read-only", MaxRequests: 3})
			if err != nil || result.Text != "WORKER_OK" || count.Load() != 1 || result.Requests != 1 {
				t.Fatalf("result=%+v requests=%d err=%v", result, count.Load(), err)
			}
		})
	}
}

// TestCodexWorkerEditsAndCommands 用本地模型驱动真实 Codex 先改文件、再运行验证命令，保留已有文件。
func TestCodexWorkerEditsAndCommands(t *testing.T) {
	bin := os.Getenv("CODEX_TEST_BIN")
	if bin == "" {
		t.Skip("set CODEX_TEST_BIN for isolated local-model integration")
	}
	for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) { checkCodexEdits(t, bin, protocol) })
	}
}

// checkCodexEdits 对每类协议执行相同的文件与命令验收，选择真实声明的工具名而不是模型品牌。
func checkCodexEdits(t *testing.T, bin, protocol string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	file, patch, command := "worker-test.ps1", "*** Begin Patch\n*** Add File: worker-test.ps1\n+if ((Get-Content -Raw -LiteralPath './existing.txt') -ne 'preserve me') { exit 1 }\n+Write-Output 'TEST_PASSED'\n*** End Patch", "powershell.exe -NoProfile -NonInteractive -File ./worker-test.ps1"
	if runtime.GOOS != "windows" {
		file, patch, command = "worker-test.sh", "*** Begin Patch\n*** Add File: worker-test.sh\n+test \"$(cat ./existing.txt)\" = 'preserve me' || exit 1\n+echo TEST_PASSED\n*** End Patch", "sh ./worker-test.sh"
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body shared.Object
		if json.NewDecoder(req.Body).Decode(&body) != nil {
			t.Error("bad request")
		}
		switch count.Add(1) {
		case 1:
			emitProtocolTool(t, w, protocol, body, "apply_patch", shared.Object{"input": patch})
		case 2:
			emitProtocolTool(t, w, protocol, body, "exec_command", shared.Object{"cmd": command, "workdir": root, "max_output_tokens": 1000, "yield_time_ms": 10000})
		default:
			emitProtocolTool(t, w, protocol, body, "", nil)
		}
	}))
	defer server.Close()
	_, profile := testutil.Config(t, protocol, server.URL)
	profile.Stream = false
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := (&Runner{Binary: bin}).Run(ctx, Request{Profile: profile, Task: "Create a test script, run it, preserve existing.txt and report the result.", Workspace: root, HomeRoot: t.TempDir(), Access: "workspace-write", MaxRequests: 5})
	if err != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, file)); err != nil {
		t.Fatalf("file not written, result=%+v: %v", result, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "existing.txt"))
	if string(data) != "preserve me" || len(result.Commands) == 0 || result.Commands[0]["exitCode"] == nil || shared.Int(result.Commands[0]["exitCode"]) != 0 || result.Commands[0]["status"] != "completed" || !result.WorkspaceMayHaveChanges || count.Load() != 3 {
		t.Fatalf("missing execution evidence: %+v", result)
	}
}

// emitProtocolTool 模拟四类上游的函数调用与最终消息，完整工具执行由真实 Codex 负责。
func emitProtocolTool(t *testing.T, w http.ResponseWriter, protocol string, body shared.Object, desired string, args shared.Object) {
	t.Helper()
	name := desired
	if protocol != "responses" && desired != "" {
		name = ""
		tools := shared.Arr(body["tools"])
		if protocol == "gemini" && len(tools) > 0 {
			tools = shared.Arr(shared.Obj(tools[0])["functionDeclarations"])
		}
		for _, raw := range tools {
			tool := shared.Obj(raw)
			if protocol == "compatible" {
				tool = shared.Obj(tool["function"])
			}
			candidate := shared.Str(tool["name"])
			if strings.HasSuffix(candidate, desired) || (desired == "apply_patch" && strings.Contains(candidate, "api_subagents_patch")) {
				name = candidate
				break
			}
		}
		if name == "" {
			t.Error("missing tool", protocol, desired)
			w.WriteHeader(400)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	var response shared.Object
	switch protocol {
	case "responses":
		if desired == "" {
			emitMockResponse(w, finalItem())
		} else if desired == "apply_patch" {
			emitMockResponse(w, shared.Object{"type": "custom_tool_call", "id": "patch-item", "call_id": "patch-call", "name": name, "input": args["input"]})
		} else {
			emitMockResponse(w, shared.Object{"type": "function_call", "id": "cmd-item", "call_id": "cmd-call", "name": name, "arguments": string(shared.Marshal(args))})
		}
		return
	case "compatible":
		message, reason := shared.Object{"role": "assistant", "content": "WORKER_OK"}, "stop"
		if desired != "" {
			message["content"] = nil
			message["tool_calls"] = []any{shared.Object{"id": desired + "-call", "type": "function", "function": shared.Object{"name": name, "arguments": string(shared.Marshal(args))}}}
			reason = "tool_calls"
		}
		response = shared.Object{"id": "chat", "model": "arbitrary-model", "choices": []any{shared.Object{"index": 0, "message": message, "finish_reason": reason}}, "usage": shared.Object{"prompt_tokens": 5, "completion_tokens": 5, "total_tokens": 10}}
	case "anthropic":
		content, reason := shared.Object{"type": "text", "text": "WORKER_OK"}, "end_turn"
		if desired != "" {
			content = shared.Object{"type": "tool_use", "id": desired + "-call", "name": name, "input": args}
			reason = "tool_use"
		}
		response = shared.Object{"id": "message", "type": "message", "role": "assistant", "model": "arbitrary-model", "content": []any{content}, "stop_reason": reason, "usage": shared.Object{"input_tokens": 5, "output_tokens": 5}}
	case "gemini":
		part := shared.Object{"text": "WORKER_OK"}
		if desired != "" {
			part = shared.Object{"functionCall": shared.Object{"name": name, "args": args}}
		}
		response = shared.Object{"candidates": []any{shared.Object{"content": shared.Object{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}, "usageMetadata": shared.Object{"promptTokenCount": 5, "candidatesTokenCount": 5, "totalTokenCount": 10}}
	}
	_ = json.NewEncoder(w).Encode(response)
}

// TestCodexWorkerReadOnlyAndLimit 验证项目配置不能扩大 worker 权限，拒绝的工具结果可回传且模型请求上限真实生效。
func TestCodexWorkerReadOnlyAndLimit(t *testing.T) {
	bin := os.Getenv("CODEX_TEST_BIN")
	if bin == "" {
		t.Skip("set CODEX_TEST_BIN for isolated local-model integration")
	}
	for _, limit := range []int{2, 1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			root := t.TempDir()
			// 即使项目有宽权限配置，也不能覆盖执行器明确设置的权限与 provider。
			if err := os.Mkdir(filepath.Join(root, ".codex"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".codex", "config.toml"), []byte("sandbox_mode = 'danger-full-access'\nmodel = 'wrong-model'\nmodel_provider = 'openai'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var refusal atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				data, _ := io.ReadAll(req.Body)
				if calls.Add(1) == 1 {
					emitMockResponse(w, shared.Object{"type": "custom_tool_call", "id": "patch-item", "call_id": "patch-call", "name": "apply_patch", "input": "*** Begin Patch\n*** Add File: denied.txt\n+must not write\n*** End Patch"})
				} else {
					refusal.Store(strings.Contains(string(data), "read-only") || strings.Contains(string(data), "rejected"))
					emitMockResponse(w, finalItem())
				}
			}))
			defer server.Close()
			_, profile := testutil.Config(t, "responses", server.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			result, err := (&Runner{Binary: bin}).Run(ctx, Request{Profile: profile, Task: "Try the mock patch and report the permission result.", Workspace: root, HomeRoot: t.TempDir(), Access: "read-only", MaxRequests: limit})
			if limit == 1 {
				var op *shared.OpError
				if !errors.As(err, &op) || op.Code != "TASK_STEP_LIMIT" || calls.Load() != 1 {
					t.Fatal(result, err, calls.Load())
				}
			} else if err != nil || !refusal.Load() {
				t.Fatal(result, err, refusal.Load())
			}
			if result.WorkspaceMayHaveChanges {
				t.Fatal("read-only reported write access")
			}
			if _, err := os.Stat(filepath.Join(root, "denied.txt")); !os.IsNotExist(err) {
				t.Fatal("read-only patch wrote a file", err)
			}
		})
	}
}
