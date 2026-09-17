package codex

import "github.com/U109/api-subagents/internal/shared"

// catalogReasoningLevels 使用 Codex 已支持的标准档位，避免未知枚举导致整个自定义目录被忽略。
// 目录列出可传递的参数而非服务商能力保证，具体模型不支持的档位仍由上游明确报错。
func catalogReasoningLevels() []any {
	levels := []any{}
	for _, option := range [][2]string{{"none", "不思考（需模型支持）"}, {"minimal", "极低：优先响应速度"}, {"low", "低：适合简单任务"}, {"medium", "中：平衡速度与分析深度"}, {"high", "高：更充分地分析"}, {"xhigh", "超高：仅支持此档位的模型可用"}} {
		levels = append(levels, shared.Object{"effort": option[0], "description": option[1]})
	}
	return levels
}
