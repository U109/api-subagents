package tasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/U109/api-subagents/internal/buildinfo"
	"github.com/U109/api-subagents/internal/shared"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverInstructions = "Save parent context: offload bounded work without duplicate investigations. Honor model and external-data restrictions. Prefer compact results and the 600000ms default wait; use wait_ms: 0 only for independent work. A running result is not failure: wait on the same task, never resubmit because a wait timed out. Proposal mode never writes: review and apply returned proposals. Codex mode runs commands and can directly edit files with access: workspace-write; use it only within the user's and parent's authorized permissions. Never enable network_access without authorization. Do not reapply execution-worker changes. Inspect actual diffs and command exit codes, including after cancellation or failure. generate_image makes a billable request and writes a new workspace file; use it only when the user explicitly asks to create an image."

// Input 声明 MCP 参数并指定必填项，其他字段保持可选且不接受未知参数。
func Input(properties shared.Object, required ...string) shared.Object {
	if required == nil {
		required = []string{}
	}
	return shared.Object{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

// MCPTools 保留委派工具并增加独立生图入口；图片模型不承担 Codex 主对话。
func MCPTools() []shared.Tool {
	str := shared.Object{"type": "string"}
	detail := shared.Object{"type": "string", "enum": []string{"compact", "full"}, "default": "compact"}
	wait := shared.Object{"type": "integer", "minimum": 0, "maximum": maxWaitMS, "default": defaultWaitMS}
	return []shared.Tool{
		{Name: "list_models", Description: "List workers and purposes without keys. Reuse during the task; honor user model restrictions.", Schema: Input(shared.Object{})},
		{Name: "generate_image", Description: "Generate one image using a configured OpenAI-compatible image model. Billable; call only when the user explicitly requests an image. Defaults to one 1024x1024 low-quality draft. Saves a new file under the workspace's api-subagents-images directory and returns its path and preview. Use list_models for the connection and selected image model.", Schema: Input(shared.Object{"model": shared.Object{"type": "string", "description": "Configured connection name from list_models"}, "model_id": shared.Object{"type": "string", "description": "Selected gpt-image-* model; optional when exactly one image model is selected on this connection"}, "prompt": shared.Object{"type": "string", "minLength": 1, "maxLength": 4000}, "size": shared.Object{"type": "string", "enum": []string{"1024x1024", "1536x1024", "1024x1536"}, "default": "1024x1024"}, "quality": shared.Object{"type": "string", "enum": []string{"low", "medium", "high", "xhigh", "max", "auto"}, "default": "low"}, "workspace": shared.Object{"type": "string", "description": "Absolute existing workspace directory; output path is fixed inside it"}}, "model", "prompt", "workspace")},
		{Name: "delegate_task", Description: "Delegate to any configured connection. Default proposal mode only proposes edits; codex mode uses an isolated Codex worker to run commands and, with explicit workspace-write access, edit files directly. Sends task/files to the selected provider. Waits up to 10 minutes; use wait_ms: 0 for independent work.", Schema: Input(shared.Object{"model": shared.Object{"type": "string", "description": "Configured connection name from list_models"}, "model_id": shared.Object{"type": "string", "description": "Optional model from this connection's availableModels; omitted uses its default"}, "execution_mode": shared.Object{"type": "string", "enum": []string{"proposal", "codex"}, "default": "proposal"}, "access": shared.Object{"type": "string", "enum": []string{"read-only", "workspace-write"}, "default": "read-only"}, "network_access": shared.Object{"type": "boolean", "default": false, "description": "Codex command network access, only with workspace-write and explicit user authorization; never bypass parent restrictions"}, "task": str, "workspace": shared.Object{"type": "string", "description": "Absolute project directory; direct workers share it, no automatic worktree or rollback"}, "continuation_id": shared.Object{"type": "string", "description": "Proposal mode only"}, "max_steps": shared.Object{"type": "integer", "minimum": 1, "maximum": 30, "default": 8, "description": "Proposal tool-loop rounds or codex model-request limit"}, "wait_ms": wait, "detail": detail}, "model", "task", "workspace")},
		{Name: "task_status", Description: "Read task results once when needed, not for polling active tasks; use wait_task instead. Use full only for omitted content. Redacted proposals cannot be applied verbatim.", Schema: Input(shared.Object{"task_id": str, "detail": detail}, "task_id")},
		{Name: "wait_task", Description: "Wait up to 10 minutes by default; returns immediately on completion. Prefer the default over short polling. Running means unfinished: keep the same task_id, do not resubmit.", Schema: Input(shared.Object{"task_id": str, "timeout_ms": wait, "detail": detail}, "task_id")},
		{Name: "cancel_task", Description: "Cancel a queued or running worker. Codex workers may have already modified files: cancellation does not roll back changes or guarantee upstream billing stops.", Schema: Input(shared.Object{"task_id": str}, "task_id")},
		{Name: "configuration_help", Description: "Show where settings are stored and how to open configuration.", Schema: Input(shared.Object{})},
	}
}

// NewMCPServer 使用官方 Go MCP SDK 处理 stdio、协议协商及并发请求。
func NewMCPServer(manager *Manager) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "api-subagents", Version: buildinfo.Version}, &mcp.ServerOptions{Instructions: serverInstructions})
	for _, tool := range MCPTools() {
		name := tool.Name
		readOnly := name != "delegate_task" && name != "cancel_task" && name != "generate_image"
		destructive, openWorld := name == "delegate_task" || name == "generate_image", name == "delegate_task" || name == "generate_image"
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
			content := []mcp.Content{&mcp.TextContent{Text: string(shared.Marshal(value))}}
			if name == "generate_image" {
				path := shared.Str(shared.Obj(value)["path"])
				if data, readErr := imagePreview(path); readErr == nil {
					content = append(content, &mcp.ImageContent{MIMEType: shared.Str(shared.Obj(value)["mimeType"]), Data: []byte(base64.StdEncoding.EncodeToString(data))})
				}
			}
			return &mcp.CallToolResult{Content: content}, nil
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
	case "generate_image":
		var req ImageRequest
		if json.Unmarshal(shared.Marshal(args), &req) != nil {
			return nil, errors.New("生图参数类型无效。")
		}
		return m.GenerateImage(ctx, req)
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
