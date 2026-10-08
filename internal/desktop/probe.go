//go:build windows

package desktop

import (
	"context"
	"errors"
	"regexp"
)

var probeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,80}$`)

// startProbeContext 按前端随机请求号隔离连接测试；先到的取消请求也会生效，重复活动号被拒绝。
func (a *App) startProbeContext(id string) (context.Context, func(), error) {
	if !probeIDPattern.MatchString(id) {
		return nil, nil, errors.New("连接测试请求号无效")
	}
	a.probeMu.Lock()
	defer a.probeMu.Unlock()
	if a.probes == nil {
		a.probes = make(map[string]context.CancelFunc)
	}
	if _, exists := a.probes[id]; exists {
		return nil, nil, errors.New("连接测试请求正在执行")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.probes[id] = cancel
	if a.probeCancelled[id] {
		cancel()
		delete(a.probeCancelled, id)
	}
	return ctx, func() {
		cancel()
		a.probeMu.Lock()
		delete(a.probes, id)
		a.probeMu.Unlock()
	}, nil
}

// CancelProbe 只取消指定连接测试，不取消保存、插件或更新；有界保留提前到达的取消意图以处理 IPC 竞态。
func (a *App) CancelProbe(id string) error {
	if !probeIDPattern.MatchString(id) {
		return errors.New("连接测试请求号无效")
	}
	a.probeMu.Lock()
	defer a.probeMu.Unlock()
	if cancel, ok := a.probes[id]; ok {
		cancel()
		return nil
	}
	if a.probeCancelled == nil {
		a.probeCancelled = make(map[string]bool)
	}
	if len(a.probeCancelled) >= 32 {
		for key := range a.probeCancelled {
			delete(a.probeCancelled, key)
			break
		}
	}
	a.probeCancelled[id] = true
	return nil
}
