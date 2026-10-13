import {build} from 'esbuild';
import {spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {mkdir, readFile, readdir, writeFile} from 'node:fs/promises';
import path from 'node:path';

// 构建只读取已入库的源码和锁定依赖，产物为 Go 白名单嵌入的两个静态文件，不包含本机配置。
const root = path.dirname(fileURLToPath(import.meta.url));
const result = await build({absWorkingDir: root, entryPoints: ['src/app.jsx'], outfile: 'workbench-app.js', bundle: true, minify: true, target: ['chrome110'], jsx: 'automatic', define: {'process.env.NODE_ENV': '"production"'}, legalComments: 'eof', metafile: true});
// 只规范 esbuild 末尾许可证注释的行尾空白，保留完整许可文字，不改写生成代码或字符串。
const scriptPath = path.join(root, 'workbench-app.js'), script = await readFile(scriptPath, 'utf8'), licenseStart = script.lastIndexOf('/*! Bundled license information:');
if (licenseStart >= 0) await writeFile(scriptPath, script.slice(0, licenseStart) + script.slice(licenseStart).replace(/[\t ]+$/gm, '').trimEnd() + '\n');
const css = spawnSync(process.execPath, [path.join(root, 'node_modules/@tailwindcss/cli/dist/index.mjs'), '-i', 'src/styles.css', '-o', 'workbench-app.css', '--minify'], {cwd: root, stdio: 'inherit'});
if (css.status !== 0) process.exit(css.status || 1);
// 按顺序追加页面样式；共用接入配置的视觉令牌，最后补齐其他工作区页面，不引入原型脚本或本机资源。
const additions = await Promise.all(['usage.css', 'workbench-master.css', 'review-polish.css', 'reference-labels.css', 'macos-settings.css', 'macos-workspace.css'].map(name => readFile(path.join(root, 'src', name), 'utf8')));
await writeFile(path.join(root, 'workbench-app.css'), (await readFile(path.join(root, 'workbench-app.css'), 'utf8')) + '\n' + additions.join('\n'));

// packageForInput 向上查找实际入包源码的 npm 包根目录；不把开发工具或包内无名称的配置误认为运行依赖。
async function packageForInput(input) {
  const modules = path.join(root, 'node_modules');
  let directory = path.dirname(path.resolve(root, input));
  while (directory.startsWith(modules + path.sep)) {
    try {
      const manifest = JSON.parse(await readFile(path.join(directory, 'package.json'), 'utf8'));
      if (manifest.name && manifest.version) return {directory, name: manifest.name, version: manifest.version};
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
    directory = path.dirname(directory);
  }
  throw new Error(`无法识别前端运行依赖：${input}`);
}

// runtimeNotices 从真实打包输入收集完整许可证并按包名稳定排序，缺少声明时阻止分发，不输出工具依赖或本机路径。
async function runtimeNotices(inputs) {
  const packages = new Map();
  for (const input of [...inputs.filter(input => input.replaceAll('\\', '/').startsWith('node_modules/')), 'node_modules/tailwindcss/dist/lib.mjs']) {
    const item = await packageForInput(input);
    packages.set(`${item.name}@${item.version}`, item);
  }
  const sections = ['API Subagents — bundled frontend runtime licenses\n'];
  for (const [key, item] of [...packages].sort(([a], [b]) => a.localeCompare(b, 'en'))) {
    const filenames = (await readdir(item.directory, {withFileTypes: true}))
      .filter(entry => entry.isFile() && /^(licen[cs]e|copying|notice)(?:[.-]|$)/i.test(entry.name))
      .map(entry => entry.name).sort();
    // 个别 npm 发布包遗漏 LICENSE，只允许复用已核对且与包版本绑定的上游完整声明；不在构建时联网补取。
    if (!filenames.length) {
      const fallback = path.join(root, 'licenses', `${key.replaceAll('/', '__')}.txt`);
      try {
        sections.push(`===== ${key} / verified upstream license =====\n${(await readFile(fallback, 'utf8')).trimEnd()}\n`);
      } catch (error) {
        if (error.code === 'ENOENT') throw new Error(`前端运行依赖缺少完整许可证：${key}`);
        throw error;
      }
    }
    for (const filename of filenames) sections.push(`===== ${key} / ${filename} =====\n${(await readFile(path.join(item.directory, filename), 'utf8')).trimEnd()}\n`);
  }
  await mkdir(path.join(root, 'licenses'), {recursive: true});
  await writeFile(path.join(root, 'licenses/workbench-runtime.txt'), sections.join('\n'));
}

await runtimeNotices(Object.keys(result.metafile.inputs));
