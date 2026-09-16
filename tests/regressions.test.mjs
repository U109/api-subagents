import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import http from 'node:http';
import {fileURLToPath} from 'node:url';
import {
  TaskManager,
  Workspace,
  saveConfig,
  readConfig,
  publicConfig,
  startSetup,
} from '../dist/server.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const deferred = () => {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return {promise, resolve};
};
const model = (overrides) => ({
  protocol: 'compatible',
  baseUrl: 'http://127.0.0.1:9999/v1',
  model: 'test-model',
  apiKey: 'synthetic-key',
  maxTokens: 4096,
  ...overrides,
});
const response = (message) => new Response(JSON.stringify({choices: [{message}]}));
const blockedFetch = (_, {signal}) =>
  new Promise((resolve, reject) => {
    if (signal.aborted) reject(signal.reason);
    else signal.addEventListener('abort', () => reject(signal.reason), {once: true});
  });

async function fixture(t, overrides = {}, fetchImpl = blockedFetch) {
  const parent = process.env.API_SUBAGENTS_TEST_ROOT || path.join(os.tmpdir(), 'api-subagents-tests');
  await fs.mkdir(parent, {recursive: true});
  const dir = await fs.mkdtemp(path.join(parent, 'regression-'));
  const configFile = path.join(dir, 'models.json');
  await saveConfig({version: 1, maxConcurrent: 1, models: {worker: model(overrides)}}, configFile);
  const manager = new TaskManager({configFile, storageDir: path.join(dir, 'results'), fetchImpl});
  t.after(() => manager.close());
  return {
    dir,
    configFile,
    manager,
    submit: () => manager.submit({model: 'worker', task: 'Bounded test task', workspace: dir}),
  };
}

async function configurationServer(t, options = {}) {
  const f = await fixture(t, options.profile);
  const ui = await startSetup({
    htmlPath: path.join(root, 'dist/config.html'),
    configFile: f.configFile,
    probeImpl: async () => ({ok: true, reply: 'OK'}),
    ...options,
  });
  t.after(
    () =>
      new Promise((resolve) => {
        ui.server.closeAllConnections();
        ui.server.close(resolve);
      }),
  );
  const headers = {authorization: `Bearer ${ui.token}`, 'content-type': 'application/json'};
  const post = (route, body) =>
    fetch(ui.origin + route, {method: 'POST', headers, body: JSON.stringify(body)});
  const {config} = await (await fetch(ui.origin + '/api/config', {headers})).json();
  return {...f, ...ui, headers, post, config};
}

test('parallel submissions cannot exceed 24 active tasks', async (t) => {
  const {submit} = await fixture(t);
  const results = await Promise.allSettled(Array.from({length: 30}, submit));
  assert.equal(results.filter((item) => item.status === 'fulfilled').length, 24);
  assert.equal(results.filter((item) => item.status === 'rejected').length, 6);
});

test('shutdown rejects a submission still preparing its workspace', async (t) => {
  const {manager, submit} = await fixture(t);
  const submitting = submit();
  const rejected = assert.rejects(submitting, /关闭/);
  await manager.close();
  await rejected;
  assert.equal(manager.jobs.size, 0);
});

test('a late API response cannot turn a cancelled task into success', async (t) => {
  const api = deferred();
  t.after(() => api.resolve(response({role: 'assistant', content: 'Late result'})));
  const {manager, submit} = await fixture(t, {}, () => api.promise);
  const job = await submit();
  await manager.cancel(job.task_id);
  api.resolve(response({role: 'assistant', content: 'Late result'}));
  assert.equal((await manager.wait(job.task_id, 2000)).status, 'cancelled');
});

