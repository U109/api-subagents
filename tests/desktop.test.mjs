import test from 'node:test';
import assert from 'node:assert/strict';
import {EventEmitter} from 'node:events';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {fileURLToPath} from 'node:url';
import {UpdateManager} from '../src/desktop/update-manager.mjs';
import {PluginManager} from '../src/desktop/plugin-manager.mjs';
import {startSetup} from '../dist/server.mjs';

const execute = promisify(execFile);
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const release = {owner: 'example', repo: 'app-releases', private: false};

/** 更新器只模拟网络事件，验证按钮触发的时机，不访问真实发布服务。 */
class FakeUpdater extends EventEmitter {
  calls = [];
  /** 保存固定的仓库源，便于验证客户端没有嵌入凭据。 */
  setFeedURL(value) {
    this.feed = value;
  }
  /** 模拟发现新版本，不擅自开始下载。 */
  async checkForUpdates() {
    this.calls.push('check');
    this.emit('update-available', {version: '0.3.0'});
  }
  /** 模拟完成下载并发出校验完成事件。 */
  async downloadUpdate() {
    this.calls.push('download');
    this.emit('download-progress', {percent: 42});
    this.emit('update-downloaded');
  }
  /** 记录用户主动重启的请求。 */
  quitAndInstall(...args) {
    this.calls.push(['install', ...args]);
  }
}

/** 所有文件操作限于专用测试目录，避免接触真实模型配置与 Codex 插件。 */
async function temporary() {
  const parent = process.env.API_SUBAGENTS_TEST_ROOT || path.join(os.tmpdir(), 'api-subagents-tests');
  await fs.mkdir(parent, {recursive: true});
  return fs.mkdtemp(path.join(parent, 'desktop-'));
}

/** 创建只包含占位运行文件的资源目录，执行器在每个用例中单独提供。 */
async function managerFixture(execImpl) {
  const dir = await temporary();
  const resources = path.join(dir, 'resources');
  await fs.mkdir(path.join(resources, 'runtime'), {recursive: true});
  await fs.writeFile(path.join(resources, 'runtime', 'node.exe'), 'test-runtime');
  await fs.writeFile(path.join(resources, 'runtime', 'LICENSE'), 'test-license');
  const manager = new PluginManager({
    resources,
    profileRoot: path.join(dir, 'profile'),
    dataRoot: path.join(dir, 'data'),
    codexHome: path.join(dir, 'codex'),
    version: '0.2.0',
    nodeVersion: '24.21.0',
    execImpl,
  });
  return {manager, dir, resources};
}

test('desktop update requires separate check, download and restart actions', async () => {
  const updater = new FakeUpdater();
  const states = [];
  const manager = new UpdateManager(updater, {
    version: '0.2.0',
    release,
    packaged: true,
    onChange: (state) => states.push(state),
  });
  assert.equal(updater.autoDownload, false);
  assert.equal(updater.autoInstallOnAppQuit, false);
  assert.equal(updater.allowDowngrade, false);
  assert.deepEqual(updater.feed, {provider: 'github', owner: 'example', repo: 'app-releases'});
  await assert.rejects(manager.download(), /先检查更新/);
  assert.throws(() => manager.install(), /尚未下载/);
  await manager.check();
  assert.equal(manager.state.phase, 'available');
  assert.deepEqual(updater.calls, ['check']);
  await manager.download();
  assert.equal(manager.state.phase, 'downloaded');
  assert.ok(states.some((state) => state.progress === 42));
  await manager.check();
  assert.deepEqual(updater.calls, ['check', 'download']);
  manager.install();
  assert.deepEqual(updater.calls.at(-1), ['install', false, true]);
});

test('private and development distributions use manual downloads without credentials', async () => {
  for (const options of [
    {release: {...release, private: true}, packaged: true},
    {release, packaged: false},
  ]) {
    const updater = new FakeUpdater();
    const manager = new UpdateManager(updater, {version: '0.2.0', ...options});
    await manager.check();
    assert.equal(manager.state.phase, 'manual');
    assert.deepEqual(updater.calls, []);
    await assert.rejects(manager.download());
  }
  assert.throws(() => new UpdateManager(new FakeUpdater(), {release: {owner: '../bad', repo: 'x'}}));
});

