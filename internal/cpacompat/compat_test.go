package cpacompat

import (
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestChatAliasesPreserveBoundaries 验证追加工具、自由文本工具和指定工具共用映射，正文和实际参数不会被名称替换污染。
func TestChatAliasesPreserveBoundaries(t *testing.T) {
	namespace := "plugin_" + strings.Repeat("long_namespace_", 6)
	args := `{"name":"unchanged","namespace":"data"}`
	input := shared.Object{"tools": []any{shared.Object{"type": "namespace", "name": namespace, "tools": []any{
		shared.Object{"type": "custom", "name": "apply_change", "format": shared.Object{"type": "text"}},
	}}}, "input": []any{
		shared.Object{"type": "additional_tools", "tools": []any{shared.Object{"type": "function", "name": "read_file"}}},
		shared.Object{"type": "custom_tool_call", "name": "apply_change", "namespace": namespace, "input": args},
		shared.Object{"type": "function_call_output", "output": args},
	}, "tool_choice": shared.Object{"type": "custom", "custom": shared.Object{"name": "apply_change", "namespace": namespace}}}
	aliases := PrepareChatTools(input)
	name := shared.Str(shared.Obj(shared.Arr(input["tools"])[0])["name"])
	if name == "" || len(name) > 64 || len(aliases) != 2 {
		t.Fatal("invalid aliases")
	}
	history := shared.Arr(input["input"])
	call := shared.Obj(history[1])
	if call["name"] != name || call["namespace"] != nil || call["input"] != args || shared.Obj(history[2])["output"] != args {
		t.Fatal("history altered outside tool identity")
	}
	if shared.Obj(shared.Obj(input["tool_choice"])["custom"])["name"] != name {
		t.Fatal("nested tool choice lost mapping")
	}
	response := shared.Object{"type": "response.completed", "response": shared.Object{"output": []any{
		shared.Object{"type": "custom_tool_call", "name": name, "input": args},
		shared.Object{"type": "message", "content": []any{shared.Object{"type": "output_text", "text": name}}},
	}}}
	aliases.Restore(response)
	output := shared.Arr(shared.Obj(response["response"])["output"])
	restored := shared.Obj(output[0])
	if restored["name"] != "apply_change" || restored["namespace"] != namespace || restored["input"] != args {
		t.Fatal("custom identity not restored")
	}
	if shared.Obj(shared.Arr(shared.Obj(output[1])["content"])[0])["text"] != name {
		t.Fatal("normal text modified")
	}
	before := string(shared.Marshal(input))
	if got := PrepareChatTools(input); got != nil || string(shared.Marshal(input)) != before {
		t.Fatal("short tools unexpectedly changed")
	}
}

// TestGeminiSchemaDataPreservation 清理嵌套 schema 元数据时保留同名业务属性、默认数据及工具调用参数。
func TestGeminiSchemaDataPreservation(t *testing.T) {
	data := shared.Object{"id": "data", "$anchor": "literal-data"}
	schema := shared.Object{"id": "schema-id", "type": "object", "properties": shared.Object{
		"id":   shared.Object{"type": "string", "$anchor": "nested"},
		"rows": shared.Object{"type": "array", "items": shared.Object{"type": "object", "$dynamicAnchor": "nested", "default": data}},
	}}
	body := shared.Object{"tools": []any{shared.Object{"functionDeclarations": []any{shared.Object{"name": "lookup", "parameters": schema}}}}, "contents": []any{shared.Object{"parts": []any{shared.Object{"functionCall": shared.Object{"args": data}}}}}}
	CleanGeminiSchemas(body)
	if schema["id"] != "schema-id" {
		t.Fatal("shared source schema mutated")
	}
	schema = shared.Obj(shared.Obj(shared.Arr(shared.Obj(shared.Arr(body["tools"])[0])["functionDeclarations"])[0])["parameters"])
	properties := shared.Obj(schema["properties"])
	if schema["id"] != nil || properties["id"] == nil || shared.Obj(properties["id"])["$anchor"] != nil {
		t.Fatal("schema/property boundaries mixed")
	}
	items := shared.Obj(shared.Obj(properties["rows"])["items"])
	if items["$dynamicAnchor"] != nil || shared.Obj(items["default"])["id"] != "data" || data["$anchor"] != "literal-data" {
		t.Fatal("literal data modified")
	}
}

// TestClaudeEffortMapping 覆盖 CPA 档位转换及不支持 max 时的保守回退。
func TestClaudeEffortMapping(t *testing.T) {
	for _, tc := range []struct {
		input       string
		supportsMax bool
		want        string
	}{
		{"minimal", true, "low"}, {"medium", true, "medium"}, {"xhigh", true, "max"},
		{"xhigh", false, "high"}, {"auto", true, "high"}, {" HIGH ", true, "high"},
	} {
		if got, ok := MapToClaudeEffort(tc.input, tc.supportsMax); !ok || got != tc.want {
			t.Fatal(tc.input, got, ok)
		}
	}
	if _, ok := MapToClaudeEffort("invalid", true); ok {
		t.Fatal("invalid level accepted")
	}
}
