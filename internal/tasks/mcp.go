package tasks

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/U109/api-subagents/internal/buildinfo"
	"github.com/U109/api-subagents/internal/shared"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverInstructions = "Save parent context: offload bounded routine work when it replaces substantial parent reading or editing. Avoid duplicate investigations and needless delegation of trivial tasks. Honor model restrictions. Prefer compact results. Wait with the 600000ms default unless independent work is available; then delegate with wait_ms: 0 and do that work first. A running result is not failure: wait on the same task, never resubmit or restart it merely because a wait timed out. Avoid short status polling and unchanged progress narration. Review affected changes and run focused checks once; apply reviewed proposals via the returned local command. Workers never apply changes."

// Input 声明 MCP 参数并指定必填项，其他字段保持可选且不接受未知参数。
func Input(properties shared.Object, required ...string) shared.Object {
	if required == nil {
		required = []string{}
	}
	return shared.Object{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

// MCPTools 延续旧版六个工具名称及精简 schema，避免增加父模型调度上下文。
func MCPTools() []shared.Tool {
	str := shared.Object{"type": "string"}
	detail := shared.Object{"type": "string", "enum": []string{"compact", "full"}, "default": "compact"}
	wait := shared.Object{"type": "integer", "minimum": 0, "maximum": maxWaitMS, "default": defaultWaitMS}
	return []shared.Tool{
		{Name: "list_models", Description: "List workers and purposes without keys. Reuse during the task; honor user model restrictions.", Schema: Input(shared.Object{})},
		{Name: "delegate_task", Description: "Offload a bounded routine task to a configured API worker. Defaults to waiting up to 10 minutes, returning early on completion. Use wait_ms: 0 when independent work is available. Sends task/files to its provider; parent reviews and applies proposals.", Schema: Input(shared.Object{"model": str, "task": str, "workspace": shared.Object{"type": "string", "description": "Absolute project directory"}, "continuation_id": str, "max_steps": shared.Object{"type": "integer", "minimum": 1, "maximum": 30, "default": 8}, "wait_ms": wait, "detail": detail}, "model", "task", "workspace")},
		{Name: "task_status", Description: "Read task results once when needed, not for polling active tasks; use wait_task instead. Use full only for omitted content. Redacted proposals cannot be applied verbatim.", Schema: Input(shared.Object{"task_id": str, "detail": detail}, "task_id")},
		{Name: "wait_task", Description: "Wait up to 10 minutes by default; returns immediately on completion. Prefer the default over short polling. Running means unfinished: keep the same task_id, do not resubmit.", Schema: Input(shared.Object{"task_id": str, "timeout_ms": wait, "detail": detail}, "task_id")},
		{Name: "cancel_task", Description: "Cancel a queued or running worker task.", Schema: Input(shared.Object{"task_id": str}, "task_id")},
		{Name: "configuration_help", Description: "Show where settings are stored and how to open configuration.", Schema: Input(shared.Object{})},
	}
}

// NewMCPServer 使用官方 Go MCP SDK 处理 stdio、协议协商及并发请求。
func NewMCPServer(manager *Manager) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "api-subagents", Version: buildinfo.Version}, &mcp.ServerOptions{Instructions: serverInstructions})
	for _, tool := range MCPTools() {
		name := tool.Name
		readOnly := name != "delegate_task" && name != "cancel_task"
		destructive, openWorld := false, name == "delegate_task"
		server.AddTool(&mcp.Tool{Name: name, Description: tool.Description, InputSchema: tool.Schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, OpenWorldHint: &openWorld}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args shared.Object
			if len(req.Params.Arguments) == 0 {
				args = shared.Object{}
			} else if json.Unmarshal(req.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "参数 JSON 无效。"}}}, nil
			}
			value, err := manager.Invoke(ctx, name, args)
			if err != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(shared.Marshal(value))}}}, nil
		})
	}
	return server
}

// integerArg 校验 MCP 数字为整数，并区分缺省值与显式的零。
func integerArg(args shared.Object, key string, fallback, min, max int) (int, error) {
	value, ok := args[key]
	if !ok {
		return fallback, nil
	}
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) || number < float64(min) || number > float64(max) {
		return 0, errors.New(key + " 超出允许的整数范围。")
	}
	return int(number), nil
}

// Invoke 将 MCP 请求映射到任务管理器，默认等待一次并返回精简结果。
func (m *Manager) Invoke(ctx context.Context, name string, args shared.Object) (any, error) {
	detail := shared.Str(args["detail"])
	if detail != "" && detail != "compact" && detail != "full" {
		return nil, errors.New("detail 必须为 compact 或 full。")
	}
	var value shared.Object
	var err error
	switch name {
	case "list_models":
		models, e := m.ListModels()
		return shared.Object{"models": models}, e
	case "configuration_help":
		return shared.Object{"configFile": m.Store.Path, "instructions": "打开 API Subagents 桌面 App 或双击 Configure.cmd。保存连接后对新任务生效。测试连接会发送一条简短 API 请求。"}, nil
	case "delegate_task":
		wait, e := integerArg(args, "wait_ms", defaultWaitMS, 0, maxWaitMS)
		if e != nil {
			return nil, e
		}
		steps, e := integerArg(args, "max_steps", 8, 1, 30)
		if e != nil {
			return nil, e
		}
		var req TaskRequest
		if json.Unmarshal(shared.Marshal(args), &req) != nil {
			return nil, errors.New("任务参数类型无效。")
		}
		req.MaxSteps = steps
		value, err = m.Submit(req)
		if err == nil {
			value, err = m.Wait(ctx, shared.Str(value["task_id"]), wait)
		}
	case "task_status":
		value, err = m.Get(shared.Str(args["task_id"]))
	case "wait_task":
		wait, e := integerArg(args, "timeout_ms", defaultWaitMS, 0, maxWaitMS)
		if e != nil {
			return nil, e
		}
		value, err = m.Wait(ctx, shared.Str(args["task_id"]), wait)
	case "cancel_task":
		value, err = m.Cancel(shared.Str(args["task_id"]))
	default:
		return nil, errors.New("未知工具。")
	}
	if err != nil {
		return nil, err
	}
	return m.Present(value, detail)
}
