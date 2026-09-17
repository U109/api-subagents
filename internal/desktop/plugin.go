//go:build windows

package desktop

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"time"

	"github.com/U109/api-subagents/bundle"
	pluginruntime "github.com/U109/api-subagents/internal/plugin"
	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/updates"
)

// CheckPluginUpdate 只查询插件发布清单，以实际安装版本比较，不下载文件或修改 Codex。
func (a *App) CheckPluginUpdate() (shared.Object, error) {
	a.mu.Lock()
	installing := a.plugin["phase"] == "installing"
	a.mu.Unlock()
	if installing {
		return a.GetState(), errors.New("请等待插件安装完成。")
	}
	err := a.pluginUpdater.Check(a.ctx)
	return a.GetState(), err
}

// InstallPlugin 安装随 App 提供的插件；已有更高版本时拒绝降级，避免独立更新被旧 App 覆盖。
func (a *App) InstallPlugin() (shared.Object, error) { return a.installPlugin(false) }

// UpdatePlugin 下载并验证已经检查过的插件版本，完成后原地安装，不关闭 App 或修改模型配置。
func (a *App) UpdatePlugin() (shared.Object, error) { return a.installPlugin(true) }

// installPlugin 串行处理两种来源；在线包先校验哈希、版本和文件白名单，再进入固定的插件安装流程。
func (a *App) installPlugin(remote bool) (shared.Object, error) {
	a.mu.Lock()
	if a.plugin["phase"] == "installing" || a.closeInProgressLocked() || a.updater.Snapshot().Phase == "installing" {
		a.mu.Unlock()
		return a.GetState(), errors.New("请等待当前安装完成。")
	}
	installed := shared.Str(a.plugin["installedVersion"])
	if !remote && (a.bundledVersion == "" || updates.NewerVersion(installed, a.bundledVersion)) {
		a.mu.Unlock()
		return a.GetState(), errors.New("已安装插件高于 App 附带版本，请使用独立插件更新。")
	}
	if remote && !shared.Contains([]string{"available", "downloaded"}, a.pluginUpdater.Snapshot().Phase) {
		a.mu.Unlock()
		return a.GetState(), errors.New("请先检查插件更新。")
	}
	a.plugin["phase"], a.plugin["message"] = "installing", "正在安装插件…"
	if remote {
		a.plugin["message"] = "正在下载并校验插件更新…"
	}
	a.mu.Unlock()
	a.broadcast()
	var source fs.FS
	var err error
	if remote {
		if a.pluginUpdater.Snapshot().Phase == "available" {
			err = a.pluginUpdater.Download(a.ctx)
		}
		if err == nil {
			var data []byte
			var version string
			data, version, err = a.pluginUpdater.PluginArchive()
			if err == nil {
				source, err = pluginruntime.OpenUpdateArchive(data, version)
			}
		}
	} else {
		source, err = bundle.Open()
	}
	var result pluginruntime.InstallResult
	if err == nil {
		a.mu.Lock()
		a.plugin["message"] = "正在将插件安装到 Codex…"
		a.mu.Unlock()
		a.broadcast()
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
		result, err = pluginruntime.InstallPlugin(ctx, source, a.installOptions)
		cancel()
	}
	a.mu.Lock()
	if err != nil {
		a.plugin["phase"], a.plugin["message"] = "error", err.Error()
	} else if result.Installed {
		installed = strings.Split(result.Version, "+")[0]
		a.plugin = shared.Object{"phase": "installed", "installedVersion": installed, "message": "插件安装成功，在 Codex 新建对话即可使用。"}
	} else {
		a.plugin = shared.Object{"phase": "manual", "installedVersion": installed, "message": "插件文件已准备好。请先安装并启动 Codex，再点击安装。"}
	}
	a.mu.Unlock()
	if err == nil {
		a.pluginUpdater.ResetPluginVersion(installed)
	}
	a.broadcast()
	return a.GetState(), err
}
