package codex

import "github.com/U109/api-subagents/internal/shared"

// catalogReasoningLevels 声明可选档位，包括新版 Codex 字符串协议支持的 max 和 ultra。
// 目录列出可传递的参数而非服务商能力保证，具体模型不支持的档位仍由上游明确报错。
func catalogReasoningLevels() []any {
	levels := []any{}
	for _, option := range [][2]string{{"none", "不思考（需模型支持）"}, {"minimal", "极低：优先响应速度"}, {"low", "低：适合简单任务"}, {"medium", "中：平衡速度与分析深度"}, {"high", "高：更充分地分析"}, {"xhigh", "超高：仅支持此档位的模型可用"}, {"max", "最大：需上游支持，可能增加时间和用量"}, {"ultra", "极限：需上游支持，可能显著增加时间和用量"}} {
		levels = append(levels, shared.Object{"effort": option[0], "description": option[1]})
	}
	return levels
}
