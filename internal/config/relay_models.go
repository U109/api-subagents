package config

import (
	"errors"
	"strings"
	"unicode"
)

const MaxRelayModels = 32

// normalizeRelayModels 校验同一连接的额外挟持模型，去除两端空白和重复项，保留用户顺序。
// 缺省或空列表兼容旧配置，Codex 仍可选择该连接的默认模型。
func normalizeRelayModels(models []string) ([]string, error) {
	if len(models) > MaxRelayModels {
		return nil, errors.New("每个连接最多额外添加 32 个挟持模型。")
	}
	var result []string
	seen := map[string]bool{}
	for _, value := range models {
		model := strings.TrimSpace(value)
		if model == "" || len([]rune(model)) > 200 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
			return nil, errors.New("挟持模型 ID 不能为空、超过 200 字符或包含控制字符。")
		}
		if !seen[model] {
			result = append(result, model)
			seen[model] = true
		}
	}
	return result, nil
}
