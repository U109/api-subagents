//go:build windows

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/U109/api-subagents/bundle"
	"github.com/U109/api-subagents/frontend"
	"github.com/U109/api-subagents/internal/buildinfo"
	codexconfig "github.com/U109/api-subagents/internal/codex"
	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/platform"
	pluginruntime "github.com/U109/api-subagents/internal/plugin"
	"github.com/U109/api-subagents/internal/relay"
	"github.com/U109/api-subagents/internal/settings"
	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/updates"
	"github.com/U109/api-subagents/packaging"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx            context.Context
	cancel         context.CancelFunc
	service        *settings.ConfigService
	updater        *updates.Updater
	relay          *relay.Relay
	installOptions pluginruntime.InstallOptions
	mu             sync.Mutex
	plugin         shared.Object
	dirty, closing bool
	busy           atomic.Int32
	smoke          string
}

// newApp 复用本机配置目录，初始化轻量 Go 服务；构造期间不会调用模型或更新接口。
func newApp(packaged bool) *App {
	var release updates.Release
	_ = json.Unmarshal(packaging.Release, &release)
	release.Version = buildinfo.Version
	app := &App{service: settings.NewConfigService(configstore.NewConfigStore("")), installOptions: pluginruntime.DefaultInstallOptions(), plugin: shared.Object{"phase": "idle", "installedVersion": "", "message": "将插件安装到 Codex，即可使用配置的模型"}}
	app.updater = updates.NewUpdater(release, filepath.Join(configstore.DataDir(), "updates"), packaged)
	app.updater.OnChange = app.broadcast
	app.relay = relay.New(app.service.Store, codexconfig.CodexConfig{Home: app.installOptions.CodexHome, DataRoot: configstore.DataDir()})
	app.relay.OnChange = app.broadcast
	for i, arg := range os.Args {
		if arg == "--smoke-report" && i+1 < len(os.Args) {
			app.smoke = os.Args[i+1]
		}
	}
	return app
}

// startup 获取窗口生命周期上下文，后台检测已安装版本，不阻塞首屏渲染。
func (a *App) startup(ctx context.Context) {
	a.ctx, a.cancel = context.WithCancel(ctx)
	_ = a.relay.Recover()
	go func() {
		version := pluginruntime.InstalledVersion(a.installOptions)
		if version != "" {
			a.mu.Lock()
			a.plugin = shared.Object{"phase": "installed", "installedVersion": version, "message": "已安装，配置模型后在 Codex 新建对话即可使用"}
			a.mu.Unlock()
			a.broadcast()
		}
	}()
	if a.smoke != "" {
		go func() {
			select {
			case <-time.After(30 * time.Second):
				_ = shared.AtomicWrite(a.smoke, shared.Marshal(shared.Object{"ok": false, "error": "WebView2 页面启动超时"}), 0600)
				wailsruntime.Quit(a.ctx)
			case <-a.ctx.Done():
			}
		}()
	}
}

// shutdown 取消界面发起的网络请求，独立 MCP 进程使用自己的生命周期。
func (a *App) shutdown(_ context.Context) {
	if a.cancel != nil {
		a.cancel()
	}
}

// broadcast 使用事件推送安装和下载进度，避免页面频繁轮询后台。
func (a *App) broadcast() {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "desktop:state", a.GetState())
	}
}

// GetState 只返回版本和操作状态，不包含连接地址、Key 或文件执行权限。
func (a *App) GetState() shared.Object {
	a.mu.Lock()
	plugin := shared.Object{}
	for k, v := range a.plugin {
		plugin[k] = v
	}
	a.mu.Unlock()
	return shared.Object{"version": buildinfo.Version, "plugin": plugin, "update": a.updater.Snapshot(), "relay": a.relay.Snapshot()}
}

// API 将固定配置路由传给 Go 服务，后台请求可取消，页面不接受任意文件操作。
func (a *App) API(route, body string) (shared.Object, error) {
	a.busy.Add(1)
	defer a.busy.Add(-1)
	value, err := a.service.Handle(a.ctx, route, []byte(body))
	if err == nil && body != "" && (route == "/api/config" || route == "/api/config/copy" || route == "/api/config/remove") {
		if refreshErr := a.relay.RefreshCatalog(); refreshErr != nil {
			value["warning"] = refreshErr.Error()
		}
	}
	return value, err
}

