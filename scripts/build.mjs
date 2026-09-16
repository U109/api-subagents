import {build} from 'esbuild';
import fs from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
// 同时打包 MCP 服务与供主 Agent 使用的本地应用脚本，分发时不需要 node_modules。
await build({
  entryPoints: [path.join(root, 'src/server.mjs'), path.join(root, 'src/apply-proposals.mjs')],
  outdir: path.join(root, 'dist'),
  outExtension: {'.js': '.mjs'},
  bundle: true,
  platform: 'node',
  target: 'node20',
  format: 'esm',
  legalComments: 'eof',
  banner: {
    js: 'import { createRequire as __createRequire } from "node:module"; const require = __createRequire(import.meta.url);',
  },
});
for (const asset of ['config.html', 'config-ui.js', 'config.css']) {
  await fs.copyFile(path.join(root, 'src', asset), path.join(root, 'dist', asset));
}
