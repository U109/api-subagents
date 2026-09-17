package cpacompat

import (
	"github.com/U109/api-subagents/internal/shared"
	"github.com/tidwall/gjson"
)

type toolIdentity struct{ name, namespace string }

// ToolAliases 只保存当前请求的名称映射，不共享状态，也不包含工具参数或任何连接凭据。
type ToolAliases map[string]toolIdentity

// PrepareChatTools 将 CPA 的长名称兼容算法接到本地请求；仅有超长名称时改写声明、历史与指定工具。
// 返回映射用于流式及普通响应恢复原身份，工具参数、正文与工具结果始终保持原值。
func PrepareChatTools(input shared.Object) ToolAliases {
	original := shared.Marshal(input)
	root := gjson.ParseBytes(original)
	aliases := ToolAliases{}
	long := false
	walkResponsesToolDeclarations(root, func(d responsesToolDeclaration) bool {
		long = long || len(rawResponsesNamespaceQualifiedName(d.namespace, d.localName)) > responsesChatToolNameLimit
		if _, exists := aliases[d.chatName]; !exists {
			aliases[d.chatName] = toolIdentity{name: d.localName, namespace: d.namespace}
		}
		return true
	})
	if !long {
		return nil
	}
	if tools, ok := input["tools"].([]any); ok {
		input["tools"] = flattenChatTools(tools, original)
	}
	for _, raw := range shared.Arr(input["input"]) {
		item := shared.Obj(raw)
		switch item["type"] {
		case "additional_tools":
			item["tools"] = flattenChatTools(shared.Arr(item["tools"]), original)
		case "function_call", "custom_tool_call":
			rewriteChatToolIdentity(item, original)
		}
	}
	choice := shared.Obj(input["tool_choice"])
	if choice["type"] == "function" || choice["type"] == "custom" {
		rewriteChatToolIdentity(choice, original)
	}
	if allowed, ok := choice["tools"].([]any); ok {
		for _, raw := range allowed {
			rewriteChatToolIdentity(shared.Obj(raw), original)
		}
	}
	return aliases
}

// flattenChatTools 在请求副本中展开命名空间并设置已计算的别名，非函数类工具原样保留。
func flattenChatTools(tools []any, original []byte) []any {
	result := []any{}
	for _, raw := range tools {
		tool := shared.Obj(raw)
		if tool["type"] == "namespace" {
			for _, child := range shared.Arr(tool["tools"]) {
				item := shared.Obj(child)
				if item["type"] == "function" || item["type"] == "custom" || item["type"] == nil {
					item["name"] = chatNameForResponsesNamespaceToolCall(original, shared.Str(tool["name"]), shared.Str(item["name"]))
				}
				result = append(result, item)
			}
		} else {
			if tool["type"] == "function" || tool["type"] == "custom" || tool["type"] == nil {
				rewriteChatToolIdentity(tool, original)
			}
			result = append(result, tool)
		}
	}
	return result
}

// rewriteChatToolIdentity 将结构化工具身份映射为 CPA 别名；兼容嵌套的 tool_choice.function/custom 写法。
func rewriteChatToolIdentity(item shared.Object, original []byte) {
	if item == nil {
		return
	}
	if shared.Str(item["name"]) == "" {
		for _, key := range []string{"function", "custom"} {
			if nested, ok := item[key].(map[string]any); ok {
				rewriteChatToolIdentity(nested, original)
			}
		}
		return
	}
	if namespace := shared.Str(item["namespace"]); namespace != "" {
		item["name"] = chatNameForResponsesNamespaceToolCall(original, namespace, shared.Str(item["name"]))
	} else {
		item["name"] = canonicalResponsesToolName(original, shared.Str(item["name"]))
	}
	delete(item, "namespace")
}

// Restore 仅恢复 Responses 调用项的名称与命名空间，不遍历字符串中的 JSON、正文或参数内容。
func (a ToolAliases) Restore(value any) {
	if len(a) == 0 {
		return
	}
	switch item := value.(type) {
	case map[string]any:
		if item["type"] == "function_call" || item["type"] == "custom_tool_call" {
			if identity, ok := a[shared.Str(item["name"])]; ok {
				item["name"] = identity.name
				if identity.namespace != "" {
					item["namespace"] = identity.namespace
				} else {
					delete(item, "namespace")
				}
			}
			return
		}
		for _, key := range []string{"item", "response", "output"} {
			if child, ok := item[key]; ok {
				a.Restore(child)
			}
		}
	case []any:
		for _, child := range item {
			a.Restore(child)
		}
	}
}
