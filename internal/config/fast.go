package config

import "errors"

// FastMode 只读取当前模型的显式开关；缺省关闭，模型名称含 fast 也不隐式启用。
func (p Profile) FastMode() bool { return p.ModelFastModes[p.Model] }

// normalizeModelFastModes 清理未选模型及关闭项；不把 Gemini 原生协议误当成 OpenAI 的服务档位。
func normalizeModelFastModes(p Profile) (map[string]bool, error) {
	var result map[string]bool
	for _, model := range append([]string{p.Model}, p.RelayModels...) {
		if model == "" || !p.ModelFastModes[model] {
			continue
		}
		if p.Protocol == "gemini" {
			return nil, errors.New("Gemini 原生协议暂不支持此 Fast 开关，请关闭后保存。")
		}
		if result == nil {
			result = map[string]bool{}
		}
		result[model] = true
	}
	return result, nil
}
