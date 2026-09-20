package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	"github.com/U109/api-subagents/internal/shared"
)

// TestCodexProviderReload 验证模式切换后重新启动 Codex 会加载正确提供商，且本地别名经网关还原为上游模型。
// 配置、进程和接口全部隔离；旧进程是否热加载只记录实际行为，不依赖特定 Codex 版本的缓存实现。
func TestCodexProviderReload(t *testing.T) {
	bin := codexBinary(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		var body shared.Object
		json.NewDecoder(req.Body).Decode(&body)
		if body["model"] != "mock-model" || req.Header.Get("Authorization") != "Bearer synthetic-relay-key" || req.Header.Get("X-Api-Subagents-Token") != "" {
			t.Error("local alias or credentials crossed the upstream boundary")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("responses", true))
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	if err := r.Disable(); err != nil {
		t.Fatal(err)
	}
	original := "model='original-model'\nmodel_provider='original'\n[model_providers.original]\nname='Original mock'\nbase_url='" + server.URL + "/v1'\nwire_api='responses'\nrequires_openai_auth=false\n"
	if err := os.WriteFile(filepath.Join(r.Codex.Home, "config.toml"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	oldClient := startCodexRPC(t, bin, r.Codex.Home, root)
	initial := oldClient.call(t, "thread/start", shared.Object{"ephemeral": true})
	if initial["modelProvider"] != "original" {
		t.Fatal("original provider missing", initial["modelProvider"])
	}
	if err := r.Enable("demo"); err != nil {
		t.Fatal(err)
	}
	alias := codexconfig.RelayModelAlias("demo", "mock-model")
	stale := oldClient.call(t, "thread/start", shared.Object{"model": alias, "ephemeral": true})
	t.Logf("already-running Codex selected provider: %v", stale["modelProvider"])
	bound := oldClient.call(t, "thread/start", shared.Object{"model": alias, "modelProvider": "original", "ephemeral": true})
	if bound["modelProvider"] != "original" {
		t.Fatal("explicit provider binding unexpectedly changed when selecting a model", bound["modelProvider"])
	}
	t.Run("restarted-with-relay", func(t *testing.T) {
		client := startCodexRPC(t, bin, r.Codex.Home, root)
		result := client.call(t, "thread/start", shared.Object{"model": alias, "ephemeral": true})
		if result["modelProvider"] != "api_subagents" {
			t.Fatal("restarted Codex did not select local provider", result["modelProvider"])
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := codexTestCommand(ctx, bin, "exec", "--ephemeral", "--skip-git-repo-check", "--json", "-s", "read-only", "-m", alias, "-C", root, "Reply OK without tools.")
	cmd.Env = codexTestEnv(r.Codex.Home)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil || requests.Load() != 1 {
		t.Fatalf("reloaded route failed: %v, requests=%d\n%s", err, requests.Load(), output)
	}
	if err := r.Disable(); err != nil {
		t.Fatal(err)
	}
	t.Run("restarted-after-disable", func(t *testing.T) {
		client := startCodexRPC(t, bin, r.Codex.Home, root)
		result := client.call(t, "thread/start", shared.Object{"ephemeral": true})
		if result["modelProvider"] != "original" || result["model"] != "original-model" {
			t.Fatal("original route not restored", result["modelProvider"], result["model"])
		}
	})
}

// TestCodexInactiveThread 用真实桌面协议重开已停用提供商的历史对话，再开启后能继续；所有生成仅访问本机。
func TestCodexInactiveThread(t *testing.T) {
	bin := codexBinary(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		var request shared.Object
		json.NewDecoder(req.Body).Decode(&request)
		if request["reasoning_effort"] != nil {
			t.Error("unspecified effort must preserve service default", request["reasoning_effort"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("compatible", true))
	}))
	defer server.Close()
	r := testRelay(t, "compatible", server.URL, true)
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	cmd := codexTestCommand(ctx, bin, "exec", "--skip-git-repo-check", "--json", "-s", "read-only", "-m", "api-subagents/demo", "-C", root, "Reply OK without using tools.")
	cmd.Env = codexTestEnv(r.Codex.Home)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("initial conversation: %v\n%s", err, output)
	}
	id := ""
	for _, line := range bytes.Split(output, []byte("\n")) {
		var event shared.Object
		if json.Unmarshal(line, &event) == nil && event["type"] == "thread.started" {
			id = shared.Str(event["thread_id"])
		}
	}
	if id == "" {
		t.Fatal("persisted thread ID missing")
	}
	if err := r.Disable(); err != nil {
		t.Fatal(err)
	}
	t.Run("open-while-disabled", func(t *testing.T) {
		client := startCodexRPC(t, bin, r.Codex.Home, root)
		result := client.call(t, "thread/resume", shared.Object{"threadId": id, "modelProvider": "api_subagents", "model": "api-subagents/demo"})
		thread := shared.Obj(result["thread"])
		if thread["id"] != id || !strings.Contains(string(shared.Marshal(thread)), "OK") {
			t.Fatal("old conversation did not reopen with history", result)
		}
		if requests.Load() != 1 {
			t.Fatal("opening history made a generation request")
		}
	})
	if err := r.Enable("demo"); err != nil {
		t.Fatal(err)
	}
	cmd = codexTestCommand(ctx, bin, "exec", "resume", "--skip-git-repo-check", "--json", id, "Reply OK again without tools.")
	cmd.Env = codexTestEnv(r.Codex.Home)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = root
	output, err = cmd.CombinedOutput()
	if err != nil || requests.Load() != 2 {
		t.Fatalf("continue after enabling: %v, requests=%d\n%s", err, requests.Load(), output)
	}
}

type codexRPC struct {
	encoder       *json.Encoder
	decoder       *json.Decoder
	id            int
	notifications []shared.Object
}

// startCodexRPC 启动隔离的真实 app-server；子测试结束立即关闭，避免持有会话数据库或沿用旧配置。
func startCodexRPC(t *testing.T, bin, home, root string) *codexRPC {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	cmd := codexTestCommand(ctx, bin, "app-server", "--listen", "stdio://")
	cmd.Env = codexTestEnv(home)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// 先让 app-server 正常释放会话数据库，立即强杀会让 Windows 的子进程仍持有临时目录。
		stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cancel()
			<-done
		}
		stdout.Close()
		cancel()
	})
	client := &codexRPC{encoder: json.NewEncoder(stdin), decoder: json.NewDecoder(stdout)}
	client.call(t, "initialize", shared.Object{"clientInfo": shared.Object{"name": "api-subagents-tests", "version": "0.3.1"}})
	if err := client.encoder.Encode(shared.Object{"method": "initialized", "params": shared.Object{}}); err != nil {
		t.Fatal(err)
	}
	return client
}

// call 读取对应请求的响应并保存期间通知；每次重新解码对象，避免前一条通知遗留 ID 或 result。
func (c *codexRPC) call(t *testing.T, method string, params shared.Object) shared.Object {
	t.Helper()
	c.id++
	if err := c.encoder.Encode(shared.Object{"id": c.id, "method": method, "params": params}); err != nil {
		t.Fatal(err)
	}
	for {
		var value shared.Object
		if err := c.decoder.Decode(&value); err != nil {
			t.Fatal(method, err)
		}
		if shared.Int(value["id"]) != c.id {
			if value["method"] != nil {
				c.notifications = append(c.notifications, value)
			}
			continue
		}
		if value["error"] != nil {
			t.Fatal(method, value["error"])
		}
		return shared.Obj(value["result"])
	}
}