for (const apiKey of ['content', 'demo"\\token']) {
  test(`proposal redaction preserves JSON fields and handles ${JSON.stringify(apiKey)}`, async (t) => {
    let calls = 0;
    const fetchImpl = async () =>
      response(
        ++calls === 1
          ? {
              role: 'assistant',
              tool_calls: [
                {
                  id: 'proposal',
                  type: 'function',
                  function: {
                    name: 'propose_file',
                    arguments: JSON.stringify({path: 'new.txt', content: apiKey, expectedSha256: null}),
                  },
                },
              ],
            }
          : {role: 'assistant', content: 'Done'},
      );
    const {manager, submit} = await fixture(t, {apiKey}, fetchImpl);
    const job = await submit();
    const result = await manager.wait(job.task_id, 2000);
    assert.equal(result.status, 'completed', result.error);
    assert.deepEqual(result.changes, [
      {path: 'new.txt', originalSha256: null, content: '[REDACTED]', redacted: true},
    ]);
  });
}

test('queued cancellation still succeeds when result storage is unavailable', async (t) => {
  const {manager, submit, dir} = await fixture(t);
  const blocked = path.join(dir, 'not-a-directory');
  await fs.writeFile(blocked, 'file');
  manager.storageDir = blocked;
  await submit();
  const queued = await submit();
  const result = await manager.cancel(queued.task_id);
  assert.equal(result.status, 'cancelled');
  assert.match(result.storageWarning, /无法写入/);
});

test('wait resolves after result persistence, not before finalization', async (t) => {
  const stored = deferred(),
    entered = deferred();
  t.after(stored.resolve);
  const {manager, submit} = await fixture(t, {}, async () =>
    response({role: 'assistant', content: 'Done'}),
  );
  manager.persist = async () => {
    entered.resolve();
    await stored.promise;
  };
  const job = await submit();
  await entered.promise;
  let resolved = false;
  const waiting = manager.wait(job.task_id, 2000).then((value) => {
    resolved = true;
    return value;
  });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(resolved, false);
  stored.resolve();
  assert.equal((await waiting).status, 'completed');
});

test('model list reflects a missing environment override even with a saved key', async (t) => {
  const apiKeyEnv = `API_SUBAGENTS_MISSING_${process.pid}`;
  assert.equal(process.env[apiKeyEnv], undefined);
  const {manager} = await fixture(t, {apiKeyEnv});
  assert.equal((await manager.listModels())[0].keyConfigured, false);
});

test('invalid configuration request JSON does not echo its contents', async (t) => {
  const {origin, headers} = await configurationServer(t);
  const res = await fetch(origin + '/api/config', {method: 'POST', headers, body: 'synthetic-secret'});
  assert.equal(res.status, 400);
  const body = await res.text();
  assert.match(body, /JSON/);
  assert.doesNotMatch(body, /synthetic-secret/);
});

test('configuration saves Chinese text split across UTF-8 request chunks', async (t) => {
  const {config, configFile, origin, headers} = await configurationServer(t);
  config.models.worker.description = '中文用途';
  const bytes = Buffer.from(JSON.stringify({config}));
  const split = bytes.indexOf(Buffer.from('中文')) + 1;
  const status = await new Promise((resolve, reject) => {
    const req = http.request(origin + '/api/config', {method: 'POST', headers}, (res) => {
      res.resume();
      res.on('end', () => resolve(res.statusCode));
    });
    req.on('error', reject);
    req.write(bytes.subarray(0, split));
    setTimeout(() => req.end(bytes.subarray(split)), 30);
  });
  assert.equal(status, 200);
  assert.equal((await readConfig(configFile)).models.worker.description, '中文用途');
});

test('testing one connection ignores another unfinished model', async (t) => {
  const {config, post} = await configurationServer(t);
  config.models.draft = model({model: ''});
  assert.equal((await post('/api/probe', {config, name: 'worker'})).status, 200);
});

