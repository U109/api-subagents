package cpacompat

import (
	"encoding/json"

	"github.com/U109/api-subagents/internal/shared"
)

// CleanGeminiSchemas 移植 CPA v7.3.6 的 schema 标识关键字清理，只处理工具参数模式，不扫描聊天或实际参数。
// 基础转换器已处理其余类型与约束；这些新增规则修复严格 Gemini 接口拒绝 schema 标识字段的问题。
func CleanGeminiSchemas(body shared.Object) {
	for _, raw := range shared.Arr(body["tools"]) {
		for _, value := range shared.Arr(shared.Obj(raw)["functionDeclarations"]) {
			tool := shared.Obj(value)
			for _, field := range []string{"parameters", "parametersJsonSchema"} {
				if schema, ok := tool[field].(map[string]any); ok {
					// Worker 的工具定义会跨请求复用，复制模式后再清理，避免并发污染原始定义。
					data, err := json.Marshal(schema)
					if err != nil {
						continue
					}
					var copied shared.Object
					if json.Unmarshal(data, &copied) != nil {
						continue
					}
					cleanGeminiSchemaIdentifiers(copied)
					tool[field] = copied
				}
			}
		}
	}
}

// cleanGeminiSchemaIdentifiers 按 schema 语义递归，保留 properties 中名为 id 的业务属性及 default/enum 数据。
func cleanGeminiSchemaIdentifiers(schema shared.Object) {
	for _, key := range []string{"id", "$anchor", "$vocabulary", "$dynamicRef", "$dynamicAnchor"} {
		delete(schema, key)
	}
	for _, key := range []string{"properties", "$defs", "definitions", "patternProperties", "dependentSchemas"} {
		for _, child := range shared.Obj(schema[key]) {
			cleanGeminiSchemaIdentifiers(shared.Obj(child))
		}
	}
	for _, key := range []string{"items", "additionalProperties", "contains", "propertyNames", "not", "if", "then", "else"} {
		if child, ok := schema[key].(map[string]any); ok {
			cleanGeminiSchemaIdentifiers(child)
		}
		if children, ok := schema[key].([]any); ok {
			for _, child := range children {
				cleanGeminiSchemaIdentifiers(shared.Obj(child))
			}
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		for _, child := range shared.Arr(schema[key]) {
			cleanGeminiSchemaIdentifiers(shared.Obj(child))
		}
	}
}
