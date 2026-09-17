//go:build windows

package desktop

import (
	"context"
	"errors"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// closeState 只向页面发送退出阶段与提示，不暴露配置备份、连接或凭据。
type closeState struct {
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

// closeSnapshot 返回退出流程的副本，供页面展示草稿确认、恢复进度与失败原因。
func (a *App) closeSnapshot() closeState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closeState
}

// closeInProgressLocked 判断恢复是否已开始或窗口是否获准退出；调用者必须持有 a.mu。
func (a *App) closeInProgressLocked() bool {
	return a.closing || a.closeState.Phase == "closing"
}

// beginRequest 在关闭检查的同一把锁下登记操作，防止恢复期间又保存配置或重新开启网关。
// 成功后调用者必须递减 busy；网络探测与配置操作都不会被窗口关闭中途截断。
func (a *App) beginRequest() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closeInProgressLocked() {
		return errors.New("正在恢复配置并退出，请稍候。")
	}
	a.busy.Add(1)
	return nil
}

// beforeClose 先拦住窗口销毁，在后台恢复配置；只有完成恢复的第二次关闭才放行。
// 挟持模式本身不要求确认，草稿确认交给页面弹窗，避免 Windows 原生按钮名称差异。
func (a *App) beforeClose(_ context.Context) bool {
	a.mu.Lock()
	ready := a.closing
	a.mu.Unlock()
	if ready {
		return false
	}
	go a.requestClose(false)
	return true
}

// ConfirmClose 仅响应已显示的草稿确认；恢复前仍重新检查插件安装和配置操作。
func (a *App) ConfirmClose() {
	a.mu.Lock()
	confirmed := a.closeState.Phase == "confirm"
	a.mu.Unlock()
	if confirmed {
		go a.requestClose(true)
	}
}

// CancelClose 取消草稿确认而不修改草稿或挟持状态；已开始的恢复不能被界面撤销。
func (a *App) CancelClose() {
	a.mu.Lock()
	if a.closeState.Phase == "confirm" {
		a.closeState = closeState{}
	}
	a.mu.Unlock()
	a.broadcast()
}

// requestClose 仅在恢复成功后请求 Wails 退出；重复关闭不会启动多次清理。
func (a *App) requestClose(discardDrafts bool) {
	if a.prepareClose(discardDrafts) {
		wailsruntime.Quit(a.ctx)
	}
}

// prepareClose 自动恢复 Codex、取消网关请求并释放监听端口，成功时才标记允许退出。
// 恢复冲突时保留服务和备份；已停用但遗留备份的情况也会尝试恢复，避免下次启动仍被接管。
func (a *App) prepareClose(discardDrafts bool) bool {
	a.mu.Lock()
	if a.closeInProgressLocked() {
		a.mu.Unlock()
		return false
	}
	if discardDrafts && a.closeState.Phase != "confirm" {
		a.mu.Unlock()
		return false
	}
	switch {
	case a.plugin["phase"] == "installing":
		a.closeState = closeState{Phase: "blocked", Message: "正在安装插件，请完成后再关闭。"}
	case a.busy.Load() > 0:
		a.closeState = closeState{Phase: "blocked", Message: "正在处理配置操作，请完成后再关闭。"}
	case a.dirty && !discardDrafts:
		a.closeState = closeState{Phase: "confirm", Message: "关闭会放弃尚未保存的修改，并自动关闭挟持模式、恢复 Codex 配置。"}
	default:
		a.closeState = closeState{Phase: "closing", Message: "正在恢复 Codex 配置并退出…"}
	}
	restore := a.closeState.Phase == "closing"
	a.mu.Unlock()
	a.broadcast()
	if !restore {
		return false
	}
	err := a.relay.Disable()
	a.mu.Lock()
	if err != nil {
		a.closeState = closeState{Phase: "error", Message: "无法自动恢复 Codex 配置，App 已保持运行。" + err.Error()}
	} else {
		a.closing = true
		a.closeState = closeState{Phase: "ready"}
	}
	a.mu.Unlock()
	a.broadcast()
	return err == nil
}
