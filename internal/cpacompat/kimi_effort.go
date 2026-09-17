package cpacompat

import (
	"fmt"
	"github.com/tidwall/sjson"
)

// ApplyKimiCodingThinking 使用 CPA 的 Kimi Coding 原生格式；Moonshot 开放平台不应调用此函数。
func ApplyKimiCodingThinking(body []byte, effort string) ([]byte, error) {
	if effort == "none" {
		return applyDisabledThinking(body)
	}
	return applyEnabledThinking(body, effort)
}

// applyEnabledThinking 移植 CPA 的 Kimi Coding 思考写入，清除旧档位并保留 thinking.keep。
func applyEnabledThinking(body []byte, effort string) ([]byte, error) {
	result, errDeleteLegacyEffort := sjson.DeleteBytes(body, "reasoning_effort")
	if errDeleteLegacyEffort != nil {
		return body, fmt.Errorf("kimi thinking: failed to clear reasoning_effort: %w", errDeleteLegacyEffort)
	}
	result, errSetType := sjson.SetBytes(result, "thinking.type", "enabled")
	if errSetType != nil {
		return body, fmt.Errorf("kimi thinking: failed to set thinking.type: %w", errSetType)
	}
	result, errSetEffort := sjson.SetBytes(result, "thinking.effort", effort)
	if errSetEffort != nil {
		return body, fmt.Errorf("kimi thinking: failed to set thinking.effort: %w", errSetEffort)
	}
	return result, nil
}

// applyDisabledThinking 移植 CPA 的显式关闭逻辑，清除与 disabled 冲突的旧字段。
func applyDisabledThinking(body []byte) ([]byte, error) {
	result, errDeleteThinking := sjson.DeleteBytes(body, "thinking")
	if errDeleteThinking != nil {
		return body, fmt.Errorf("kimi thinking: failed to clear thinking object: %w", errDeleteThinking)
	}
	result, errDeleteEffort := sjson.DeleteBytes(result, "reasoning_effort")
	if errDeleteEffort != nil {
		return body, fmt.Errorf("kimi thinking: failed to clear reasoning_effort: %w", errDeleteEffort)
	}
	result, errSetType := sjson.SetBytes(result, "thinking.type", "disabled")
	if errSetType != nil {
		return body, fmt.Errorf("kimi thinking: failed to set thinking.type: %w", errSetType)
	}
	return result, nil
}
