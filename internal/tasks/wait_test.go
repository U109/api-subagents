package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// waitFixture 用可手动释放的本地模拟接口创建任务管理器，取消请求时也能安全退出。
func waitFixture(t *testing.T) (*Manager, func()) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-release:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(modelReply("compatible", nil, "done"))
		case <-r.Context().Done():
		}
	}))
	store, _ := testutil.Config(t, "compatible", server.URL)
	manager := NewManager(store, t.TempDir(), nil)
	t.Cleanup(func() {
		finish()
		manager.Close()
		server.Close()
	})
	return manager, finish
}

// TestWaitLifecycle 验证短超时和调用方取消均保留原任务，长等待在结果落盘后立即返回。
func TestWaitLifecycle(t *testing.T) {
	m, finish := waitFixture(t)
	value, err := m.Invoke(context.Background(), "delegate_task", shared.Object{
		"model": "demo", "task": "wait test", "workspace": t.TempDir(), "wait_ms": float64(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := shared.Str(shared.Obj(value)["task_id"])
	for _, timeout := range []int{0, 20} {
		state, err := m.Wait(context.Background(), id, timeout)
		if err != nil || (state["status"] != "queued" && state["status"] != "running") {
			t.Fatal("timeout changed task state", state, err)
		}
		compact, err := m.Present(state, "")
		if err != nil || compact["nextWaitMs"] != defaultWaitMS {
			t.Fatal("missing long-wait hint", compact, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := m.Invoke(ctx, "wait_task", shared.Object{"task_id": id}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("default wait did not respect caller deadline", err)
	}
	state, err := m.Get(id)
	if err != nil || (state["status"] != "queued" && state["status"] != "running") {
		t.Fatal("cancelled wait cancelled background task", state, err)
	}
	// 模拟稍后完成；一分钟以内的保护上下文确保错误实现不会卡住整个测试。
	timer := time.AfterFunc(30*time.Millisecond, finish)
	defer timer.Stop()
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	state, err = m.Wait(ctx, id, maxWaitMS)
	if err != nil || state["status"] != "completed" {
		t.Fatal("long wait failed to return on completion", state, err)
	}
	if _, err := os.Stat(m.Storage + "/" + id + ".json"); err != nil {
		t.Fatal("wait returned before result was saved", err)
	}
	compact, err := m.Present(state, "")
	if err != nil || compact["nextWaitMs"] != nil {
		t.Fatal("completed task still advertises waiting", compact, err)
	}
}

// TestWaitCancellationAndBounds 验证长等待可被任务取消唤醒，并拒绝越界参数且不创建新任务。
func TestWaitCancellationAndBounds(t *testing.T) {
	m, _ := waitFixture(t)
	for _, timeout := range []float64{-1, maxWaitMS + 1, 1.5} {
		for name, arg := range map[string]string{"delegate_task": "wait_ms", "wait_task": "timeout_ms"} {
			if _, err := m.Invoke(context.Background(), name, shared.Object{arg: timeout}); err == nil {
				t.Fatalf("%s accepted invalid timeout %v", name, timeout)
			}
		}
	}
	for _, timeout := range []int{-1, maxWaitMS + 1} {
		if _, err := m.Wait(context.Background(), "invalid", timeout); err == nil {
			t.Fatal("direct wait accepted invalid timeout", timeout)
		}
	}
	if len(m.jobs) != 0 {
		t.Fatal("invalid delegate created a task")
	}
	value, err := m.Submit(TaskRequest{Model: "demo", Task: "cancel test", Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id := shared.Str(value["task_id"])
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Wait(ctx, id, 0); !errors.Is(err, context.Canceled) {
		t.Fatal("already cancelled context was ignored", err)
	}
	timer := time.AfterFunc(30*time.Millisecond, func() { _, _ = m.Cancel(id) })
	defer timer.Stop()
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	value, err = m.Wait(ctx, id, maxWaitMS)
	if err != nil || value["status"] != "cancelled" {
		t.Fatal("task cancellation did not wake long wait", value, err)
	}
}

// TestWaitSchemaAndHostBudget 防止工具 schema、默认参数和发布配置的宿主超时发生漂移。
func TestWaitSchemaAndHostBudget(t *testing.T) {
	for _, tool := range MCPTools() {
		arg := map[string]string{"delegate_task": "wait_ms", "wait_task": "timeout_ms"}[tool.Name]
		if arg == "" {
			continue
		}
		spec := shared.Obj(shared.Obj(tool.Schema["properties"])[arg])
		if spec["default"] != defaultWaitMS || spec["maximum"] != maxWaitMS || spec["minimum"] != 0 {
			t.Fatal("wait schema differs from runtime", tool.Name, spec)
		}
		fallback, err := integerArg(shared.Object{}, arg, defaultWaitMS, 0, maxWaitMS)
		if err != nil || fallback != defaultWaitMS {
			t.Fatal("wrong omitted-argument default", fallback, err)
		}
	}
	data, err := os.ReadFile("../../.mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	var config shared.Object
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	server := shared.Obj(shared.Obj(config["mcpServers"])["api-subagents"])
	if shared.Int(server["tool_timeout_sec"])*1000 < maxWaitMS+60000 {
		t.Fatal("host timeout must leave at least one minute after maximum wait")
	}
}

// TestMCPLongWait 跨过旧的 25 秒窗口，验证一次 MCP 委派即可取得结果，无需父模型中途再次调用。
func TestMCPLongWait(t *testing.T) {
	if testing.Short() {
		t.Skip("long-wait transport regression takes 26 seconds")
	}
	m, finish := waitFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := NewMCPServer(m).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "wait-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	timer := time.AfterFunc(26*time.Second, finish)
	defer timer.Stop()
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "delegate_task", Arguments: shared.Object{
		"model": "demo", "task": "single long wait", "workspace": t.TempDir(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatal("MCP delegation failed", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("unexpected MCP content", result.Content)
	}
	var state shared.Object
	if err := json.Unmarshal([]byte(text.Text), &state); err != nil || state["status"] != "completed" {
		t.Fatal("MCP returned before completion", text.Text, err)
	}
}
