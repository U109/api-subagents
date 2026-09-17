package config

import (
	"errors"
	"strings"
	"unicode"
)

const MaxModelNameLength = 80

// ModelName 返回模型的自定义显示名称；旧配置和空名称使用真实 ID，不改变上游请求参数。
func (p Profile) ModelName(model string) string {
	if name := p.ModelNames[model]; name != "" {
		return name
	}
	return model
}

// normalizeModelNames 仅保存默认及已选模型的显示名称，清理已取消的模型和恢复默认的空值。
// 名称按 Unicode 字符限长且禁止控制字符；错误不包含输入内容，路由始终使用原模型 ID。
func normalizeModelNames(p Profile) (map[string]string, error) {
	var result map[string]string
	for _, id := range append([]string{p.Model}, p.RelayModels...) {
		if id == "" {
			continue
		}
		name := strings.TrimSpace(p.ModelNames[id])
		if len([]rune(name)) > MaxModelNameLength || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return nil, errors.New("模型显示名称最多 80 字符，不能包含控制字符。")
		}
		if name != "" && name != id {
			if result == nil {
				result = map[string]string{}
			}
			result[id] = name
		}
	}
	return result, nil
}
