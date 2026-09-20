package config

import "strings"

// SupportsImages 优先使用用户对单个模型的声明；缺省时识别常见视觉模型系列，未知型号保持仅文字。
// 自定义网关可能限制图片，用户可以显式关闭；本方法只控制 Codex 上传入口，不改写请求内容。
func (p Profile) SupportsImages(model string) bool {
	if supported, exists := p.ModelImageInputs[model]; exists {
		return supported
	}
	id := strings.ToLower(model)
	for _, prefix := range []string{"gemini-", "claude-", "gpt-4o", "gpt-4.1", "gpt-5", "gpt-6", "o3", "o4-mini"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

// normalizeModelImageInputs 保留已选模型的显式开关，包含 false；删除模型后清理孤立能力设置。
func normalizeModelImageInputs(p Profile) map[string]bool {
	var result map[string]bool
	for _, model := range append([]string{p.Model}, p.RelayModels...) {
		if supported, exists := p.ModelImageInputs[model]; exists && model != "" {
			if result == nil {
				result = map[string]bool{}
			}
			result[model] = supported
		}
	}
	return result
}
