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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/shared"
)

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
	cmd := exec.CommandContext(ctx, bin, "exec", "--skip-git-repo-check", "--json", "-s", "read-only", "-m", "api-subagents/demo", "-C", root, "Reply OK without using tools.")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+r.Codex.Home)
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
	cmd = exec.CommandContext(ctx, bin, "exec", "resume", "--skip-git-repo-check", "--json", id, "Reply OK again without tools.")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+r.Codex.Home)
	cmd.Dir = root
	output, err = cmd.CombinedOutput()
	if err != nil || requests.Load() != 2 {
		t.Fatalf("continue after enabling: %v, requests=%d\n%s", err, requests.Load(), output)
	}
}

type codexRPC struct {
	encoder *json.Encoder
	decoder *json.Decoder
	id      int
}

// startCodexRPC 启动隔离的真实 app-server；子测试结束立即关闭，避免持有会话数据库或沿用旧配置。
func startCodexRPC(t *testing.T, bin, home, root string) *codexRPC {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	cmd := exec.CommandContext(ctx, bin, "app-server", "--listen", "stdio://")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
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
	t.Cleanup(func() { stdin.Close(); cancel(); _ = cmd.Wait() })
	client := &codexRPC{encoder: json.NewEncoder(stdin), decoder: json.NewDecoder(stdout)}
	client.call(t, "initialize", shared.Object{"clientInfo": shared.Object{"name": "api-subagents-tests", "version": "0.3.1"}})
	if err := client.encoder.Encode(shared.Object{"method": "initialized", "params": shared.Object{}}); err != nil {
		t.Fatal(err)
	}
	return client
}

// call 读取对应请求的响应并忽略通知；每次重新解码对象，避免前一条通知遗留 ID 或 result。
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
			continue
		}
		if value["error"] != nil {
			t.Fatal(method, value["error"])
		}
		return shared.Obj(value["result"])
	}
}
