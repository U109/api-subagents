import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import http from 'node:http';
import crypto from 'node:crypto';
import {spawn, execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {fileURLToPath} from 'node:url';
import {TaskManager, Workspace, saveConfig, readConfig, publicConfig, startSetup} from '../dist/server.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const tempRoot = process.env.API_SUBAGENTS_TEST_ROOT || path.join(os.tmpdir(), 'api-subagents-tests');
const exec = promisify(execFile);
async function temporary() {
  await fs.mkdir(tempRoot, {recursive: true});
  return fs.mkdtemp(path.join(tempRoot, 'case-'));
}
async function serve(handler) {
  const server = http.createServer(async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    try {
      await handler(req, res, body ? JSON.parse(body) : null);
    } catch (e) {
      res.writeHead(500);
      res.end(e.message);
    }
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  return {
    server,
    url: `http://127.0.0.1:${server.address().port}/v1`,
    close: () =>
      new Promise((r) => {
        server.closeAllConnections();
        server.close(r);
      }),
  };
}
const reply = (res, body) => {
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify(body));
};
const profile = (protocol, baseUrl) => ({
  protocol,
  baseUrl,
  model: 'test-model',
  apiKey: 'synthetic-secret-123',
  maxTokens: 4096,
});
async function configured(dir, profiles, maxConcurrent = 3) {
  const file = path.join(dir, 'models.json');
  await saveConfig({version: 1, maxConcurrent, models: profiles}, file);
  return file;
}

test('configuration validates endpoints, keeps keys private and reports malformed JSON without echoing secrets', async () => {
  const dir = await temporary(),
    file = await configured(dir, {reviewer: profile('compatible', 'http://127.0.0.1:9999/v1')});
  const config = await readConfig(file);
  assert.equal(config.models.reviewer.apiKey, 'synthetic-secret-123');
  const publicValue = publicConfig(config);
  assert.equal(publicValue.models.reviewer.hasKey, true);
  assert.ok(!JSON.stringify(publicValue).includes('synthetic-secret'));
  await assert.rejects(() =>
    saveConfig({version: 1, models: {a: profile('compatible', 'https://user:password@example.com')}}, file),
  );
  await fs.writeFile(file, '{"apiKey":"secret-bad-value", broken}');
  await assert.rejects(
    () => readConfig(file),
    (e) => !e.message.includes('secret-bad-value') && e.message.includes('JSON'),
  );
});

test('workspace blocks traversal, secrets and symlink escapes; proposed edits never modify files', async () => {
  const dir = await temporary(),
    project = path.join(dir, 'project'),
    outside = path.join(dir, 'outside');
  await fs.mkdir(project);
  await fs.mkdir(outside);
  await fs.writeFile(path.join(project, 'app.js'), 'const value = 1;\n');
  await fs.writeFile(path.join(project, '.env'), 'PRIVATE=abc');
  await fs.writeFile(path.join(outside, 'secret.txt'), 'outside');
  await fs.symlink(outside, path.join(project, 'escape'), process.platform === 'win32' ? 'junction' : 'dir');
  const ws = await Workspace.open(project);
  for (const p of [
    '../outside/secret.txt',
    '.env',
    'escape/secret.txt',
    '../project/.env',
    'C:/Windows/file',
    '..\\outside\\secret.txt',
  ])
    await assert.rejects(() => ws.read(p));
  const read = await ws.read('app.js');
  assert.equal(read.sha256, crypto.createHash('sha256').update('const value = 1;\n').digest('hex'));
  assert.equal((await ws.search('value')).matches.length, 1);
  await ws.propose('app.js', 'const value = 2;\n', read.sha256);
  await ws.propose('new/sub.txt', 'hello', null);
  assert.equal(await fs.readFile(path.join(project, 'app.js'), 'utf8'), 'const value = 1;\n');
  await assert.rejects(() => fs.stat(path.join(project, 'new/sub.txt')));
  await assert.rejects(() => ws.propose('.git/config', 'bad', null));
  await assert.rejects(() => ws.propose('app.js', 'bad', 'outdated-sha'));
  const listed = await ws.list('.', true);
  assert.ok(!listed.entries.some((x) => x.path.includes('escape') || x.path === '.env'));
});

for (const protocol of ['compatible', 'responses', 'anthropic', 'gemini']) {
  test(`${protocol}: real HTTP multi-turn file read and proposed edit with preserved provider history`, async (t) => {
    const dir = await temporary(),
      project = path.join(dir, 'project');
    await fs.mkdir(project);
    await fs.writeFile(path.join(project, 'hello.txt'), 'before');
    let count = 0;
    const requests = [];
    const api = await serve((req, res, body) => {
      requests.push(body);
      count++;
      if (protocol === 'anthropic') assert.equal(req.headers['x-api-key'], 'synthetic-secret-123');
      else if (protocol === 'gemini') assert.equal(req.headers['x-goog-api-key'], 'synthetic-secret-123');
      else assert.equal(req.headers.authorization, 'Bearer synthetic-secret-123');
      const tool = count === 1 ? 'read_file' : 'propose_file';
      const args =
        count === 1
          ? {path: 'hello.txt'}
          : {
              path: 'hello.txt',
              content: 'after',
              expectedSha256: crypto.createHash('sha256').update('before').digest('hex'),
            };
      const final = count === 3;
      const id = 'call-' + count;
      if (protocol === 'compatible')
        reply(res, {
          choices: [
            {
              finish_reason: final ? 'stop' : 'tool_calls',
              message: final
                ? {role: 'assistant', content: 'Finished safely.'}
                : {
                    role: 'assistant',
                    content: null,
                    tool_calls: [
                      {id, type: 'function', function: {name: tool, arguments: JSON.stringify(args)}},
                    ],
                  },
            },
          ],
        });
      if (protocol === 'responses')
        reply(res, {
          status: 'completed',
          output: final
            ? [
                {
                  type: 'message',
                  role: 'assistant',
                  content: [{type: 'output_text', text: 'Finished safely.'}],
                },
              ]
            : [
                {type: 'reasoning', id: 'r-' + count, summary: [], encrypted_content: 'encrypted-' + count},
                {type: 'function_call', call_id: id, name: tool, arguments: JSON.stringify(args)},
              ],
        });
      if (protocol === 'anthropic')
        reply(res, {
          stop_reason: final ? 'end_turn' : 'tool_use',
          content: final
            ? [{type: 'text', text: 'Finished safely.'}]
            : [{type: 'tool_use', id, name: tool, input: args}],
        });
      if (protocol === 'gemini')
        reply(res, {
          candidates: [
            {
              finishReason: 'STOP',
              content: {
                role: 'model',
                parts: final
                  ? [{text: 'Finished safely.'}]
                  : [{functionCall: {id, name: tool, args}, thoughtSignature: 'signature-' + count}],
              },
            },
          ],
        });
    });
    t.after(api.close);
    const file = await configured(dir, {worker: profile(protocol, api.url)}),
      manager = new TaskManager({configFile: file, storageDir: path.join(dir, 'results')});
    t.after(() => manager.close());
    const job = await manager.submit({
      model: 'worker',
      task: 'Read hello.txt and propose after.',
      workspace: project,
    });
    const result = await manager.wait(job.task_id, 10000);
    assert.equal(result.status, 'completed', result.error);
    assert.equal(count, 3);
    assert.equal(result.toolCalls, 2);
    assert.equal(result.changesApplied, false);
    assert.equal(result.changes[0].content, 'after');
    assert.equal(await fs.readFile(path.join(project, 'hello.txt'), 'utf8'), 'before');
    assert.ok(JSON.stringify(requests[1]).includes('before'));
    if (protocol === 'gemini') assert.ok(JSON.stringify(requests[1]).includes('signature-1'));
    if (protocol === 'responses') assert.ok(JSON.stringify(requests[1]).includes('encrypted-1'));
    assert.ok(!JSON.stringify(result).includes('synthetic-secret'));
    await manager.close();
    const recovered = new TaskManager({configFile: file, storageDir: path.join(dir, 'results')});
    assert.equal((await recovered.get(job.task_id)).status, 'completed');
  });
}

test('queued tasks cancel without an API request and running task cancellation aborts fetch', async (t) => {
  const dir = await temporary();
  let requests = 0;
  const api = await serve(() => {
    requests++;
  });
  t.after(api.close);
  const file = await configured(dir, {worker: profile('compatible', api.url)}, 1);
  const manager = new TaskManager({configFile: file, storageDir: path.join(dir, 'results')});
  t.after(() => manager.close());
  const a = await manager.submit({model: 'worker', task: 'one', workspace: dir});
  const b = await manager.submit({model: 'worker', task: 'two', workspace: dir});
  assert.equal(b.status, 'queued');
  await manager.cancel(b.task_id);
  assert.equal((await manager.get(b.task_id)).status, 'cancelled');
  await new Promise((r) => setTimeout(r, 100));
  assert.equal(requests, 1);
  await manager.cancel(a.task_id);
  assert.equal((await manager.wait(a.task_id, 5000)).status, 'cancelled');
});

test('step limits fail explicitly and HTTP errors do not echo response bodies', async (t) => {
  const dir = await temporary();
  let code = 200;
  const api = await serve((req, res) => {
    if (code === 401) {
      res.writeHead(401);
      res.end('synthetic-secret-123');
      return;
    }
    reply(res, {
      choices: [
        {
          finish_reason: 'tool_calls',
          message: {
            role: 'assistant',
            tool_calls: [{id: 'c', type: 'function', function: {name: 'unknown_tool', arguments: '{}'}}],
          },
        },
      ],
    });
  });
  t.after(api.close);
  const file = await configured(dir, {worker: profile('compatible', api.url)}),
    manager = new TaskManager({configFile: file, storageDir: path.join(dir, 'results')});
  t.after(() => manager.close());
  const job = await manager.submit({model: 'worker', task: 'task', workspace: dir, max_steps: 1});
  assert.equal((await manager.wait(job.task_id, 5000)).status, 'failed');
  code = 401;
  const next = await manager.submit({model: 'worker', task: 'task', workspace: dir});
  const result = await manager.wait(next.task_id, 5000);
  assert.equal(result.status, 'failed');
  assert.match(result.error, /401/);
  assert.ok(!JSON.stringify(result).includes('synthetic-secret'));
});

test('continuation carries prior context only for the same model and project', async (t) => {
  const dir = await temporary();
  const captured = [];
  const api = await serve((req, res, body) => {
    captured.push(body);
    reply(res, {choices: [{finish_reason: 'stop', message: {role: 'assistant', content: 'First response'}}]});
  });
  t.after(api.close);
  const file = await configured(dir, {
      worker: profile('compatible', api.url),
      other: profile('compatible', api.url),
    }),
    manager = new TaskManager({configFile: file, storageDir: path.join(dir, 'results')});
  t.after(() => manager.close());
  const a = await manager.submit({model: 'worker', task: 'First task', workspace: dir});
  await manager.wait(a.task_id, 5000);
  const b = await manager.submit({
    model: 'worker',
    task: 'Follow up',
    workspace: dir,
    continuation_id: a.task_id,
  });
  await manager.wait(b.task_id, 5000);
  assert.ok(JSON.stringify(captured[1]).includes('First response'));
  await assert.rejects(() =>
    manager.submit({model: 'other', task: 'bad', workspace: dir, continuation_id: a.task_id}),
  );
});

test('configuration page requires its token, rejects cross-origin writes, preserves saved keys', async (t) => {
  const dir = await temporary(),
    file = await configured(dir, {worker: profile('compatible', 'http://127.0.0.1:9999/v1')});
  const setup = await startSetup({
    htmlPath: path.join(root, 'dist/config.html'),
    configFile: file,
    probeImpl: async (p) => ({ok: p.apiKey === 'synthetic-secret-123', reply: 'OK'}),
  });
  t.after(
    () =>
      new Promise((r) => {
        setup.server.closeAllConnections();
        setup.server.close(r);
      }),
  );
  assert.equal((await fetch(setup.origin + '/api/config')).status, 401);
  const headers = {authorization: 'Bearer ' + setup.token, 'content-type': 'application/json'};
  const data = await (await fetch(setup.origin + '/api/config', {headers})).json();
  assert.ok(!JSON.stringify(data).includes('synthetic-secret'));
  assert.equal(
    (
      await fetch(setup.origin + '/api/config', {
        method: 'POST',
        headers: {...headers, origin: 'https://evil.invalid'},
        body: JSON.stringify({config: data.config}),
      })
    ).status,
    403,
  );
  const save = await fetch(setup.origin + '/api/config', {
    method: 'POST',
    headers,
    body: JSON.stringify({config: data.config}),
  });
  assert.equal(save.status, 200);
  assert.equal((await readConfig(file)).models.worker.apiKey, 'synthetic-secret-123');
  const check = await (
    await fetch(setup.origin + '/api/probe', {
      method: 'POST',
      headers,
      body: JSON.stringify({config: data.config, name: 'worker'}),
    })
  ).json();
  assert.equal(check.ok, true);
  const changed = structuredClone(data.config);
  changed.models.worker.baseUrl = 'https://another-service.invalid/v1';
  const rejected = await fetch(setup.origin + '/api/probe', {
    method: 'POST',
    headers,
    body: JSON.stringify({config: changed, name: 'worker'}),
  });
  assert.equal(rejected.status, 400);
  assert.match((await rejected.json()).error, /重新填写 Key/);
});

test('bundled MCP server initializes, lists tools, and handles calls through stdio', async (t) => {
  const dir = await temporary();
  const child = spawn(process.execPath, [path.join(root, 'dist/server.mjs')], {
    cwd: root,
    env: {...process.env, API_SUBAGENTS_HOME: dir},
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  t.after(() => child.kill());
  let buffer = '',
    next = 0;
  const pending = new Map();
  let stderr = '';
  child.stderr.on('data', (d) => (stderr += d));
  child.stdout.on('data', (chunk) => {
    buffer += chunk;
    let newline;
    while ((newline = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newline);
      buffer = buffer.slice(newline + 1);
      if (!line.trim()) continue;
      const response = JSON.parse(line);
      pending.get(response.id)?.(response);
      pending.delete(response.id);
    }
  });
  const call = (method, params = {}) =>
    new Promise((resolve, reject) => {
      const id = ++next;
      const timeout = setTimeout(() => reject(Error('MCP timeout: ' + stderr)), 5000);
      pending.set(id, (response) => {
        clearTimeout(timeout);
        resolve(response);
      });
      child.stdin.write(JSON.stringify({jsonrpc: '2.0', id, method, params}) + '\n');
    });
  const init = await call('initialize', {
    protocolVersion: '2024-11-05',
    capabilities: {},
    clientInfo: {name: 'test', version: '1'},
  });
  assert.equal(init.result.serverInfo.name, 'api-subagents');
  child.stdin.write(JSON.stringify({jsonrpc: '2.0', method: 'notifications/initialized'}) + '\n');
  const list = await call('tools/list');
  assert.equal(list.result.tools.length, 6);
  const models = await call('tools/call', {name: 'list_models', arguments: {}});
  assert.deepEqual(JSON.parse(models.result.content[0].text).models, []);
  const missing = await call('tools/call', {
    name: 'delegate_task',
    arguments: {model: 'missing', task: 'read', workspace: dir},
  });
  assert.equal(missing.result.isError, true);
  child.stdin.end();
});

test(
  'Windows installer preserves an existing marketplace and supports repeat installation',
  {skip: process.platform !== 'win32', timeout: 30000},
  async () => {
    const dir = await temporary(),
      profileRoot = path.join(dir, 'profile'),
      market = path.join(profileRoot, '.agents/plugins/marketplace.json');
    await fs.mkdir(path.dirname(market), {recursive: true});
    await fs.writeFile(
      market,
      JSON.stringify({
        name: 'my-personal',
        interface: {displayName: '我的目录'},
        plugins: [
          {
            name: 'existing',
            source: {source: 'local', path: './plugins/existing'},
            policy: {installation: 'AVAILABLE', authentication: 'ON_INSTALL'},
            category: 'Productivity',
          },
        ],
      }),
    );
    const args = [
      '-NoProfile',
      '-ExecutionPolicy',
      'Bypass',
      '-File',
      path.join(root, 'scripts/install.ps1'),
      '-ProfileRoot',
      profileRoot,
      '-SkipCodex',
    ];
    await exec('powershell.exe', args);
    await exec('powershell.exe', args);
    const catalog = JSON.parse(await fs.readFile(market, 'utf8'));
    assert.equal(catalog.name, 'my-personal');
    assert.equal(catalog.interface.displayName, '我的目录');
    assert.equal(catalog.plugins.length, 2);
    const mcp = JSON.parse(
      await fs.readFile(path.join(profileRoot, 'plugins/api-subagents/.mcp.json'), 'utf8'),
    );
    assert.ok(path.isAbsolute(mcp.mcpServers['api-subagents'].command));
    for (const file of [
      'dist/server.mjs',
      'dist/config.html',
      'dist/config-ui.js',
      'dist/config.css',
      'dist/apply-proposals.mjs',
      'README.md',
      'THIRD-PARTY-NOTICES.txt',
    ]) {
      assert.ok(await fs.stat(path.join(profileRoot, 'plugins/api-subagents', file)));
    }
  },
);