test('failed update integrity checks never allow installation and can be retried', async () => {
  const updater = new FakeUpdater();
  updater.downloadUpdate = async () => {
    throw new Error('sha512 checksum mismatch');
  };
  const manager = new UpdateManager(updater, {version: '0.2.0', release, packaged: true});
  await manager.check();
  await manager.download();
  assert.equal(manager.state.phase, 'error');
  assert.throws(() => manager.install(), /尚未下载/);
  await manager.check();
  assert.equal(manager.state.phase, 'available');
  assert.equal(updater.calls.filter((call) => call === 'check').length, 2);
});

test('duplicate checks are ignored while the update request is pending', async () => {
  const updater = new FakeUpdater();
  let finish,
    count = 0;
  updater.checkForUpdates = () => {
    count++;
    return new Promise((resolve) => {
      finish = resolve;
    });
  };
  const manager = new UpdateManager(updater, {version: '0.2.0', release, packaged: true});
  const pending = manager.check();
  await manager.check();
  assert.equal(count, 1);
  updater.emit('update-not-available');
  finish();
  await pending;
  assert.equal(manager.state.phase, 'latest');
});

test('plugin Node runtime survives app replacement and repairs its license on reuse', async () => {
  const {manager, resources} = await managerFixture();
  const original = await manager.prepareRuntime();
  const license = path.join(manager.dataRoot, 'runtime', 'LICENSE-node-24.21.0.txt');
  await fs.unlink(license);
  assert.equal(await manager.prepareRuntime(), original);
  assert.equal(await fs.readFile(license, 'utf8'), 'test-license');
  await fs.writeFile(path.join(resources, 'runtime', 'node.exe'), 'updated-runtime');
  const updated = await manager.prepareRuntime();
  assert.notEqual(updated, original);
  assert.equal(await fs.readFile(original, 'utf8'), 'test-runtime');
  assert.equal(await fs.readFile(updated, 'utf8'), 'updated-runtime');
});

