package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/shared"
)

// codexBinary 仅在明确指定本机测试 CLI 时执行真实 Codex；所有模型和配置仍位于隔离目录。
func codexBinary(t *testing.T) string {
	t.Helper()
	path := os.Getenv("CODEX_TEST_BIN")
	if path == "" {
		t.Skip("set CODEX_TEST_BIN to run isolated Codex integration")
	}
	return path
}

// TestCodexPrimaryModel 验证没有登录凭据的真实 Codex 能通过四种协议获得回答，不访问付费 API。
func TestCodexPrimaryModel(t *testing.T) {
	bin := codexBinary(t)
	for _, protocol := range []string{"compatible", "responses", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			stream := protocol != "anthropic"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var body shared.Object
				json.NewDecoder(req.Body).Decode(&body)
				switch protocol {
				case "compatible":
					if body["reasoning_effort"] != "high" {
						t.Error("Codex effort not forwarded", body["reasoning_effort"])
					}
				case "responses":
					if shared.Obj(body["reasoning"])["effort"] != "high" {
						t.Error("Codex effort not forwarded", body["reasoning"])
					}
				case "anthropic":
					if shared.Obj(body["thinking"])["type"] != "enabled" {
						t.Error("Claude thinking missing")
					}
				case "gemini":
					if shared.Obj(shared.Obj(body["generationConfig"])["thinkingConfig"])["thinkingBudget"] == nil {
						t.Error("Gemini thinking missing")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				io.WriteString(w, upstreamReply(protocol, stream))
			}))
			defer server.Close()
			r := testRelay(t, protocol, server.URL, stream)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			root := t.TempDir()
			answer := filepath.Join(root, "answer.txt")
			cmd := exec.CommandContext(ctx, bin, "exec", "--ephemeral", "--skip-git-repo-check", "--json", "-s", "read-only", "-c", `model_reasoning_effort="high"`, "-m", "api-subagents/demo", "-C", root, "-o", answer, "Reply OK without using tools.")
			cmd.Env = append(os.Environ(), "CODEX_HOME="+r.Codex.Home)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Codex failed: %v\n%s", err, output)
			}
			data, _ := os.ReadFile(answer)
			if strings.TrimSpace(string(data)) != "OK" || r.Snapshot().Requests != 1 {
				t.Fatalf("answer=%q, requests=%d\n%s", data, r.Snapshot().Requests, output)
			}
		})
	}
}

// TestCodexModelList 检查桌面使用的 app-server 模型列表会显示默认路由及单独连接。
func TestCodexModelList(t *testing.T) {
	bin := codexBinary(t)
	r := testRelay(t, "compatible", "http://127.0.0.1:9", true)
	config, _ := r.Store.Read()
	profile := config.Models["demo"]
	profile.ReasoningEffort = "low"
	config.Models["demo"] = profile
	if err := r.Store.Save(config); err != nil {
		t.Fatal(err)
	}
	if err := r.RefreshCatalog(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "app-server", "--listen", "stdio://")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "CODEX_HOME="+r.Codex.Home)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); cancel(); _ = cmd.Wait() }()
	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(stdout)
	encoder.Encode(shared.Object{"id": 1, "method": "initialize", "params": shared.Object{"clientInfo": shared.Object{"name": "api-subagents-tests", "version": "0.3.0"}}})
	var value shared.Object
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if value["error"] != nil {
		t.Fatal(value)
	}
	encoder.Encode(shared.Object{"method": "initialized", "params": shared.Object{}})
	encoder.Encode(shared.Object{"id": 2, "method": "model/list", "params": shared.Object{}})
	for {
		value = nil
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("model/list: %v %s", err, stderr.String())
		}
		if shared.Int(value["id"]) != 2 {
			continue
		}
		if value["error"] != nil {
			t.Fatal(value)
		}
		models := map[string]bool{}
		for _, item := range shared.Arr(shared.Obj(value["result"])["data"]) {
			model := shared.Obj(item)
			models[shared.Str(model["model"])] = true
			if len(shared.Arr(model["supportedReasoningEfforts"])) < 5 {
				t.Fatal("reasoning picker unavailable", model)
			}
			if model["defaultReasoningEffort"] != "low" {
				t.Fatal("connection default missing from catalog", model)
			}
		}
		if !models["api-subagents"] || !models["api-subagents/demo"] {
			t.Fatalf("custom models missing; stderr: %s", stderr.String())
		}
		break
	}
	if r.Snapshot().Requests != 0 {
		t.Fatal("listing models generated a paid request")
	}
}

// TestCodexPatchPermissions 验证真实 Codex 收到补丁调用并执行自身只读权限检查，拒绝结果能传回下一轮。
func TestCodexPatchPermissions(t *testing.T) {
	bin := codexBinary(t)
	requests := 0
	permissionReported := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var input shared.Object
		json.NewDecoder(req.Body).Decode(&input)
		requests++
		if requests > 1 {
			body := string(shared.Marshal(input))
			permissionReported = strings.Contains(body, "read-only") && strings.Contains(body, "patch")
			io.WriteString(w, upstreamReply("anthropic", false))
			return
		}
		name := ""
		for _, raw := range shared.Arr(input["tools"]) {
			candidate := shared.Str(shared.Obj(raw)["name"])
			if strings.Contains(candidate, "api_subagents_patch") {
				name = candidate
			}
		}
		if name == "" {
			t.Error("Codex patch tool missing")
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(shared.Object{"id": "msg1", "type": "message", "role": "assistant", "content": []any{shared.Object{"type": "tool_use", "id": "patch1", "name": name, "input": shared.Object{"input": "*** Begin Patch\n*** Add File: probe.txt\n+hello\n*** End Patch"}}}, "stop_reason": "tool_use", "usage": shared.Object{"input_tokens": 10, "output_tokens": 5}})
	}))
	defer server.Close()
	r := testRelay(t, "anthropic", server.URL, false)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	root := t.TempDir()
	cmd := exec.CommandContext(ctx, bin, "exec", "--ephemeral", "--skip-git-repo-check", "--json", "-s", "read-only", "-c", "features.plugins=false", "-C", root, "Try the supplied mock patch; report the permission outcome.")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+r.Codex.Home)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Codex patch failed: %v\n%s", err, output)
	}
	_, err = os.Stat(filepath.Join(root, "probe.txt"))
	if !os.IsNotExist(err) || requests != 2 || !permissionReported {
		t.Fatalf("sandbox result: requests %d, reported %v, file error %v\n%s", requests, permissionReported, err, output)
	}
}
