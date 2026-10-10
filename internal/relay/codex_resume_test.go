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
	var originalRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body shared.Object
		json.NewDecoder(req.Body).Decode(&body)
		if req.Header.Get("Authorization") == "Bearer synthetic-original-key" {
			originalRequests.Add(1)
			if body["model"] != "original-model" {
				t.Error("local alias escaped through the original provider")
			}
		} else if body["model"] != "mock-model" || req.Header.Get("Authorization") != "Bearer synthetic-relay-key" || req.Header.Get("X-Api-Subagents-Token") != "" {
			t.Error("local alias or credentials crossed the upstream boundary")
		} else {
			requests.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("responses", true))
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	if err := r.Disable(); err != nil {
		t.Fatal(err)
	}
	original := "model='original-model'\nmodel_provider='original'\n[model_providers.original]\nname='Original mock'\nbase_url='" + server.URL + "/v1'\nwire_api='responses'\nrequires_openai_auth=false\nhttp_headers={Authorization='Bearer synthetic-original-key'}\n"
	if err := os.WriteFile(filepath.Join(r.Codex.Home, "config.toml"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	initialCmd := codexTestCommand(ctx, bin, "exec", "--skip-git-repo-check", "--json", "-s", "read-only", "-C", root, "Reply OK without tools.")
	initialCmd.Env = codexTestEnv(r.Codex.Home)
	initialCmd.Dir = root
	output, err := initialCmd.CombinedOutput()
	if err != nil || originalRequests.Load() != 1 {
		t.Fatalf("original provider setup failed: %v\n%s", err, output)
	}
	threadID := ""
	for _, line := range bytes.Split(output, []byte("\n")) {
		var item shared.Object
		if json.Unmarshal(line, &item) == nil && item["type"] == "thread.started" {
			threadID = shared.Str(item["thread_id"])
		}
	}
	if threadID == "" {
		t.Fatal("original conversation was not saved")
	}
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
	cmd := codexTestCommand(ctx, bin, "exec", "--ephemeral", "--skip-git-repo-check", "--json", "-s", "read-only", "-m", alias, "-C", root, "Reply OK without tools.")
	cmd.Env = codexTestEnv(r.Codex.Home)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil || requests.Load() != 1 {
		t.Fatalf("reloaded route failed: %v, requests=%d\n%s", err, requests.Load(), output)
	}
	for i, selected := range []string{alias, "mock-model"} {
		cmd = codexTestCommand(ctx, bin, "-c", "model_provider=\"original\"", "exec", "resume", "--skip-git-repo-check", "--json", "-m", selected, threadID, "Reply OK without tools.")
		cmd.Env = codexTestEnv(r.Codex.Home)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil || requests.Load() != int32(i+2) {
			t.Fatalf("old provider identity bypassed relay: %v, requests=%d\n%s", err, requests.Load(), output)
		}
	}
	if err := r.Disable(); err != nil {
		t.Fatal(err)
	}
	cmd = codexTestCommand(ctx, bin, "exec", "resume", "--skip-git-repo-check", "--json", "-m", "original-model", threadID, "Reply OK without tools.")
	cmd.Env = codexTestEnv(r.Codex.Home)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil || originalRequests.Load() != 2 || requests.Load() != 3 {
		t.Fatalf("restored conversation route failed: %v\n%s", err, output)
	}
	t.Run("restarted-after-disable", func(t *testing.T) {
		client := startCodexRPC(t, bin, r.Codex.Home, root)
		result := client.call(t, "thread/start", shared.Object{"ephemeral": true})
		if result["modelProvider"] != "original" || result["model"] != "original-model" {
			t.Fatal("original route not restored", result["modelProvider"], result["model"])
		}
	})
}

// TestCodexInactiveThread 用隔离 Codex 重开历史对话、改用模拟账号模型，再开启网关继续；所有生成仅访问本机。
func TestCodexInactiveThread(t *testing.T) {
	bin := codexBinary(t)
	var requests atomic.Int32
	var accountRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		var request shared.Object
		json.NewDecoder(req.Body).Decode(&request)
		protocol := "compatible"
		if strings.HasSuffix(req.URL.Path, "/responses") {
			protocol = "responses"
			accountRequests.Add(1)
			if req.Header.Get("Authorization") != "Bearer synthetic-account-test-key" {
				t.Error("Codex auth was not used for historical provider")
			}
			if request["model"] != "mock-model" {
				t.Error("account model was not selected", request["model"])
			}
		}
		if request["reasoning_effort"] != nil {
			t.Error("unspecified effort must preserve service default", request["reasoning_effort"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply(protocol, true))
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
	// 只把合成 Key 写入隔离 CODEX_HOME，模拟从网关切回 Codex 自己的登录凭据。
	login := codexTestCommand(ctx, bin, "-c", "cli_auth_credentials_store=\"file\"", "login", "--with-api-key")
	login.Env = codexTestEnv(r.Codex.Home)
	login.Stdin = strings.NewReader("synthetic-account-test-key\n")
	if output, err := login.CombinedOutput(); err != nil {
		t.Fatalf("synthetic Codex login failed: %v\n%s", err, output)
	}
	// 只在隔离测试进程中覆盖账号提供商地址，绝不向真实付费接口发送请求。
	providerArgs := []string{"-c", "model_provider=\"api_subagents\"", "-c", "model_providers.api_subagents.base_url=" + string(shared.Marshal(server.URL+"/v1"))}
	t.Run("desktop-resume-with-codex-auth", func(t *testing.T) {
		client := startCodexRPC(t, bin, r.Codex.Home, root, providerArgs...)
		resumed := client.call(t, "thread/resume", shared.Object{"threadId": id, "modelProvider": "api_subagents", "model": "mock-model"})
		if shared.Str(shared.Obj(resumed["thread"])["id"]) != id {
			t.Fatal("old conversation was not resumed with its provider", resumed)
		}
		turn := client.call(t, "turn/start", shared.Object{"threadId": id, "model": "mock-model", "input": []any{shared.Object{"type": "text", "text": "Reply OK without tools.", "text_elements": []any{}}}})
		client.waitTurn(t, shared.Str(shared.Obj(turn["turn"])["id"]))
		if accountRequests.Load() != 1 {
			t.Fatal("resumed desktop conversation did not reach Codex login route")
		}
	})
	cmd = codexTestCommand(ctx, bin, append(append([]string{}, providerArgs...), "exec", "resume", "--skip-git-repo-check", "--json", "-m", "mock-model", id, "Reply OK with the account model without tools.")...)
	cmd.Env = codexTestEnv(r.Codex.Home)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = root
	output, err = cmd.CombinedOutput()
	if err != nil || accountRequests.Load() != 2 || requests.Load() != 3 {
		t.Fatalf("old conversation did not switch to Codex auth: %v, requests=%d, account=%d\n%s", err, requests.Load(), accountRequests.Load(), output)
	}
	if err := r.Enable("demo"); err != nil {
		t.Fatal(err)
	}
	cmd = codexTestCommand(ctx, bin, "exec", "resume", "--skip-git-repo-check", "--json", "-m", "api-subagents/demo", id, "Reply OK again without tools.")
	cmd.Env = codexTestEnv(r.Codex.Home)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = root
	output, err = cmd.CombinedOutput()
	if err != nil || requests.Load() != 4 {
		t.Fatalf("continue after enabling: %v, requests=%d\n%s", err, requests.Load(), output)
	}
}

type codexRPC struct {
	encoder       *json.Encoder
	decoder       *json.Decoder
	id            int
	notifications []shared.Object
}

// startCodexRPC 启动隔离的真实 app-server，可给本机模拟接口附加配置；子测试结束立即关闭以释放会话数据库。
func startCodexRPC(t *testing.T, bin, home, root string, configArgs ...string) *codexRPC {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	cmd := codexTestCommand(ctx, bin, append(append([]string{}, configArgs...), "app-server", "--listen", "stdio://")...)
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