test('removing a model persists only that deletion and supports an empty configuration', async (t) => {
  const {config, configFile, post} = await configurationServer(t);
  const previous = await saveConfig(
    {
      ...config,
      models: {worker: model(), reviewer: model({description: 'saved', apiKey: 'synthetic-review-key'})},
    },
    configFile,
  );
  config.models.reviewer = model({description: 'unsaved'});
  config.models.draft = model({model: ''});
  const res = await post('/api/config/remove', {name: 'worker', config});
  assert.equal(res.status, 200);
  assert.doesNotMatch(await res.text(), /synthetic-key|synthetic-review-key/);
  const stored = await readConfig(configFile);
  assert.deepEqual(Object.keys(stored.models), ['reviewer']);
  assert.deepEqual(stored.models.reviewer, previous.models.reviewer);
  assert.equal(stored.maxConcurrent, previous.maxConcurrent);
  assert.equal((await post('/api/config/remove', {name: 'reviewer'})).status, 200);
  assert.deepEqual(Object.keys((await readConfig(configFile)).models), []);
});

test('model removal rejects unauthorized, cross-origin, and unknown targets without changing configuration', async (t) => {
  const {origin, headers, configFile, post} = await configurationServer(t);
  const before = await fs.readFile(configFile, 'utf8');
  assert.equal((await fetch(origin + '/api/config/remove', {method: 'POST'})).status, 401);
  const crossOrigin = await fetch(origin + '/api/config/remove', {
    method: 'POST',
    headers: {...headers, origin: 'https://other.invalid'},
    body: JSON.stringify({name: 'worker'}),
  });
  assert.equal(crossOrigin.status, 403);
  for (const name of ['missing', '__proto__', 'constructor', null])
    assert.equal((await post('/api/config/remove', {name})).status, 404);
  assert.equal(await fs.readFile(configFile, 'utf8'), before);
});

test('renaming a saved connection preserves its settings and private credentials', async (t) => {
  const {config, configFile, post} = await configurationServer(t, {
    profile: {
      description: '中文用途',
      apiKeyEnv: 'OPTIONAL_MODEL_KEY',
      stream: false,
      firstResponseTimeoutSeconds: 220,
      streamIdleTimeoutSeconds: 150,
      taskTimeoutMinutes: 20,
    },
  });
  const before = await readConfig(configFile);
  assert.equal(config.models.worker.savedName, 'worker');
  config.models.reviewer = config.models.worker;
  delete config.models.worker;
  const res = await post('/api/config', {config});
  assert.equal(res.status, 200);
  const body = await res.json();
  assert.deepEqual(Object.keys(body.config.models), ['reviewer']);
  assert.equal(body.config.models.reviewer.savedName, 'reviewer');
  assert.equal(body.config.models.reviewer.apiKey, '');
  assert.equal(body.config.models.reviewer.hasKey, true);
  assert.doesNotMatch(JSON.stringify(body), /synthetic-key/);
  assert.deepEqual((await readConfig(configFile)).models.reviewer, before.models.worker);
  assert.doesNotMatch(await fs.readFile(configFile, 'utf8'), /savedName|hasKey/);
});

test('invalid, colliding, missing-source and duplicate-source renames do not write configuration', async (t) => {
  const {config, configFile, post} = await configurationServer(t);
  await saveConfig(
    {...config, models: {worker: model(), occupied: model({apiKey: 'other-key'})}},
    configFile,
  );
  const before = await fs.readFile(configFile, 'utf8');
  for (const name of ['occupied', 'Invalid Name', 'constructor', 'a'.repeat(49)]) {
    const renamed = {...config, models: {[name]: config.models.worker}};
    assert.equal((await post('/api/config', {config: renamed})).status, 400);
  }
  for (const savedName of ['missing', '__proto__', null, 1]) {
    const renamed = {...config, models: {renamed: {...config.models.worker, savedName}}};
    assert.equal((await post('/api/config', {config: renamed})).status, 400);
  }
  assert.equal(
    (
      await post('/api/config', {
        config: {
          ...config,
          models: {
            first: config.models.worker,
            second: config.models.worker,
          },
        },
      })
    ).status,
    400,
  );
  assert.equal(await fs.readFile(configFile, 'utf8'), before);
});

