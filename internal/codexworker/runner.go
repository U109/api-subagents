package codexworker

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/U109/api-subagents/internal/buildinfo"
	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/relay"
	"github.com/U109/api-subagents/internal/shared"
)

type Request struct {
	Profile                           configstore.Profile
	Task, Workspace, Access, HomeRoot string
	NetworkAccess                     bool
	MaxRequests                       int
	Progress                          func(shared.Object)
}

type Result struct {
	Text                    string          `json:"text"`
	ThreadID                string          `json:"threadId"`
	TurnID                  string          `json:"turnId"`
	Requests                int             `json:"requests"`
	ToolCalls               int             `json:"toolCalls"`
	WorkspaceMayHaveChanges bool            `json:"workspaceMayHaveChanges"`
	ChangedFiles            []string        `json:"changedFiles"`
	Commands                []shared.Object `json:"commands"`
	EvidenceTruncated       bool            `json:"evidenceTruncated,omitempty"`
	CleanupWarning          string          `json:"cleanupWarning,omitempty"`
}

type Executor interface {
	// Run 在明确的目录和权限内执行任务，返回执行证据或失败原因，不允许静默切换模型。
	Run(context.Context, Request) (Result, error)
}

type Runner struct {
	Binary  string
	Client  *http.Client
	command func(string, ...string) *exec.Cmd
}

// Run 为一次任务创建独立 home、单模型网关和 App Server，始终收拢进程后再返回；不会重试或切换供应商。
func (r *Runner) Run(ctx context.Context, req Request) (result Result, failure error) {
	if req.Access != "read-only" && req.Access != "workspace-write" {
		return result, errors.New("无效的执行权限。")
	}
	if req.NetworkAccess && req.Access != "workspace-write" {
		return result, errors.New("网络访问必须显式选择 workspace-write。")
	}
	if err := ctx.Err(); err != nil {
		return result, context.Cause(ctx)
	}
	binary, err := findBinary(r.Binary)
	if err != nil {
		return result, err
	}
	root, err := filepath.EvalSymlinks(req.Workspace)
	if err != nil || !filepath.IsAbs(root) {
		return result, errors.New("执行工作区必须是存在的绝对目录。")
	}
	req.Workspace = root
	if !filepath.IsAbs(req.HomeRoot) {
		return result, errors.New("执行状态目录必须为绝对路径。")
	}
	if err = os.MkdirAll(req.HomeRoot, 0700); err != nil {
		return result, err
	}
	parent, err := filepath.EvalSymlinks(req.HomeRoot)
	if err != nil || shared.Inside(root, parent) {
		return result, errors.New("执行状态目录不能放在子代理工作区内，请调整 API_SUBAGENTS_HOME 或工作区。")
	}
	home, err := os.MkdirTemp(parent, "worker-")
	if err != nil {
		return result, err
	}
	defer func() {
		if removeHome(home, parent) != nil {
			result.CleanupWarning = "任务临时 CODEX_HOME 未完全清理；不含真实供应商 Key，后续请检查本地 worker-homes。"
		}
	}()
	gateway, err := relay.StartWorkerGateway(ctx, req.Profile, req.MaxRequests, r.Client)
	if err != nil {
		return result, err
	}
	defer func() {
		var exceeded bool
		result.Requests, exceeded = gateway.Usage()
		gateway.Close()
		if exceeded {
			failure = &shared.OpError{Code: "TASK_STEP_LIMIT", Message: "执行型子代理达到模型请求上限，已停止；工作区可能保留部分修改。"}
		}
	}()
	config, err := prepareConfig(req, home, gateway.Address, gateway.Token)
	if err != nil {
		return result, err
	}
	state := executionState{req: req, result: &result, seen: map[string]bool{}}
	command := r.command
	if command == nil {
		command = exec.Command
	}
	cmd := command(binary, "app-server", "--listen", "stdio://")
	cmd.Dir, cmd.Env = home, workerEnvironment(home)
	client, err := startRPC(cmd, state.event)
	if err != nil {
		return result, err
	}
	defer func() { client.close(result.ThreadID, result.TurnID) }()
	// stdin 写阻塞也受任务取消约束，不能只依赖读取循环检查 context。
	stopCancel := context.AfterFunc(ctx, func() { client.kill(); _ = client.input.Close() })
	defer stopCancel()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	startupStop := context.AfterFunc(startup, func() { client.kill(); _ = client.input.Close() })
	defer startupStop()
	if req.Progress != nil {
		req.Progress(shared.Object{"phase": "starting_codex"})
	}
	if _, err = client.call(startup, "initialize", shared.Object{"clientInfo": shared.Object{"name": "api-subagents-worker", "version": buildinfo.Version}}); err != nil {
		return result, err
	}
	if err = client.send(shared.Object{"method": "initialized", "params": shared.Object{}}); err != nil {
		return result, err
	}
	thread, err := client.call(startup, "thread/start", shared.Object{"model": config["model"], "modelProvider": "api_subagents_worker", "cwd": root, "approvalPolicy": "never", "sandbox": req.Access, "ephemeral": true, "developerInstructions": instructions, "config": config})
	if err != nil {
		return result, err
	}
	result.ThreadID = shared.Str(shared.Obj(thread["thread"])["id"])
	if err = verifyThread(thread, req, home); err != nil {
		return result, err
	}
	startupStop()
	cancel()
	// 一旦开始派发，即使启动响应丢失也不能声称工作区没有变化。
	result.WorkspaceMayHaveChanges = req.Access == "workspace-write"
	turn, err := client.call(ctx, "turn/start", shared.Object{"threadId": result.ThreadID, "model": config["model"], "cwd": root, "approvalPolicy": "never", "sandboxPolicy": sandboxPolicy(req, home), "input": []any{shared.Object{"type": "text", "text": req.Task, "text_elements": []any{}}}})
	if err != nil {
		return result, err
	}
	id := shared.Str(shared.Obj(turn["turn"])["id"])
	if id == "" || (result.TurnID != "" && result.TurnID != id) {
		return result, errors.New("Codex worker 返回了无效的 turn ID。")
	}
	result.TurnID = id
	for !state.completed {
		value, err := client.receive(ctx)
		if err != nil {
			return result, err
		}
		if err = client.dispatch(value); err != nil {
			return result, err
		}
	}
	if state.status != "completed" {
		return result, errors.New("Codex 执行未完成（" + state.status + "）：" + state.message)
	}
	if result.Text == "" {
		return result, errors.New("Codex worker 完成但没有返回最终报告。")
	}
	return result, nil
}

