package config

import "errors"

// normalizeModelCompatibility 仅保存已选模型的明确策略；自动模式省略存储，旧配置保持通用网关行为。
func normalizeModelCompatibility(p Profile) (map[string]string, error) {
	var result map[string]string
	for _, id := range append([]string{p.Model}, p.RelayModels...) {
		mode := p.ModelCompatibility[id]
		if id == "" || mode == "" || mode == "auto" {
			continue
		}
		switch mode {
		case "generic", "deepseek", "kimi", "kimi-coding", "doubao", "minimax", "glm", "gemini":
		default:
			return nil, errors.New("未知模型兼容策略，请重新选择")
		}
		if result == nil {
			result = map[string]string{}
		}
		result[id] = mode
	}
	return result, nil
}
