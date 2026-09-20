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

// OrderedModels 返回当前连接全部已选模型的稳定顺序；旧配置先放默认模型，再沿用额外模型顺序。
// modelOrder 中已删除、重复或尚未选中的项会被忽略，缺失项按旧顺序补到末尾。
func (p Profile) OrderedModels() []string {
	legacy := append([]string{p.Model}, p.RelayModels...)
	selected := make(map[string]bool, len(legacy))
	for _, model := range legacy {
		if model != "" {
			selected[model] = true
		}
	}
	result := make([]string, 0, len(selected))
	seen := make(map[string]bool, len(selected))
	for _, model := range append(append([]string(nil), p.ModelOrder...), legacy...) {
		model = strings.TrimSpace(model)
		if selected[model] && !seen[model] {
			result = append(result, model)
			seen[model] = true
		}
	}
	return result
}

// normalizeModelOrder 只保存覆盖全部已选模型且区别于旧顺序的拖拽结果；部分旧字段回退到默认顺序。
func normalizeModelOrder(p Profile) []string {
	if len(p.ModelOrder) == 0 {
		return nil
	}
	selected := make(map[string]bool, 1+len(p.RelayModels))
	for _, model := range append([]string{p.Model}, p.RelayModels...) {
		if model != "" {
			selected[model] = true
		}
	}
	order := make([]string, 0, len(selected))
	seen := make(map[string]bool, len(selected))
	for _, value := range p.ModelOrder {
		model := strings.TrimSpace(value)
		if selected[model] && !seen[model] {
			order = append(order, model)
			seen[model] = true
		}
	}
	if len(order) != len(selected) {
		return nil
	}
	legacy := (Profile{Model: p.Model, RelayModels: p.RelayModels}).OrderedModels()
	equal := len(order) == len(legacy)
	for index := range order {
		equal = equal && order[index] == legacy[index]
	}
	if equal {
		return nil
	}
	return order
}
