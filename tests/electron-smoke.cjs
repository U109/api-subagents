const {app, dialog} = require('electron');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'api-subagents-electron-test-'));
const report = path.join(root, '.desktop-resources', 'smoke-result.json');
process.env.API_SUBAGENTS_HOME = path.join(temporary, 'data');
process.env.CODEX_HOME = path.join(temporary, 'codex');
app.setPath('userData', path.join(temporary, 'electron'));
// Electron 以测试文件启动时没有项目元数据；为被测主进程提供真实构建目录与版本。
app.getAppPath = () => root;
app.getVersion = () => require('../package.json').version;
let finished = false;

/** 写入明确的启动检查结果后退出，避免测试弹出错误窗口并永久挂起。 */
function finish(error) {
  if (finished) return;
  finished = true;
  fs.writeFileSync(report, JSON.stringify({ok: !error, error: error ? String(error) : null}));
  app.exit(error ? 1 : 0);
}

/** 将启动异常转换为失败报告；不会隐藏正式应用中的错误提示。 */
dialog.showErrorBox = (_title, message) => finish(message);
process.on('uncaughtException', finish);
process.on('unhandledRejection', finish);
setTimeout(() => finish('Electron startup timed out'), 20000);
app.on('browser-window-created', (_event, window) => {
  let initialUrl;
  // 配置页初始化后会从地址栏移除令牌，测试只保存主进程首次导航的本机地址。
  window.webContents.once('did-start-navigation', (_event, url) => {
    initialUrl = url;
  });
  window.webContents.on('preload-error', (_event, _path, error) => finish(error));
  window.webContents.on('render-process-gone', (_event, details) => finish(JSON.stringify(details)));
  window.webContents.once('did-finish-load', async () => {
    try {
      const prefs = window.webContents.getLastWebPreferences();
      assert.equal(prefs.nodeIntegration, false);
      assert.equal(prefs.contextIsolation, true);
      assert.equal(prefs.sandbox, true);
      const url = new URL(initialUrl);
      assert.equal(url.hostname, '127.0.0.1');
      const denied = await fetch(url.origin + '/api/config');
      assert.equal(denied.status, 401);
      const response = await fetch(url.origin + '/api/config', {
        headers: {authorization: 'Bearer ' + url.hash.slice(1)},
      });
      assert.equal(response.status, 200);
      assert.deepEqual((await response.json()).config.models, {});
      finish();
    } catch (error) {
      finish(error);
    }
  });
});
require('../dist-desktop/main.cjs');
