/** 管理更新状态；只连接打包时确定的发布源，不接受渲染页面传入下载地址或令牌。 */
export class UpdateManager {
  /** 注入更新器便于使用本地模拟验证，不在构造时访问网络或下载文件。 */
  constructor(updater, {version, release, packaged, onChange = () => {}}) {
    if (!/^[\w-]+$/.test(release.owner) || !/^[\w.-]+$/.test(release.repo))
      throw new Error('更新仓库配置无效。');
    this.updater = updater;
    this.release = release;
    this.packaged = packaged;
    this.onChange = onChange;
    this.state = {
      phase: 'idle',
      version,
      availableVersion: '',
      progress: 0,
      message: '检查是否有新版本',
    };
    this.releaseUrl = `https://github.com/${release.owner}/${release.repo}/releases`;
    updater.autoDownload = false;
    updater.autoInstallOnAppQuit = false;
    updater.allowPrerelease = false;
    updater.allowDowngrade = false;
    updater.disableWebInstaller = true;
    updater.setFeedURL({provider: 'github', owner: release.owner, repo: release.repo});
    updater.on('update-available', (info) =>
      this.change({
        phase: 'available',
        availableVersion: info.version,
        message: `发现新版本 ${info.version}`,
      }),
    );
    updater.on('update-not-available', () => this.change({phase: 'latest', message: '已经是最新版本'}));
    updater.on('download-progress', (info) =>
      this.change({
        phase: 'downloading',
        progress: Math.max(0, Math.min(100, Number(info.percent) || 0)),
        message: '正在下载更新…',
      }),
    );
    updater.on('update-downloaded', () =>
      this.change({phase: 'downloaded', progress: 100, message: '更新已下载，重启后安装'}),
    );
    updater.on('error', () =>
      this.change({phase: 'error', message: '更新服务暂时不可用，请检查网络后重试，或前往版本下载页。'}),
    );
  }

  /** 合并可展示状态并发送快照，避免界面修改内部状态。 */
  change(patch) {
    Object.assign(this.state, patch);
    this.onChange({...this.state});
  }

  /** 仅在用户检查时请求版本；私有仓库不向客户端植入作者令牌。 */
  async check() {
    if (['checking', 'downloading', 'downloaded', 'installing'].includes(this.state.phase)) return;
    if (this.release.private) {
      this.change({phase: 'manual', message: '这是私有发布源，请在版本下载页登录 GitHub 后下载。'});
      return;
    }
    if (!this.packaged) {
      this.change({phase: 'manual', message: '开发模式不安装更新，请使用打包后的桌面 App。'});
      return;
    }
    this.change({phase: 'checking', availableVersion: '', message: '正在检查更新…'});
    try {
      await this.updater.checkForUpdates();
    } catch {
      this.change({phase: 'error', message: '暂时无法检查更新，可能尚未发布版本或网络不可用。'});
    }
  }

  /** 只有发现新版本后才开始下载；校验与断点缓存由 electron-updater 处理。 */
  async download() {
    if (this.state.phase !== 'available') throw new Error('请先检查更新并确认有新版本。');
    this.change({phase: 'downloading', progress: 0, message: '正在下载更新…'});
    try {
      await this.updater.downloadUpdate();
    } catch {
      this.change({phase: 'error', message: '更新下载或校验失败，请重新检查更新后重试。'});
    }
  }

  /** 用户确认重启后才安装已校验的更新；普通退出不会触发安装。 */
  install() {
    if (this.state.phase !== 'downloaded') throw new Error('更新尚未下载完成。');
    this.change({phase: 'installing', message: '正在重启并安装更新…'});
    this.updater.quitAndInstall(false, true);
  }
}
