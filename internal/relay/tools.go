package relay

import (
	"strconv"

	"github.com/U109/api-subagents/internal/shared"
)

// aliasPatchTool 规避 CPA Claude 转换器对 apply_patch 的名称过滤，保留 Codex 已提供的工具能力。
// 仅更改声明和调用项的名称，不修改补丁正文；候选别名与已有工具重名时自动换名。
func aliasPatchTool(input shared.Object) string {
	definitions := []shared.Object{}
	var collect func([]any)
	collect = func(tools []any) {
		for _, raw := range tools {
			tool := shared.Obj(raw)
			definitions = append(definitions, tool)
			if tool["type"] == "namespace" {
				collect(shared.Arr(tool["tools"]))
			}
		}
	}
	collect(shared.Arr(input["tools"]))
	for _, raw := range shared.Arr(input["input"]) {
		item := shared.Obj(raw)
		if item["type"] == "additional_tools" {
			collect(shared.Arr(item["tools"]))
		}
	}
	names := map[string]bool{}
	patch := false
	for _, tool := range definitions {
		names[shared.Str(tool["name"])] = true
		patch = patch || tool["type"] == "custom" && tool["name"] == "apply_patch"
	}
	if !patch {
		return ""
	}
	alias := "api_subagents_patch"
	for index := 1; names[alias]; index++ {
		alias = "api_subagents_patch_" + strconv.Itoa(index)
	}
	for _, tool := range definitions {
		if tool["type"] == "custom" && tool["name"] == "apply_patch" {
			tool["name"] = alias
		}
	}
	for _, raw := range shared.Arr(input["input"]) {
		item := shared.Obj(raw)
		if item["type"] == "custom_tool_call" && item["name"] == "apply_patch" {
			item["name"] = alias
		}
	}
	choice := shared.Obj(input["tool_choice"])
	if choice["name"] == "apply_patch" && choice["type"] == "custom" {
		choice["name"] = alias
	}
	return alias
}

// restorePatchTool 只恢复结构化的自定义工具调用名，回答和补丁字符串中的同名文字保持原样。
func restorePatchTool(value any, alias string) {
	if alias == "" {
		return
	}
	switch item := value.(type) {
	case map[string]any:
		if item["type"] == "custom_tool_call" && item["name"] == alias {
			item["name"] = "apply_patch"
		}
		for _, v := range item {
			restorePatchTool(v, alias)
		}
	case []any:
		for _, v := range item {
			restorePatchTool(v, alias)
		}
	}
}
