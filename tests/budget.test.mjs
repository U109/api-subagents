import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import crypto from 'node:crypto';
import http from 'node:http';
import {spawn, execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {TaskManager, Workspace, saveConfig} from '../dist/server.mjs';
import {applyProposals} from '../dist/apply-proposals.mjs';

/** 为建议应用与返回体测试创建隔离项目，不读取真实模型或密钥。 */
async function fixture(t) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'api-subagents-budget-'));
  const workspace = path.join(root, 'project');
  const storageDir = path.join(root, 'tasks');
  await fs.mkdir(workspace);
  await fs.mkdir(storageDir);
  const configFile = path.join(root, 'models.json');
  await saveConfig(
    {
      version: 1,
      models: {
        worker: {
          protocol: 'compatible',
          baseUrl: 'https://offline.invalid/v1',
          model: 'fixture',
          apiKey: 'synthetic-budget-key',
        },
      },
    },
    configFile,
  );
  const manager = new TaskManager({configFile, storageDir});
  t.after(() => manager.close());
  return {root, workspace, storageDir, configFile, manager, ws: await Workspace.open(workspace)};
}

/** 写入与生产任务结果同形状的本地记录，便于独立检验主 Agent 的应用工具。 */
async function record(f, changes, overrides = {}) {
  const value = {
    task_id: crypto.randomUUID(),
    workspace: f.workspace,
    status: 'completed',
    result: 'Done',
    changes,
    changesApplied: false,
    ...overrides,
  };
  await fs.writeFile(path.join(f.storageDir, value.task_id + '.json'), JSON.stringify(value));
  return value;
}

test('small edits keep full source out of compact results and apply without parent copying', async (t) => {
  const f = await fixture(t);
  const before = '// unchanged source line\n'.repeat(6000) + 'const value = 1;\n';
  await fs.writeFile(path.join(f.workspace, 'large.js'), before);
  const original = await f.ws.read('large.js');
  const edits = [{oldText: 'const value = 1;', newText: 'const value = 2;'}];
  await f.ws.proposeEdit('large.js', edits, original.sha256);
  assert.equal(await fs.readFile(path.join(f.workspace, 'large.js'), 'utf8'), before);
  const full = await record(f, [...f.ws.proposals.values()]);
  const compact = f.manager.present(full);
  assert.deepEqual(compact.changes[0].edits, edits);
  assert.equal(compact.changes[0].content, undefined);
  assert.equal(
    f.manager.present(full, 'full').changes[0].content,
    before.replace('value = 1', 'value = 2'),
  );
  const fullBytes = Buffer.byteLength(JSON.stringify(full));
  const compactBytes = Buffer.byteLength(JSON.stringify(compact));
  assert.ok(compactBytes < fullBytes / 20, `${compactBytes} vs ${fullBytes}`);
  console.log(
    `Return payload fixture: full=${fullBytes} bytes, compact=${compactBytes} bytes (not a Codex token measurement)`,
  );
  const result = await applyProposals(full.task_id, f.workspace, [], f.storageDir);
  assert.deepEqual(result.applied, ['large.js']);
  assert.equal(
    await fs.readFile(path.join(f.workspace, 'large.js'), 'utf8'),
    before.replace('value = 1', 'value = 2'),
  );
});

test('precise replacements reject ambiguous text and stale hashes without staging changes', async (t) => {
  const f = await fixture(t);
  await fs.writeFile(path.join(f.workspace, 'a.txt'), 'repeat repeat');
  const original = await f.ws.read('a.txt');
  await assert.rejects(
    () => f.ws.proposeEdit('a.txt', [{oldText: 'repeat', newText: 'new'}], original.sha256),
    /唯一/,
  );
  await assert.rejects(
    () => f.ws.proposeEdit('a.txt', [{oldText: 'repeat repeat', newText: 'new'}], 'outdated'),
    /版本/,
  );
  assert.equal(f.ws.proposals.size, 0);
});

test('compact results explicitly omit long reports and large edits but retain complete local data', async (t) => {
  const f = await fixture(t);
  const full = await record(
    f,
    [
      {
        path: 'new.txt',
        originalSha256: null,
        content: 'x'.repeat(10000),
        edits: [{oldText: 'a', newText: 'x'.repeat(10000)}],
      },
    ],
    {result: '结论'.repeat(3000)},
  );
  const compact = f.manager.present(full);
  assert.equal(compact.result.length, 2000);
  assert.equal(compact.resultTruncated, true);
  assert.equal(compact.changes[0].contentOmitted, true);
  assert.equal(compact.changes[0].edits, undefined);
  assert.deepEqual(JSON.parse(await fs.readFile(compact.resultFile, 'utf8')), full);
  const warning = f.manager.present({...full, storageWarning: 'unavailable'});
  assert.equal(warning.resultFile, undefined);
  assert.equal(warning.applyCommand, undefined);
});

test('all selected hashes are checked before any file is overwritten', async (t) => {
  const f = await fixture(t);
  for (const name of ['a.txt', 'b.txt']) {
    await fs.writeFile(path.join(f.workspace, name), 'before');
    await f.ws.propose(name, 'after', (await f.ws.read(name)).sha256);
  }
  const job = await record(f, [...f.ws.proposals.values()]);
  await fs.writeFile(path.join(f.workspace, 'b.txt'), 'user edit');
  await assert.rejects(() => applyProposals(job.task_id, f.workspace, [], f.storageDir), /版本/);
  assert.equal(await fs.readFile(path.join(f.workspace, 'a.txt'), 'utf8'), 'before');
  assert.equal(await fs.readFile(path.join(f.workspace, 'b.txt'), 'utf8'), 'user edit');
});