// EnableRelay 只允许已保存的连接接管主模型，草稿或配置请求尚未完成时明确阻止。
func (a *App) EnableRelay(model string) (shared.Object, error) {
	a.mu.Lock()
	dirty := a.dirty
	a.mu.Unlock()
	if dirty || a.busy.Load() > 0 {
		return a.GetState(), errors.New("请先保存当前配置并等待操作完成。")
	}
	err := a.relay.Enable(model)
	return a.GetState(), err
}

// DisableRelay 恢复 Codex 原始模型设置并关闭本地网关，不修改登录信息和权限策略。
func (a *App) DisableRelay() (shared.Object, error) {
	err := a.relay.Disable()
	return a.GetState(), err
}

// SetDirty 同步未保存草稿状态，供窗口关闭和更新安装共同检查。
func (a *App) SetDirty(dirty bool) { a.mu.Lock(); a.dirty = dirty; a.mu.Unlock() }

// InstallPlugin 串行安装内嵌插件，完成前禁止窗口关闭或重启更新。
func (a *App) InstallPlugin() (shared.Object, error) {
	a.mu.Lock()
	if a.plugin["phase"] == "installing" {
		a.mu.Unlock()
		return a.GetState(), nil
	}
	a.plugin["phase"] = "installing"
	a.plugin["message"] = "正在安装独立 Go 插件…"
	a.mu.Unlock()
	a.broadcast()
	source, err := bundle.Open()
	if err == nil {
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
		defer cancel()
		var result pluginruntime.InstallResult
		result, err = pluginruntime.InstallPlugin(ctx, source, a.installOptions)
		a.mu.Lock()
		if err == nil {
			if result.Installed {
				a.plugin = shared.Object{"phase": "installed", "installedVersion": buildinfo.Version, "message": "安装成功！保存配置后在 Codex 新建对话即可使用。"}
			} else {
				a.plugin = shared.Object{"phase": "manual", "installedVersion": "", "message": "插件已准备好。请先安装并启动 Codex，再点击安装。"}
			}
		}
		a.mu.Unlock()
	}
	if err != nil {
		a.mu.Lock()
		a.plugin["phase"] = "error"
		a.plugin["message"] = err.Error()
		a.mu.Unlock()
	}
	a.broadcast()
	return a.GetState(), err
}

// CheckUpdate 仅在用户点击后访问固定 GitHub 发布源。
func (a *App) CheckUpdate() (shared.Object, error) {
	err := a.updater.Check(a.ctx)
	return a.GetState(), err
}

// DownloadUpdate 下载已发现的版本并推送进度，不自动运行安装器。
func (a *App) DownloadUpdate() (shared.Object, error) {
	err := a.updater.Download(a.ctx)
	return a.GetState(), err
}

// InstallUpdate 核对未保存状态、插件安装和下载校验，成功启动安装器后才退出当前 App。
func (a *App) InstallUpdate() (shared.Object, error) {
	a.mu.Lock()
	blocked := a.dirty || a.plugin["phase"] == "installing"
	a.mu.Unlock()
	if blocked || a.busy.Load() > 0 {
		return a.GetState(), errors.New("请先保存配置并等待当前操作完成。")
	}
	installer, err := a.updater.Installer()
	if err != nil {
		return a.GetState(), err
	}
	if a.relay.Snapshot().Enabled {
		if err = a.relay.Disable(); err != nil {
			return a.GetState(), err
		}
	}
	// NSIS 从现有安装记录读取目标目录，避免 /D 特殊解析规则破坏含空格路径。
	cmd := exec.Command(installer, "/UPDATE")
	platform.HideProcess(cmd)
	if err = cmd.Start(); err != nil {
		return a.GetState(), errors.New("无法启动更新安装器，请重试。")
	}
	_ = cmd.Process.Release()
	a.mu.Lock()
	a.closing = true
	a.mu.Unlock()
	a.updater.Installing()
	state := a.GetState()
	go func() { time.Sleep(150 * time.Millisecond); wailsruntime.Quit(a.ctx) }()
	return state, nil
}

