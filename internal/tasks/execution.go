package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/U109/api-subagents/internal/codexworker"
	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// normalizeExecution 保持旧委派的只提建议语义，执行和联网能力必须通过显式参数开启。
func normalizeExecution(req *TaskRequest) error {
	if req.ExecutionMode == "" {
		req.ExecutionMode = "proposal"
	}
	if req.ExecutionMode != "proposal" && req.ExecutionMode != "codex" {
		return errors.New("execution_mode 必须为 proposal 或 codex。")
	}
	if req.Access == "" {
		req.Access = "read-only"
	}
	if req.Access != "read-only" && req.Access != "workspace-write" {
		return errors.New("access 必须为 read-only 或 workspace-write；不支持绕过沙箱。")
	}
	if req.ExecutionMode == "proposal" && (req.Access != "read-only" || req.NetworkAccess) {
		return errors.New("proposal 模式不能启用执行权限。")
	}
	if req.NetworkAccess && req.Access != "workspace-write" {
		return errors.New("network_access 需要显式选择 workspace-write。")
	}
	if req.ExecutionMode == "codex" && req.Continuation != "" {
		return errors.New("执行型任务暂不支持 continuation_id；请等待原任务结束，再基于实际工作区派发新的自洽任务。")
	}
	return nil
}

// writesWorkspace 标记会直接修改工作区的执行任务，用于阻止同一目录树内的并发读写串扰。
func writesWorkspace(req TaskRequest) bool {
	return req.ExecutionMode == "codex" && req.Access == "workspace-write"
}

// runExecution 把执行交给隔离 App Server，保存经过脱敏的工具证据，取消和超时不得转为迟到成功。
func (m *Manager) runExecution(j *job) {
	var result codexworker.Result
	var err error
	if m.Executor == nil {
		err = errors.New("执行型子代理后端未初始化。")
	} else {
		result, err = m.Executor.Run(j.ctx, codexworker.Request{Profile: j.profile, Task: j.request.Task, Workspace: j.ws.Root, Access: j.request.Access, NetworkAccess: j.request.NetworkAccess, HomeRoot: filepath.Join(filepath.Dir(m.Storage), "worker-homes"), MaxRequests: j.maxSteps, Progress: func(progress shared.Object) {
			m.mu.Lock()
			j.progress = progress
			m.mu.Unlock()
		}})
	}
	status := "completed"
	if err != nil {
		status = "failed"
	}
	if cause := context.Cause(j.ctx); cause != nil {
		err, status = cause, "cancelled"
		var op *shared.OpError
		if errors.As(cause, &op) && op.Code == "TASK_TIMEOUT" {
			status = "failed"
		}
	}
	var raw any
	_ = json.Unmarshal(shared.Marshal(result), &raw)
	_ = json.Unmarshal(shared.Marshal(configstore.RedactValue(raw, j.profile)), &result)
	m.mu.Lock()
	j.execution, j.steps, j.calls = &result, result.Requests, result.ToolCalls
	m.mu.Unlock()
	m.finish(j, status, result.Text, err)
	m.mu.Lock()
	m.running--
	m.pumpLocked()
	m.mu.Unlock()
}

// compactExecution 在保留退出码和变更提示的同时限制父模型上下文；完整证据可从任务记录读取。
func compactExecution(value shared.Object) shared.Object {
	result := shared.Object{}
	for _, key := range []string{"threadId", "turnId", "workspaceMayHaveChanges", "cleanupWarning", "evidenceTruncated"} {
		if value[key] != nil {
			result[key] = value[key]
		}
	}
	files := shared.Arr(value["changedFiles"])
	if len(files) > 30 {
		files = files[:30]
		result["evidenceTruncated"] = true
	}
	result["changedFiles"] = files
	commands := shared.Arr(value["commands"])
	if len(commands) > 8 {
		commands = commands[len(commands)-8:]
		result["evidenceTruncated"] = true
	}
	result["commands"] = commands
	return result
}