test('application rejects redaction, wrong workspace, failed tasks and secret paths', async (t) => {
  const f = await fixture(t);
  for (const [change, overrides, pattern] of [
    [{path: 'a.txt', content: '[REDACTED]', originalSha256: null, redacted: true}, {}, /脱敏/],
    [
      {path: 'a.txt', content: 'new', originalSha256: null},
      {workspace: path.dirname(f.workspace)},
      /指定项目/,
    ],
    [{path: 'a.txt', content: 'new', originalSha256: null}, {status: 'failed'}, /已完成/],
    [{path: '../outside.txt', content: 'new', originalSha256: null}, {}, /路径/],
    [{path: '.env', content: 'new', originalSha256: null}, {}, /路径/],
  ]) {
    const job = await record(f, [change], overrides);
    await assert.rejects(() => applyProposals(job.task_id, f.workspace, [], f.storageDir), pattern);
  }
  assert.deepEqual(await fs.readdir(f.workspace), []);
});

test('bundled application command supports reviewed path selection and refuses repeat overwrite', async (t) => {
  const f = await fixture(t);
  const job = await record(
    f,
    ['one', 'two'].map((name) => ({path: `nested/${name}.txt`, content: name, originalSha256: null})),
  );
  const compact = f.manager.present(job);
  const [command, ...args] = compact.applyCommand;
  const {stdout} = await promisify(execFile)(command, [...args, 'nested/one.txt'], {
    env: {...process.env, API_SUBAGENTS_HOME: f.root},
    windowsHide: true,
  });
  assert.deepEqual(JSON.parse(stdout).applied, ['nested/one.txt']);
  assert.equal(await fs.readFile(path.join(f.workspace, 'nested/one.txt'), 'utf8'), 'one');
  await assert.rejects(() => fs.stat(path.join(f.workspace, 'nested/two.txt')), {code: 'ENOENT'});
  await assert.rejects(
    () => applyProposals(job.task_id, f.workspace, ['nested/one.txt'], f.storageDir),
    /版本/,
  );
});

test('compact status never advertises an apply command before persistence finishes', async (t) => {
  const f = await fixture(t);
  let release;
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  t.after(release);
  f.manager.fetchImpl = async () =>
    Response.json({choices: [{message: {role: 'assistant', content: 'done'}}]});
  f.manager.persist = () => gate;
  const job = await f.manager.submit({model: 'worker', task: 'test', workspace: f.workspace});
  await new Promise((resolve) => setImmediate(resolve));
  const compact = f.manager.present(await f.manager.get(job.task_id));
  assert.equal(compact.status, 'running');
  assert.equal(compact.applyCommand, undefined);
  release();
  assert.equal((await f.manager.wait(job.task_id, 2000)).status, 'completed');
});

test('MCP delegation returns a quick result in one call with compact defaults', async (t) => {
  const f = await fixture(t);
  const api = http.createServer((req, res) => {
    req.resume();
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({choices: [{message: {role: 'assistant', content: 'short answer'}}]}));
  });
  await new Promise((resolve) => api.listen(0, '127.0.0.1', resolve));
  t.after(
    () =>
      new Promise((resolve) => {
        api.closeAllConnections();
        api.close(resolve);
      }),
  );
  await saveConfig(
    {
      version: 1,
      models: {
        worker: {
          protocol: 'compatible',
          baseUrl: `http://127.0.0.1:${api.address().port}/v1`,
          model: 'mock',
        },
      },
    },
    f.configFile,
  );
  const child = spawn(process.execPath, [path.resolve('dist/server.mjs')], {
    env: {...process.env, API_SUBAGENTS_HOME: f.root},
    stdio: ['pipe', 'pipe', 'pipe'],
    windowsHide: true,
  });
  t.after(() => child.kill());
  let buffer = '',
    next = 0;
  const pending = new Map();
  child.stdout.on('data', (chunk) => {
    buffer += chunk;
    let end;
    while ((end = buffer.indexOf('\n')) >= 0) {
      const message = JSON.parse(buffer.slice(0, end));
      buffer = buffer.slice(end + 1);
      pending.get(message.id)?.(message);
      pending.delete(message.id);
    }
  });
  /** 发出一个 JSON-RPC 请求并限制测试等待，避免子进程异常导致测试挂起。 */
  const call = (method, params) =>
    new Promise((resolve, reject) => {
      const id = ++next;
      const timer = setTimeout(() => reject(new Error('MCP fixture timeout')), 5000);
      pending.set(id, (message) => {
        clearTimeout(timer);
        resolve(message);
      });
      child.stdin.write(JSON.stringify({jsonrpc: '2.0', id, method, params}) + '\n');
    });
  await call('initialize', {
    protocolVersion: '2024-11-05',
    capabilities: {},
    clientInfo: {name: 'budget-test', version: '1'},
  });
  child.stdin.write(JSON.stringify({jsonrpc: '2.0', method: 'notifications/initialized'}) + '\n');
  const response = await call('tools/call', {
    name: 'delegate_task',
    arguments: {model: 'worker', task: 'test', workspace: f.workspace},
  });
  const result = JSON.parse(response.result.content[0].text);
  assert.equal(result.status, 'completed');
  assert.equal(result.result, 'short answer');
  assert.equal(result.createdAt, undefined);
  child.stdin.end();
});