// OpenReleases 打开固定版本下载页，页面不能提供任意链接。
func (a *App) OpenReleases() shared.Object {
	wailsruntime.BrowserOpenURL(a.ctx, a.updater.ReleaseURL())
	return a.GetState()
}

// beforeClose 保留安装中的窗口，草稿存在时由原生对话框明确选择是否放弃。
func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	installing := a.plugin["phase"] == "installing"
	dirty, closing := a.dirty, a.closing
	a.mu.Unlock()
	if closing {
		return false
	}
	if installing {
		_, _ = wailsruntime.MessageDialog(ctx, wailsruntime.MessageDialogOptions{Type: wailsruntime.InfoDialog, Title: "API Subagents", Message: "正在安装插件，请完成后再关闭。"})
		return true
	}
	if dirty {
		answer, err := wailsruntime.MessageDialog(ctx, wailsruntime.MessageDialogOptions{Type: wailsruntime.QuestionDialog, Title: "未保存的配置", Message: "有尚未保存的模型配置。", Buttons: []string{"继续编辑", "放弃修改并关闭"}, DefaultButton: "继续编辑", CancelButton: "继续编辑"})
		if err != nil || answer != "放弃修改并关闭" {
			return true
		}
	}
	if a.relay.Snapshot().Enabled {
		answer, err := wailsruntime.MessageDialog(ctx, wailsruntime.MessageDialogOptions{Type: wailsruntime.QuestionDialog, Title: "挟持模式正在运行", Message: "关闭 App 会停止本地模型连接并恢复 Codex 原配置。", Buttons: []string{"保持运行", "关闭并恢复"}, DefaultButton: "保持运行", CancelButton: "保持运行"})
		if err != nil || answer != "关闭并恢复" {
			return true
		}
		if err = a.relay.Disable(); err != nil {
			_, _ = wailsruntime.MessageDialog(ctx, wailsruntime.MessageDialogOptions{Type: wailsruntime.ErrorDialog, Title: "无法恢复配置", Message: err.Error()})
			return true
		}
	}
	return false
}

// FrontendReady 仅供隔离启动测试记录真实 WebView2 与配置绑定是否就绪，正常运行无副作用。
func (a *App) FrontendReady(report shared.Object) {
	if a.smoke == "" {
		return
	}
	report["version"] = buildinfo.Version
	report["engine"] = "Wails/WebView2"
	_ = shared.AtomicWrite(a.smoke, shared.Marshal(report), 0600)
	go func() { time.Sleep(100 * time.Millisecond); wailsruntime.Quit(a.ctx) }()
}

// Run 启动单实例 Wails 窗口，沿用现有配置界面和窗口尺寸。
func Run(packaged bool) {
	app := newApp(packaged)
	instance := "com.u109.api-subagents"
	if app.smoke != "" {
		instance += "." + shared.Hash([]byte(app.smoke))[:12]
	}
	err := wails.Run(&options.App{Title: "API Subagents", Width: 1180, Height: 880, MinWidth: 820, MinHeight: 640, BackgroundColour: options.NewRGB(255, 255, 255), AssetServer: &assetserver.Options{Assets: frontend.Files}, OnStartup: app.startup, OnShutdown: app.shutdown, OnBeforeClose: app.beforeClose, Bind: []interface{}{app}, SingleInstanceLock: &options.SingleInstanceLock{UniqueId: instance, OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
		if app.ctx != nil {
			wailsruntime.WindowUnminimise(app.ctx)
			wailsruntime.Show(app.ctx)
		}
	}}, Windows: &windows.Options{WebviewIsTransparent: false, WindowIsTranslucent: false}})
	if err != nil {
		if app.smoke != "" {
			_ = shared.AtomicWrite(app.smoke, shared.Marshal(shared.Object{"ok": false, "error": err.Error()}), 0600)
		}
		os.Exit(1)
	}
}