test('plugin installation is serialized and invokes only the packaged script', async () => {
  let finish,
    calls = 0;
  const {manager, resources} = await managerFixture(async (command, args, options) => {
    calls++;
    assert.equal(command, 'powershell.exe');
    assert.ok(args.includes(path.join(resources, 'plugin', 'scripts', 'install.ps1')));
    assert.ok(args.includes('-Json'));
    assert.equal(options.windowsHide, true);
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  const pending = manager.install();
  await manager.install();
  while (!finish) await new Promise((resolve) => setTimeout(resolve, 5));
  assert.equal(calls, 1);
  finish({stdout: JSON.stringify({installed: true, version: '0.2.0+codex.test'})});
  await pending;
  assert.equal(manager.state.phase, 'installed');
  assert.equal(manager.state.installedVersion, '0.2.0');
});

test('missing Codex and installer failures remain distinguishable from success', async () => {
  const {manager} = await managerFixture(async () => ({stdout: '{"installed":false}'}));
  await manager.install();
  assert.equal(manager.state.phase, 'manual');
  manager.execImpl = async () => {
    throw new Error('exit code 1');
  };
  await manager.install();
  assert.equal(manager.state.phase, 'error');
  assert.equal(manager.state.installedVersion, '');
});

test('plugin status requires matching Codex cache, not just copied source files', async () => {
  const {manager} = await managerFixture();
  const manifest = {version: '0.2.0+codex.test'};
  const local = path.join(manager.profileRoot, 'plugins/api-subagents/.codex-plugin/plugin.json');
  const market = path.join(manager.profileRoot, '.agents/plugins/marketplace.json');
  const cached = path.join(
    manager.codexHome,
    'plugins/cache/personal/api-subagents',
    manifest.version,
    '.codex-plugin/plugin.json',
  );
  for (const [file, data] of [
    [local, manifest],
    [market, {name: 'personal'}],
  ]) {
    await fs.mkdir(path.dirname(file), {recursive: true});
    await fs.writeFile(file, JSON.stringify(data));
  }
  await manager.refresh();
  assert.equal(manager.state.phase, 'idle');
  await fs.mkdir(path.dirname(cached), {recursive: true});
  await fs.writeFile(cached, JSON.stringify(manifest));
  await manager.refresh();
  assert.equal(manager.state.phase, 'installed');
});

test('desktop assets are served while configuration stays authenticated', async (t) => {
  const dir = await temporary();
  const setup = await startSetup({
    htmlPath: path.join(root, 'dist/config.html'),
    configFile: path.join(dir, 'models.json'),
  });
  t.after(() => {
    setup.server.closeAllConnections();
    setup.server.close();
  });
  for (const asset of ['/desktop-ui.js', '/desktop.css']) {
    const response = await fetch(setup.origin + asset);
    assert.equal(response.status, 200);
    assert.match(
      response.headers.get('content-type'),
      asset.endsWith('.css') ? /text\/css/ : /javascript/,
    );
  }
  assert.equal((await fetch(setup.origin + '/api/config')).status, 401);
  assert.equal(
    (await fetch(setup.origin + '/desktop-ui.js', {headers: {origin: 'https://external.invalid'}}))
      .status,
    403,
  );
});

test(
  'Windows installer JSON mode keeps custom env_vars and existing model data',
  {skip: process.platform !== 'win32', timeout: 30000},
  async () => {
    const dir = await temporary();
    const profile = path.join(dir, '配置 用户');
    const data = path.join(dir, 'data');
    await fs.mkdir(data, {recursive: true});
    await fs.writeFile(path.join(data, 'models.json'), 'untouched-model-data');
    const args = [
      '-NoProfile',
      '-NonInteractive',
      '-ExecutionPolicy',
      'Bypass',
      '-File',
      path.join(root, 'scripts/install.ps1'),
      '-ProfileRoot',
      profile,
      '-NodePath',
      process.execPath,
      '-SkipCodex',
      '-Json',
    ];
    const options = {env: {...process.env, API_SUBAGENTS_HOME: data}, windowsHide: true};
    const first = JSON.parse((await execute('powershell.exe', args, options)).stdout.trim());
    assert.equal(first.installed, false);
    const mcpFile = path.join(first.pluginRoot, '.mcp.json');
    const mcp = JSON.parse(await fs.readFile(mcpFile, 'utf8'));
    mcp.mcpServers['api-subagents'].env_vars.push('MY_CUSTOM_API_KEY');
    await fs.writeFile(mcpFile, JSON.stringify(mcp));
    const second = JSON.parse((await execute('powershell.exe', args, options)).stdout.trim());
    assert.equal(second.installed, false);
    const refreshed = JSON.parse(await fs.readFile(mcpFile, 'utf8'));
    assert.ok(refreshed.mcpServers['api-subagents'].env_vars.includes('MY_CUSTOM_API_KEY'));
    assert.equal(refreshed.mcpServers['api-subagents'].command, process.execPath);
    assert.equal(await fs.readFile(path.join(data, 'models.json'), 'utf8'), 'untouched-model-data');
  },
);

test(
  'source launcher installs dependencies and builds before installation, stopping on npm failure',
  {skip: process.platform !== 'win32', timeout: 30000},
  async () => {
    const dir = await temporary();
    const runtime = path.join(dir, 'node');
    const scripts = path.join(dir, 'scripts');
    const npm = path.join(runtime, 'node_modules/npm/bin/npm-cli.js');
    for (const folder of [scripts, path.dirname(npm), path.join(dir, 'src')])
      await fs.mkdir(folder, {recursive: true});
    await fs
      .link(process.execPath, path.join(runtime, 'node.exe'))
      .catch(() => fs.copyFile(process.execPath, path.join(runtime, 'node.exe')));
    await fs.writeFile(path.join(dir, 'src/server.mjs'), '');
    await fs.copyFile(
      path.join(root, 'scripts/source-install.ps1'),
      path.join(scripts, 'source-install.ps1'),
    );
    await fs.writeFile(
      path.join(scripts, 'ensure-runtime.ps1'),
      `function Get-ProjectNode { return '${path.join(runtime, 'node.exe').replaceAll("'", "''")}' }`,
    );
    await fs.writeFile(
      npm,
      `const fs=require('fs');fs.appendFileSync('steps.txt',process.argv.slice(2).join(' ')+'\\n');if(fs.existsSync('fail-npm'))process.exit(7);`,
    );
    await fs.writeFile(
      path.join(scripts, 'install.ps1'),
      "param($ProfileRoot,$NodePath,[switch]$SkipCodex)\n[IO.File]::AppendAllText((Join-Path (Split-Path -Parent $PSScriptRoot) 'steps.txt'), 'install')",
    );
    const args = [
      '-NoProfile',
      '-NonInteractive',
      '-ExecutionPolicy',
      'Bypass',
      '-File',
      path.join(scripts, 'source-install.ps1'),
      '-ProfileRoot',
      path.join(dir, 'profile'),
      '-SkipCodex',
    ];
    await execute('powershell.exe', args, {windowsHide: true});
    assert.equal(
      await fs.readFile(path.join(dir, 'steps.txt'), 'utf8'),
      'ci --no-audit --no-fund\nrun build\ninstall',
    );
    await fs.writeFile(path.join(dir, 'steps.txt'), '');
    await fs.writeFile(path.join(dir, 'fail-npm'), '');
    await assert.rejects(
      execute('powershell.exe', args, {windowsHide: true}),
      (error) => error.code === 1,
    );
    assert.equal(await fs.readFile(path.join(dir, 'steps.txt'), 'utf8'), 'ci --no-audit --no-fund\n');
    // 已安装目录即使残留旧版源码，也应复用独立运行环境，不重新运行 npm。
    await fs.writeFile(path.join(dir, 'steps.txt'), '');
    await fs.writeFile(
      path.join(dir, 'runtime.json'),
      JSON.stringify({node: path.join(runtime, 'node.exe')}),
    );
    await execute('powershell.exe', args, {windowsHide: true});
    assert.equal(await fs.readFile(path.join(dir, 'steps.txt'), 'utf8'), 'install');
  },
);

test(
  'Windows installer reports Codex CLI success and propagates failure',
  {skip: process.platform !== 'win32', timeout: 30000},
  async () => {
    const dir = await temporary();
    const bin = path.join(dir, 'bin');
    await fs.mkdir(bin);
    await fs.writeFile(
      path.join(bin, 'codex.cmd'),
      '@echo off\r\necho %*\r\nexit /b %CODEX_TEST_EXIT%\r\n',
    );
    const args = [
      '-NoProfile',
      '-NonInteractive',
      '-ExecutionPolicy',
      'Bypass',
      '-File',
      path.join(root, 'scripts/install.ps1'),
      '-ProfileRoot',
      path.join(dir, 'profile'),
      '-NodePath',
      process.execPath,
      '-Json',
    ];
    const env = {...process.env, PATH: bin + path.delimiter + process.env.PATH, CODEX_TEST_EXIT: '0'};
    const result = JSON.parse(
      (await execute('powershell.exe', args, {env, windowsHide: true})).stdout.trim(),
    );
    assert.equal(result.installed, true);
    const pkg = JSON.parse(await fs.readFile(path.join(root, 'package.json'), 'utf8'));
    assert.equal(result.version.split('+')[0], pkg.version);
    env.CODEX_TEST_EXIT = '9';
    await assert.rejects(
      execute('powershell.exe', args, {env, windowsHide: true}),
      (error) => error.code !== 0,
    );
  },
);
