import fs from 'node:fs/promises';
import path from 'node:path';

/** 按 Node 的逐级查找规则定位依赖目录，兼容 npm 为冲突版本生成的嵌套依赖。 */
async function dependencyDirectory(name, parent) {
  let current = parent;
  while (true) {
    const candidate = path.join(current, 'node_modules', name);
    try {
      await fs.access(path.join(candidate, 'package.json'));
      return candidate;
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
    const next = path.dirname(current);
    if (next === current) throw new Error(`Missing dependency license source: ${name}`);
    current = next;
  }
}

/** 收集实际随桌面应用分发的依赖许可；Electron、Chromium 和 Node 的完整许可另存为可直接阅读的文件。 */
export async function writeDesktopNotices(root, resources) {
  const licenses = path.join(resources, 'licenses');
  await fs.mkdir(licenses, {recursive: true});
  for (const [source, name] of [
    [path.join(root, 'node_modules/electron/dist/LICENSE'), 'Electron-LICENSE.txt'],
    [path.join(root, 'node_modules/electron/dist/LICENSES.chromium.html'), 'Chromium-LICENSES.html'],
    [path.join(resources, 'runtime/LICENSE'), 'Node-LICENSE.txt'],
  ])
    await fs.copyFile(source, path.join(licenses, name));
  const visited = new Set();
  const notices = ['API Subagents desktop dependency notices\n'];
  /** 递归记录生产依赖，保留每个包的版权原文；构建工具不进入分发清单。 */
  async function visit(name, parent) {
    const directory = await dependencyDirectory(name, parent);
    if (visited.has(directory)) return;
    visited.add(directory);
    const pkg = JSON.parse(await fs.readFile(path.join(directory, 'package.json'), 'utf8'));
    notices.push(
      `\n${'='.repeat(70)}\n${pkg.name} ${pkg.version}\nLicense: ${pkg.license || 'See package source'}\nSource: https://www.npmjs.com/package/${pkg.name}\n`,
    );
    const files = (await fs.readdir(directory)).filter((file) =>
      /^(licen[cs]e|copying|notice)(\.|$)/i.test(file),
    );
    for (const file of files) {
      if ((await fs.stat(path.join(directory, file))).isFile())
        notices.push(await fs.readFile(path.join(directory, file), 'utf8'));
    }
    for (const dependency of Object.keys(pkg.dependencies || {})) await visit(dependency, directory);
  }
  const pkg = JSON.parse(await fs.readFile(path.join(root, 'package.json'), 'utf8'));
  for (const name of Object.keys(pkg.dependencies)) await visit(name, root);
  await fs.writeFile(path.join(licenses, 'Dependency-LICENSES.txt'), notices.join('\n'));
}
