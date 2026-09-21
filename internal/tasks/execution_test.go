package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/codexworker"
	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
	workfiles "github.com/U109/api-subagents/internal/workspace"
)

type fakeExecutor func(context.Context, codexworker.Request) (codexworker.Result, error)

// Run 让任务管理测试注入确定性执行器，不启动真实模型或用户的 Codex。
func (f fakeExecutor) Run(ctx context.Context, req codexworker.Request) (codexworker.Result, error) {
	return f(ctx, req)
}

// TestExecutionModelsAndResults 验证任意已配置模型均可选、输出脱敏、直接写入标记与禁止二次应用。
func TestExecutionModelsAndResults(t *testing.T) {
	store, _ := testutil.Config(t, "compatible", "http://127.0.0.1:1")
	c, _ := store.Read()
	p := c.Models["demo"]
	p.RelayModels = []string{"vendor-x/custom-model", "another-model"}
	c.Models["demo"] = p
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	m := NewManager(store, filepath.Join(t.TempDir(), "tasks"), nil)
	defer m.Close()
	models, err := m.ListModels()
	if err != nil || len(shared.Obj(models[0])["availableModels"].([]string)) != 3 {
		t.Fatal(models, err)
	}
	root := t.TempDir()
	m.Executor = fakeExecutor(func(_ context.Context, req codexworker.Request) (codexworker.Result, error) {
		if req.Profile.Model != "vendor-x/custom-model" || req.Profile.Protocol != "compatible" || req.Access != "workspace-write" || req.NetworkAccess {
			t.Error("selection or boundary lost", req.Profile.Model, req.Access)
		}
		if err := os.WriteFile(filepath.Join(req.Workspace, "result.txt"), []byte("direct change"), 0600); err != nil {
			return codexworker.Result{}, err
		}
		return codexworker.Result{Text: "done " + req.Profile.APIKey, ThreadID: "thread", Requests: 2, ToolCalls: 1, WorkspaceMayHaveChanges: true, ChangedFiles: []string{"result.txt"}, Commands: []shared.Object{{"command": "synthetic-test " + req.Profile.APIKey, "exitCode": 0}}}, nil
	})
	request := TaskRequest{Model: "demo", ModelID: "vendor-x/custom-model", ExecutionMode: "codex", Access: "workspace-write", Workspace: root, Task: "bounded task"}
	submitted, err := m.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	id := shared.Str(submitted["task_id"])
	value, err := m.Wait(context.Background(), id, 5000)
	if err != nil || value["status"] != "completed" || value["model_id"] != request.ModelID || value["changeDelivery"] != "workspace" {
		t.Fatal(value, err)
	}
	compact, _ := m.Present(value, "compact")
	if compact["applyCommand"] != nil || compact["changesApplied"] != nil || shared.Obj(compact["execution"])["workspaceMayHaveChanges"] != true || strings.Contains(string(shared.Marshal(value)), p.APIKey) {
		t.Fatal("wrong direct result", compact)
	}
	if _, err = workfiles.ApplyProposals(id, root, nil, m.Storage); err == nil {
		t.Fatal("execution result accepted as a proposal")
	}
	request.Continuation = id
	if _, err = m.Submit(request); err == nil {
		t.Fatal("execution continuation silently accepted")
	}
	request.ExecutionMode, request.Access = "proposal", "read-only"
	if _, err = m.Submit(request); err == nil {
		t.Fatal("execution continued through proposal backend")
	}
	request.Continuation, request.ModelID = "", "unconfigured-model"
	if _, err = m.Submit(request); err == nil {
		t.Fatal("unconfigured model accepted")
	}
	after, _ := store.Read()
	if after.Models["demo"].Model != p.Model {
		t.Fatal("task changed connection default")
	}
}

// TestExecutionConcurrencyCancellation 禁止同一目录树并行写入，取消后等待执行器回收且保留部分修改提示。
func TestExecutionConcurrencyCancellation(t *testing.T) {
	store, _ := testutil.Config(t, "responses", "http://127.0.0.1:1")
	m := NewManager(store, filepath.Join(t.TempDir(), "tasks"), nil)
	defer m.Close()
	started := make(chan struct{})
	m.Executor = fakeExecutor(func(ctx context.Context, req codexworker.Request) (codexworker.Result, error) {
		close(started)
		<-ctx.Done()
		return codexworker.Result{Text: "partial", WorkspaceMayHaveChanges: true}, context.Cause(ctx)
	})
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	request := TaskRequest{Model: "demo", Task: "edit", Workspace: root, ExecutionMode: "codex", Access: "workspace-write"}
	submitted, err := m.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	for _, path := range []string{root, child} {
		if _, err := m.Submit(TaskRequest{Model: "demo", Task: "read", Workspace: path}); err == nil {
			t.Fatal("overlapping active task accepted")
		}
	}
	id := shared.Str(submitted["task_id"])
	if _, err := m.Cancel(id); err != nil {
		t.Fatal(err)
	}
	value, err := m.Wait(context.Background(), id, 5000)
	if err != nil || value["status"] != "cancelled" || shared.Obj(value["execution"])["workspaceMayHaveChanges"] != true {
		t.Fatal(value, err)
	}
	if raw, err := os.ReadFile(filepath.Join(m.Storage, id+".json")); err != nil || !strings.Contains(string(raw), "partial") {
		t.Fatal("partial result not persisted", err)
	}
}

// TestExecutionValidationAndTimeout 检查模式、网络权限及总超时，不接受危险权限或迟到成功。
func TestExecutionValidationAndTimeout(t *testing.T) {
	for _, req := range []TaskRequest{
		{ExecutionMode: "unknown"}, {Access: "workspace-write"}, {ExecutionMode: "codex", Access: "danger-full-access"},
		{ExecutionMode: "codex", NetworkAccess: true}, {ExecutionMode: "codex", Continuation: "old-task"},
	} {
		if normalizeExecution(&req) == nil {
			t.Fatal("invalid mode accepted", req)
		}
	}
	store, _ := testutil.Config(t, "gemini", "http://127.0.0.1:1")
	m := NewManager(store, filepath.Join(t.TempDir(), "tasks"), nil)
	defer m.Close()
	m.Deadline = 20 * time.Millisecond
	m.Executor = fakeExecutor(func(ctx context.Context, _ codexworker.Request) (codexworker.Result, error) {
		<-ctx.Done()
		return codexworker.Result{Text: "late success"}, nil
	})
	submitted, err := m.Submit(TaskRequest{Model: "demo", Task: "task", Workspace: t.TempDir(), ExecutionMode: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := m.Wait(context.Background(), shared.Str(submitted["task_id"]), 5000)
	if err != nil || value["status"] != "failed" || value["errorCode"] != "TASK_TIMEOUT" {
		t.Fatal(value, err)
	}
	m.Executor = fakeExecutor(func(_ context.Context, _ codexworker.Request) (codexworker.Result, error) {
		return codexworker.Result{}, errors.New("no fallback")
	})
}
