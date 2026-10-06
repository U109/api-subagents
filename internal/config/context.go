package config

import (
	"errors"
	"strings"
)

const DefaultContextWindow = 256000
const MinContextWindow = 4096
const MaxContextWindow = 2000000

// ContextWindow 保留手动容量；自动容量取已核实规格和 256K 的较小值，未知型号暂用 256K。
func (p Profile) ContextWindow(model string) int {
	if size := p.ModelContextWindows[model]; size >= MinContextWindow && size <= MaxContextWindow {
		return size
	}
	if preset, ok := officialModelContexts[strings.ToLower(strings.TrimSpace(model))]; ok {
		return min(preset.Tokens, DefaultContextWindow)
	}
	return DefaultContextWindow
}

// normalizeModelContextWindows 仅保存已选模型的有效容量；零值恢复默认，移除的模型不会留下孤立设置。
func normalizeModelContextWindows(p Profile) (map[string]int, error) {
	var result map[string]int
	for _, id := range append([]string{p.Model}, p.RelayModels...) {
		if id == "" {
			continue
		}
		size := p.ModelContextWindows[id]
		if size == 0 {
			continue
		}
		if size < MinContextWindow || size > MaxContextWindow {
			return nil, errors.New("模型上下文长度必须为 4096–2000000 的整数 tokens，留空使用默认值")
		}
		if result == nil {
			result = map[string]int{}
		}
		result[id] = size
	}
	return result, nil
}