test('renamed drafts can probe and list models with the original saved credentials before saving', async (t) => {
  const seen = [];
  const {config, configFile, post} = await configurationServer(t, {
    probeImpl: async (p) => {
      seen.push(p.apiKey);
      return {ok: true, reply: 'OK'};
    },
    listModelsImpl: async (p) => {
      seen.push(p.apiKey);
      return {models: []};
    },
  });
  config.models.renamed = config.models.worker;
  delete config.models.worker;
  for (const route of ['/api/probe', '/api/models'])
    assert.equal((await post(route, {config, name: 'renamed'})).status, 200);
  assert.deepEqual(seen, ['synthetic-key', 'synthetic-key']);
  assert.deepEqual(Object.keys((await readConfig(configFile)).models), ['worker']);
});

test('copy saves only the selected draft, keeps credentials private and avoids draft names', async (t) => {
  const {config, configFile, post} = await configurationServer(t);
  const original = await readConfig(configFile);
  config.maxConcurrent = 0;
  config.models.worker.description = '副本用途';
  config.models.worker.stream = false;
  config.models['worker-copy'] = model({model: ''});
  const res = await post('/api/config/copy', {config, name: 'worker'});
  assert.equal(res.status, 200);
  const body = await res.json();
  assert.equal(body.name, 'worker-copy-2');
  assert.doesNotMatch(JSON.stringify(body), /synthetic-key/);
  assert.equal(body.config.models[body.name].savedName, body.name);
  const stored = await readConfig(configFile);
  assert.equal(stored.maxConcurrent, original.maxConcurrent);
  assert.deepEqual(stored.models.worker, original.models.worker);
  assert.equal(stored.models[body.name].apiKey, original.models.worker.apiKey);
  assert.equal(stored.models[body.name].description, '副本用途');
  assert.equal(stored.models[body.name].stream, false);
  assert.equal(Object.hasOwn(stored.models, 'worker-copy'), false);
  await post('/api/config/remove', {name: 'worker'});
  assert.deepEqual((await readConfig(configFile)).models[body.name], stored.models[body.name]);
});

test('copies of renamed drafts preserve environment references and truncate long names safely', async (t) => {
  const {config, configFile, post} = await configurationServer(t, {
    profile: {apiKeyEnv: 'MODEL_ENV_KEY'},
  });
  const name = 'a'.repeat(48);
  config.models[name] = config.models.worker;
  delete config.models.worker;
  for (let index = 1; index <= 2; index++) {
    const res = await post('/api/config/copy', {config, name});
    assert.equal(res.status, 200);
    const body = await res.json();
    assert.equal(body.name.length, 48);
    assert.match(body.name, index === 1 ? /-copy$/ : /-copy-2$/);
    assert.equal(body.config.models[body.name].apiKeyEnv, 'MODEL_ENV_KEY');
    assert.equal((await readConfig(configFile)).models[body.name].apiKey, 'synthetic-key');
  }
  assert.ok((await readConfig(configFile)).models.worker);
});

test('renaming or copying cannot bypass endpoint credential guards', async (t) => {
  for (const profile of [{}, {apiKey: '', apiKeyEnv: 'MODEL_ENV_KEY'}]) {
    const {config, configFile, post} = await configurationServer(t, {profile});
    const before = await fs.readFile(configFile, 'utf8');
    config.models.renamed = {...config.models.worker, baseUrl: 'https://other.invalid/v1'};
    delete config.models.worker;
    for (const route of ['/api/config', '/api/config/copy', '/api/probe', '/api/models']) {
      const res = await post(route, {config, name: 'renamed'});
      assert.equal(res.status, 400);
      assert.match((await res.json()).error, /重新填写 Key/);
    }
    assert.equal(await fs.readFile(configFile, 'utf8'), before);
  }
});

