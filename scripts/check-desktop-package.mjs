import fs from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';
import asar from '@electron/asar';
import yaml from 'js-yaml';
import payload from '../desktop/payload.cjs';

const resources = path.resolve(process.argv[2] || 'release/win-unpacked', 'resources');
const archive = path.join(resources, 'app.asar');
const appFiles = new Set(['package.json', ...payload.appFiles]);
const privateFile = /(^|\/)(models\.json|runtime\.json|credentials\.json|\.env(?:\..*)?)$/i;

/** 逐个读取打包后的实际文件名，拒绝清单外的文件；目录内残留的旧配置也不能放行。 */
async function checkFiles(directory, expected) {
  const found = [];
  /** 仅遍历普通文件与目录，拒绝利用符号链接绕过打包范围。 */
  async function visit(current) {
    for (const entry of await fs.readdir(current, {withFileTypes: true})) {
      const file = path.join(current, entry.name);
      if (entry.isDirectory()) await visit(file);
      else {
        assert.ok(entry.isFile(), 'Unexpected link in desktop resources');
        found.push(path.relative(directory, file).replaceAll(path.sep, '/'));
      }
    }
  }
  await visit(directory);
  assert.deepEqual(found.sort(), [...expected].sort(), 'Packaged resources differ from the allowlist');
}

for (const entry of asar.listPackage(archive)) {
  const native = entry.slice(1);
  if (asar.statFile(archive, native).files) continue;
  const file = native.replaceAll(path.sep, '/');
  assert.ok(!privateFile.test(file), 'Local configuration must not be packaged');
  assert.ok(file.startsWith('node_modules/') || appFiles.has(file), 'Unexpected application file');
}
await checkFiles(path.join(resources, 'plugin'), payload.pluginFiles);
await checkFiles(path.join(resources, 'runtime'), payload.runtimeFiles);
await checkFiles(path.join(resources, 'licenses'), payload.licenseFiles);
const mcp = JSON.parse(await fs.readFile(path.join(resources, 'plugin/.mcp.json'), 'utf8'));
assert.deepEqual(Object.keys(mcp.mcpServers), ['api-subagents']);
assert.equal(mcp.mcpServers['api-subagents'].command, 'node');
assert.equal(mcp.mcpServers['api-subagents'].env, undefined, 'MCP credentials must not be packaged');
const update = yaml.load(await fs.readFile(path.join(resources, 'app-update.yml'), 'utf8'));
for (const field of ['token', 'headers', 'requestHeaders']) assert.equal(update[field], undefined);
console.log('Desktop package allowlist and local-configuration exclusion checks passed.');
