// Wails 在页面脚本执行前注入 Go 绑定；普通浏览器配置入口继续使用带令牌的 HTTP API。
if (window.go?.desktop?.App && window.runtime) {
  const app = window.go.desktop.App;
  window.desktopApp = {
    /** 获取只含版本、插件和更新进度的桌面快照。 */
    getState: () => app.GetState(),
    /** 安装随 App 分发的独立插件程序，不接收任意命令或路径。 */
    installPlugin: () => app.InstallPlugin(),
    /** 只查询编译时指定的公开发布源。 */
    checkUpdate: () => app.CheckUpdate(),
    /** 下载并校验已发现版本的安装包。 */
    downloadUpdate: () => app.DownloadUpdate(),
    /** 在保存草稿后显式启动更新安装。 */
    installUpdate: () => app.InstallUpdate(),
    /** 打开固定的 GitHub Releases 地址。 */
    openReleases: () => app.OpenReleases(),
    /** 订阅后台状态事件，使下载和安装过程及时显示。 */
    onState: (callback) => window.runtime.EventsOn('desktop:state', callback),
    /** 通过固定路由绑定访问 Go 配置服务，JSON 中的 Key 不会写入日志。 */
    api: (route, body) => app.API(route, body === undefined ? '' : JSON.stringify(body)),
    /** 同步未保存状态，让原生窗口关闭和更新操作也能保护草稿。 */
    setDirty: (value) => app.SetDirty(value),
    /** 用已保存的连接开启或切换 Codex 主模型，启用前自动保存原配置备份。 */
    enableRelay: (name) => app.EnableRelay(name),
    /** 关闭本地网关并恢复原有 Codex 模型设置。 */
    disableRelay: () => app.DisableRelay(),
  };
}
