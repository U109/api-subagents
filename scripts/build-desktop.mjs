import {build} from 'esbuild';
import fs from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {writeDesktopNotices} from './desktop-notices.mjs';

const execute = promisify(execFile);
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
if (process.platform !== 'win32') throw new Error('桌面安装包目前需要在 Windows 上构建。');
const resources = path.join(root, '.desktop-resources');
const plugin = path.join(resources, 'plugin');
const runtime = path.join(resources, 'runtime');
await fs.mkdir(plugin, {recursive: true});
await fs.mkdir(runtime, {recursive: true});

// Electron 44 起不再依赖 npm 的安装钩子；显式准备二进制，使全新克隆也能一键打包。
try {
  await fs.access(path.join(root, 'node_modules', 'electron', 'dist', 'electron.exe'));
} catch {
  console.log('Preparing Electron runtime...');
  await execute(process.execPath, [path.join(root, 'node_modules', 'electron', 'install.js')], {
    cwd: root,
    windowsHide: true,
    timeout: 600000,
    maxBuffer: 1024 * 1024,
  });
}

// 使用与源码安装相同的校验下载逻辑，桌面包随附独立 Node，不依赖用户系统环境。
console.log('Preparing verified standalone Node.js...');
const {stdout} = await execute(
  'powershell.exe',
  [
    '-NoProfile',
    '-NonInteractive',
    '-ExecutionPolicy',
    'Bypass',
    '-File',
    path.join(root, 'scripts', 'prepare-runtime.ps1'),
  ],
  {windowsHide: true, timeout: 600000, maxBuffer: 1024 * 1024},
);
const node = JSON.parse(stdout.trim()).node;
await fs.copyFile(node, path.join(runtime, 'node.exe'));
await fs.copyFile(path.join(path.dirname(node), 'LICENSE'), path.join(runtime, 'LICENSE'));
await writeDesktopNotices(root, resources);

for (const item of [
  '.codex-plugin',
  '.mcp.json',
  'dist/server.mjs',
  'dist/apply-proposals.mjs',
  'dist/config.html',
  'dist/config-ui.js',
  'dist/config.css',
  'dist/desktop-ui.js',
  'dist/desktop.css',
  'skills',
  'scripts',
  'desktop/release.json',
  'Configure.cmd',
  'Install.cmd',
  'README.md',
  'THIRD-PARTY-NOTICES.txt',
]) {
  const destination = path.join(plugin, item);
  await fs.mkdir(path.dirname(destination), {recursive: true});
  await fs.cp(path.join(root, item), destination, {recursive: true});
}
await build({
  entryPoints: [path.join(root, 'src', 'desktop', 'main.mjs')],
  outfile: path.join(root, 'dist-desktop', 'main.cjs'),
  bundle: true,
  platform: 'node',
  target: 'node22',
  format: 'cjs',
  external: ['electron', 'electron-updater'],
  legalComments: 'eof',
});
await fs.copyFile(
  path.join(root, 'src', 'desktop', 'preload.cjs'),
  path.join(root, 'dist-desktop', 'preload.cjs'),
);
await fs.copyFile(path.join(root, 'desktop', 'icon.ico'), path.join(root, 'dist-desktop', 'icon.ico'));
console.log('Desktop code and bundled plugin/runtime are ready.');