test('copy rejects unauthorized, cross-origin, missing and over-capacity requests without changes', async (t) => {
  const {config, configFile, origin, headers, post} = await configurationServer(t);
  assert.equal((await fetch(origin + '/api/config/copy', {method: 'POST'})).status, 401);
  assert.equal(
    (
      await fetch(origin + '/api/config/copy', {
        method: 'POST',
        headers: {...headers, origin: 'https://other.invalid'},
        body: JSON.stringify({config, name: 'worker'}),
      })
    ).status,
    403,
  );
  for (const name of ['missing', 'constructor', null])
    assert.equal((await post('/api/config/copy', {config, name})).status, 404);
  assert.deepEqual(Object.keys((await readConfig(configFile)).models), ['worker']);
  await saveConfig(
    {...config, models: Object.fromEntries(Array.from({length: 50}, (_, i) => ['m' + i, model()]))},
    configFile,
  );
  const full = publicConfig(await readConfig(configFile));
  const before = await fs.readFile(configFile, 'utf8');
  const res = await post('/api/config/copy', {config: full, name: 'm0'});
  assert.equal(res.status, 400);
  assert.match((await res.json()).error, /50/);
  assert.equal(await fs.readFile(configFile, 'utf8'), before);
});

test('copy, rename and deletion share the configuration write lock', async (t) => {
  const {config, post} = await configurationServer(t);
  const entered = deferred(),
    release = deferred();
  const renameFile = fs.rename.bind(fs);
  t.after(release.resolve);
  t.mock.method(fs, 'rename', async (...args) => {
    entered.resolve();
    await release.promise;
    return renameFile(...args);
  });
  const copying = post('/api/config/copy', {config, name: 'worker'});
  await entered.promise;
  assert.equal((await post('/api/config/remove', {name: 'worker'})).status, 409);
  assert.equal(
    (await post('/api/config', {config: {...config, models: {renamed: config.models.worker}}})).status,
    409,
  );
  assert.equal((await post('/api/config/copy', {config, name: 'worker'})).status, 409);
  release.resolve();
  assert.equal((await copying).status, 200);
});

test('a slow connection test does not block saving configuration', async (t) => {
  const entered = deferred(),
    release = deferred();
  t.after(release.resolve);
  const {config, post} = await configurationServer(t, {
    probeImpl: async () => {
      entered.resolve();
      await release.promise;
      return {ok: true, reply: 'OK'};
    },
  });
  const probing = post('/api/probe', {config, name: 'worker'});
  await entered.promise;
  const saved = await post('/api/config', {config});
  release.resolve();
  await probing;
  assert.equal(saved.status, 200);
});

test('changing endpoints cannot silently reuse an environment API key', async (t) => {
  const apiKeyEnv = `API_SUBAGENTS_TEST_KEY_${process.pid}`;
  process.env[apiKeyEnv] = 'synthetic-environment-key';
  t.after(() => {
    delete process.env[apiKeyEnv];
  });
  const {config, post} = await configurationServer(t, {profile: {apiKey: '', apiKeyEnv}});
  config.models.worker.baseUrl = 'https://another-service.invalid/v1';
  const res = await post('/api/probe', {config, name: 'worker'});
  assert.equal(res.status, 400);
  assert.match((await res.json()).error, /重新填写 Key/);
});

test('an oversized configuration request receives HTTP 413', async (t) => {
  const {origin, headers} = await configurationServer(t);
  const res = await fetch(origin + '/api/config', {
    method: 'POST',
    headers,
    body: JSON.stringify({large: 'x'.repeat(300001)}),
  });
  assert.equal(res.status, 413);
});

test('file size remains bounded when a file grows after stat', async (t) => {
  const {dir} = await fixture(t);
  const file = path.join(dir, 'growing.txt');
  await fs.writeFile(file, 'small');
  const open = fs.open.bind(fs);
  t.mock.method(fs, 'open', async (...args) => {
    const handle = await open(...args);
    const stat = handle.stat.bind(handle);
    handle.stat = async () => {
      const info = await stat();
      await fs.appendFile(file, 'x'.repeat(200001));
      return info;
    };
    return handle;
  });
  const workspace = await Workspace.open(dir);
  await assert.rejects(() => workspace.read('growing.txt'), /200 KB/);
});
