package config

import "maps"

// ContextPreset 保存已核实的官方容量与依据；别名只用于查表，不改写上游模型 ID。
type ContextPreset struct {
	Model      string `json:"model"`
	Tokens     int    `json:"tokens"`
	Source     string `json:"source"`
	VerifiedAt string `json:"verifiedAt"`
	InputOnly  bool   `json:"inputOnly,omitempty"`
}

var officialModelContexts = buildOfficialModelContexts()

// buildOfficialModelContexts 仅收录有官方依据的具体型号和明确别名，不把未知版本按家族前缀套用容量。
// 官方仅标注 1M 时按 1000000 保守取值；Gemini 使用官方输入上限，不把输出额度额外加到目录容量。
func buildOfficialModelContexts() map[string]ContextPreset {
	const openai = "https://developers.openai.com/api/docs/models/"
	const gemini = "https://ai.google.dev/gemini-api/docs/models/"
	const deepseek = "https://api-docs.deepseek.com/quick_start/pricing/"
	const minimax = "https://platform.minimax.io/docs/api-reference/text-anthropic-api"
	const claude = "https://platform.claude.com/docs/en/about-claude/models/overview"
	rows := []struct {
		model     string
		tokens    int
		source    string
		inputOnly bool
		aliases   []string
	}{
		{"gpt-6-astra", 1050000, openai + "gpt-6-astra", false, nil},
		{"gpt-5.6-luna", 1050000, openai + "gpt-5.6-luna", false, nil},
		{"gpt-5.6-sol", 1050000, openai + "gpt-5.6-sol", false, nil},
		{"gpt-5.6-terra", 1050000, openai + "gpt-5.6-terra", false, nil},
		{"gemini-3.8-flash", 1048576, gemini + "gemini-3.8-flash", true,
			[]string{"gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-high"}},
		{"gemini-3.1-pro-preview", 1048576, gemini + "gemini-3.1-pro-preview", true,
			[]string{"gemini-3.1-pro", "gemini-3.1-pro-low", "gemini-3.1-pro-medium", "gemini-3.1-pro-high", "gemini-3.1-pro-preview-customtools"}},
		{"deepseek-v4-flash", 1000000, deepseek, false, nil},
		{"deepseek-v4-pro", 1000000, deepseek, false, nil},
		{"kimi-k3", 1000000, "https://platform.moonshot.ai/docs/guide/models", false, nil},
		{"minimax-m3", 1000000, minimax, false, nil},
		{"minimax-m2.7", 204800, minimax, false, []string{"minimax-m2.7-highspeed"}},
		{"minimax-m2.5", 204800, minimax, false, []string{"minimax-m2.5-highspeed"}},
		{"minimax-m2.1", 204800, minimax, false, []string{"minimax-m2.1-highspeed"}},
		{"minimax-m2", 204800, minimax, false, nil},
		{"glm-5.3", 1000000, "https://docs.z.ai/guides/llm/glm-5.3", false, nil},
		{"claude-fable-5-1", 1000000, claude, false, nil},
		{"claude-opus-5", 1000000, claude, false, nil},
		{"claude-sonnet-5", 1000000, claude, false, nil},
		{"claude-haiku-4-5-20251001", 200000, claude, false, []string{"claude-haiku-4-5"}},
	}
	result := make(map[string]ContextPreset)
	for _, row := range rows {
		preset := ContextPreset{Model: row.model, Tokens: row.tokens, Source: row.source, VerifiedAt: "2026-09-20", InputOnly: row.inputOnly}
		result[row.model] = preset
		for _, alias := range row.aliases {
			result[alias] = preset
		}
	}
	return result
}

// OfficialModelContexts 给配置界面返回独立的只读资料副本，让界面提示和 Codex 目录使用同一份容量表。
func OfficialModelContexts() map[string]ContextPreset {
	return maps.Clone(officialModelContexts)
}
