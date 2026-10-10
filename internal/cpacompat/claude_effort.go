// 从 CLIProxyAPI v7.3.6 的 internal/thinking/convert.go 移植，MIT 许可
package cpacompat

import "strings"

// MapToClaudeEffort 将通用档位映射到 Claude 自适应思考；xhigh/max/ultra 仅在明确支持时折合 max，否则使用 high。
func MapToClaudeEffort(level string, supportsMax bool) (string, bool) {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "":
		return "", false
	case "minimal":
		return "low", true
	case "low", "medium", "high":
		return level, true
	case "xhigh", "max", "ultra":
		if supportsMax {
			return "max", true
		}
		return "high", true
	case "auto":
		return "high", true
	default:
		return "", false
	}
}
