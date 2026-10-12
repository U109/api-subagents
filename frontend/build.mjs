import {build} from 'esbuild';
import {spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {readFile, writeFile} from 'node:fs/promises';
import path from 'node:path';

// 构建只读取已入库的源码和锁定依赖，产物为 Go 白名单嵌入的两个静态文件，不包含本机配置。
const root = path.dirname(fileURLToPath(import.meta.url));
await build({absWorkingDir: root, entryPoints: ['src/app.jsx'], outfile: 'workbench-app.js', bundle: true, minify: true, target: ['chrome110'], jsx: 'automatic', define: {'process.env.NODE_ENV': '"production"'}, legalComments: 'eof'});
// 只规范 esbuild 末尾许可证注释的行尾空白，保留完整许可文字，不改写生成代码或字符串。
const scriptPath = path.join(root, 'workbench-app.js'), script = await readFile(scriptPath, 'utf8'), licenseStart = script.lastIndexOf('/*! Bundled license information:');
if (licenseStart >= 0) await writeFile(scriptPath, script.slice(0, licenseStart) + script.slice(licenseStart).replace(/[\t ]+$/gm, '').trimEnd() + '\n');
const css = spawnSync(process.execPath, [path.join(root, 'node_modules/@tailwindcss/cli/dist/index.mjs'), '-i', 'src/styles.css', '-o', 'workbench-app.css', '--minify'], {cwd: root, stdio: 'inherit'});
if (css.status !== 0) process.exit(css.status || 1);
