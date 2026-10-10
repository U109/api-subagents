package config

import (
	"errors"
)

const DefaultContextWindow = 256000
const MinContextWindow = 4096
const MaxContextWindow = 2000000

// ContextWindow 对目录明确声明的容量取官方值与 256K 的较小值；未知容量保留旧配置手动值，避免静默改写既有连接。
func (p Profile) ContextWindow(model string) int {
	if official := p.ModelOfficialContexts[model]; official > 0 && official <= 100000000 {
		return min(official, DefaultContextWindow)
	}
	if size := p.ModelContextWindows[model]; size >= MinContextWindow && size <= MaxContextWindow {
		return size
	}
	return DefaultContextWindow
}

// normalizeModelOfficialContexts 只保存已选模型的明确目录数据，不按型号猜测；未知值为零时清除。
func normalizeModelOfficialContexts(p Profile) (map[string]int, error) {
	var result map[string]int
	for _, id := range append([]string{p.Model}, p.RelayModels...) {
		if id == "" {
			continue
		}
		size := p.ModelOfficialContexts[id]
		if size == 0 {
			continue
		}
		if size < 0 || size > 100000000 {
			return nil, errors.New("模型声明的上下文必须为 1–100000000 的整数 tokens，未知请留空")
		}
		if result == nil {
			result = map[string]int{}
		}
		result[id] = size
	}
	return result, nil
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
			return nil, errors.New("模型上下文长度必须为 4096–2000000 的整数 tokens，默认 256000")
		}
		if result == nil {
			result = map[string]int{}
		}
		result[id] = size
	}
	return result, nil
}
