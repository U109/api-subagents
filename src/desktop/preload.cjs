const {contextBridge, ipcRenderer} = require('electron');

// 只公开固定用途的方法，禁止渲染页面指定 IPC 通道、命令、路径或更新地址。
contextBridge.exposeInMainWorld('desktopApp', {
  /** 获取不含密钥的桌面状态。 */
  getState: () => ipcRenderer.invoke('desktop:get-state'),
  /** 使用随桌面包提供的插件和运行环境安装到本机 Codex。 */
  installPlugin: () => ipcRenderer.invoke('desktop:install-plugin'),
  /** 查询打包时指定的发布仓库。 */
  checkUpdate: () => ipcRenderer.invoke('desktop:check-update'),
  /** 下载已经发现的版本，由更新器检查文件完整性。 */
  downloadUpdate: () => ipcRenderer.invoke('desktop:download-update'),
  /** 用户点击重启后才安装下载完成的版本。 */
  installUpdate: () => ipcRenderer.invoke('desktop:install-update'),
  /** 打开固定的版本下载页，不接受任意外部 URL。 */
  openReleases: () => ipcRenderer.invoke('desktop:open-releases'),
  /** 订阅安全快照，不把 Electron 事件对象交给页面；返回取消订阅函数。 */
  onState: (callback) => {
    // 丢弃 Electron 事件对象，订阅者只能读取明确公开的状态数据。
    const listener = (_event, state) => callback(state);
    ipcRenderer.on('desktop:state', listener);
    return () => ipcRenderer.removeListener('desktop:state', listener);
  },
});