type executionState struct {
	req             Request
	result          *Result
	seen            map[string]bool
	completed       bool
	status, message string
}

// event 收集原生完成项作为执行证据，不把命令输出或补丁正文无限返回给父模型；不同线程通知不能串入结果。
func (s *executionState) event(method string, params shared.Object) error {
	if s.result.ThreadID == "" || shared.Str(params["threadId"]) != s.result.ThreadID {
		return nil
	}
	turnID := shared.Str(params["turnId"])
	if method == "turn/started" || method == "turn/completed" {
		turnID = shared.Str(shared.Obj(params["turn"])["id"])
	}
	if turnID != "" {
		if s.result.TurnID == "" {
			s.result.TurnID = turnID
		}
		if s.result.TurnID != turnID {
			return errors.New("Codex worker 收到不属于当前任务的 turn。")
		}
	}
	if s.req.Progress != nil {
		s.req.Progress(shared.Object{"phase": "executing_codex", "threadId": s.result.ThreadID, "turnId": s.result.TurnID})
	}
	if method == "turn/completed" {
		turn := shared.Obj(params["turn"])
		s.completed, s.status = true, shared.Str(turn["status"])
		s.message = shared.Clip(shared.Str(shared.Obj(turn["error"])["message"]), 2000)
	}
	if method != "item/completed" {
		return nil
	}
	item := shared.Obj(params["item"])
	id := shared.Str(item["id"])
	if id == "" || s.seen[id] {
		return nil
	}
	if len(s.seen) >= 2000 {
		return errors.New("执行事件数量超过任务上限。")
	}
	s.seen[id] = true
	switch item["type"] {
	case "agentMessage":
		if item["phase"] != "commentary" {
			s.result.Text = shared.Clip(shared.Str(item["text"]), 32000)
		}
	case "commandExecution":
		s.result.ToolCalls++
		if len(s.result.Commands) < 100 {
			s.result.Commands = append(s.result.Commands, shared.Object{"command": shared.Clip(shared.Str(item["command"]), 1000), "cwd": item["cwd"], "status": item["status"], "exitCode": item["exitCode"]})
		} else {
			s.result.EvidenceTruncated = true
		}
	case "fileChange":
		s.result.ToolCalls++
		if item["status"] != "completed" {
			return nil
		}
		for _, raw := range shared.Arr(item["changes"]) {
			path := shared.Str(shared.Obj(raw)["path"])
			if path != "" && !shared.Contains(s.result.ChangedFiles, path) {
				if len(s.result.ChangedFiles) < 200 {
					s.result.ChangedFiles = append(s.result.ChangedFiles, path)
				} else {
					s.result.EvidenceTruncated = true
				}
			}
		}
	}
	return nil
}
