import {app, BrowserWindow, ipcMain, dialog, shell, Menu} from 'electron';
import updaterPackage from 'electron-updater';
import path from 'node:path';
import os from 'node:os';
import {startSetup} from '../setup.mjs';
import {dataDir} from '../config.mjs';
import {PluginManager} from './plugin-manager.mjs';
import {UpdateManager} from './update-manager.mjs';
import release from '../../desktop/release.json' with {type: 'json'};

let window, setup, plugins, updates;

/** 只向界面返回安装与更新状态，不把配置密钥或本地执行能力暴露给页面。 */
function snapshot() {
  return {version: app.getVersion(), plugin: {...plugins.state}, update: {...updates.state}};
}

/** 将后台进度推送给仍然存在的本机窗口，关闭期间不再发送。 */
function broadcast() {
  if (window && !window.isDestroyed() && plugins && updates)
    window.webContents.send('desktop:state', snapshot());
}

/** 限制 IPC 到唯一窗口的本机主框架，拒绝子框架、其他网页及任意传入命令。 */
function registerAction(channel, action) {
  ipcMain.handle(channel, async (event) => {
    if (
      !window ||
      event.sender !== window.webContents ||
      event.senderFrame !== window.webContents.mainFrame ||
      new URL(event.senderFrame.url).origin !== setup.origin
    )
      throw new Error('拒绝非本机配置页面的请求。');
    await action();
    return snapshot();
  });
}

/** 启动原配置服务和隔离渲染窗口；原有模型配置路径保持一致，不做迁移或覆盖。 */
async function launch() {
  app.setAppUserModelId('com.u109.api-subagents');
  Menu.setApplicationMenu(null);
  const resources = app.isPackaged
    ? process.resourcesPath
    : path.join(app.getAppPath(), '.desktop-resources');
  setup = await startSetup({htmlPath: path.join(resources, 'plugin', 'dist', 'config.html')});
  plugins = new PluginManager({
    resources,
    profileRoot: os.homedir(),
    dataRoot: dataDir(),
    codexHome: process.env.CODEX_HOME || path.join(os.homedir(), '.codex'),
    version: app.getVersion(),
    nodeVersion: release.nodeVersion,
    onChange: broadcast,
  });
  updates = new UpdateManager(updaterPackage.autoUpdater, {
    version: app.getVersion(),
    release,
    packaged: app.isPackaged,
    onChange: broadcast,
  });
  registerAction('desktop:get-state', async () => {});
  registerAction('desktop:install-plugin', () => plugins.install());
  registerAction('desktop:check-update', () => updates.check());
  registerAction('desktop:download-update', () => updates.download());
  registerAction('desktop:install-update', () => {
    if (plugins.state.phase === 'installing') throw new Error('请等待插件安装完成后再重启。');
    updates.install();
  });
  registerAction('desktop:open-releases', () => shell.openExternal(updates.releaseUrl));
  window = new BrowserWindow({
    width: 1180,
    height: 880,
    minWidth: 820,
    minHeight: 640,
    show: false,
    title: 'API Subagents',
    backgroundColor: '#ffffff',
    icon: path.join(app.getAppPath(), 'dist-desktop', 'icon.ico'),
    webPreferences: {
      preload: path.join(app.getAppPath(), 'dist-desktop', 'preload.cjs'),
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webviewTag: false,
    },
  });
  window.webContents.session.setPermissionRequestHandler((_contents, _permission, callback) =>
    callback(false),
  );
  window.webContents.setWindowOpenHandler(() => ({action: 'deny'}));
  window.webContents.on('will-navigate', (event, url) => {
    if (url.split('#')[0] !== setup.origin + '/') event.preventDefault();
  });
  window.webContents.on('will-attach-webview', (event) => event.preventDefault());
  window.on('close', (event) => {
    if (plugins.state.phase === 'installing') {
      event.preventDefault();
      void dialog.showMessageBox(window, {type: 'info', message: '正在安装插件，请完成后再关闭。'});
    }
  });
  window.webContents.on('will-prevent-unload', (event) => {
    const choice = dialog.showMessageBoxSync(window, {
      type: 'question',
      buttons: ['继续编辑', '放弃修改并关闭'],
      defaultId: 0,
      cancelId: 0,
      message: '有尚未保存的模型配置。',
    });
    if (choice === 1) event.preventDefault();
  });
  window.once('ready-to-show', () => window.show());
  await window.loadURL(setup.url);
  await plugins.refresh();
}

if (!app.requestSingleInstanceLock()) app.quit();
else {
  app.on('second-instance', () => {
    if (window?.isMinimized()) window.restore();
    window?.focus();
  });
  app.on('window-all-closed', () => app.quit());
  app.on('will-quit', () => {
    setup?.server.closeAllConnections();
    setup?.server.close();
  });
  app
    .whenReady()
    .then(launch)
    .catch((error) => {
      dialog.showErrorBox(
        'API Subagents 启动失败',
        `请重新打开应用；如果仍然失败，请重新安装桌面版。\n${error.message}`,
      );
      app.quit();
    });
}
