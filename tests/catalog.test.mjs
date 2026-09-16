import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import http from 'node:http';
import {fileURLToPath} from 'node:url';
import {listRemoteModels, startSetup, saveConfig, readConfig, publicConfig} from '../dist/server.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const profile = (protocol, baseUrl) => ({
  protocol,
  baseUrl,
  apiKey: 'synthetic-list-key',
  model: '',
  maxTokens: 4096,
});
async function serve(t, handler) {
  const server = http.createServer(async (req, res) => {
    try {
      await handler(req, res);
    } catch (error) {
      res.writeHead(500);
      res.end(error.message);
    }
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(
    () =>
      new Promise((resolve) => {
        server.closeAllConnections();
        server.close(resolve);
      }),
  );
  return `http://127.0.0.1:${server.address().port}`;
}
const reply = (res, value) => {
  res.setHeader('content-type', 'application/json');
  res.end(JSON.stringify(value));
};

for (const protocol of ['compatible', 'responses']) {
  test(`${protocol}: model discovery uses authenticated GET, deduplicates IDs, and supports full endpoint addresses`, async (t) => {
    const requests = [];
    const origin = await serve(t, async (req, res) => {
      let body = '';
      for await (const part of req) body += part;
      requests.push({method: req.method, url: req.url, key: req.headers.authorization, body});
      reply(res, {data: [{id: 'model-a'}, {id: 'model-b'}, {id: 'model-a'}, {id: 123}, null]});
    });
    const suffix = protocol === 'compatible' ? 'chat/completions' : 'responses';
    const result = await listRemoteModels(profile(protocol, origin + '/v1/' + suffix));
    assert.deepEqual(result.models, [
      {id: 'model-a', name: 'model-a'},
      {id: 'model-b', name: 'model-b'},
    ]);
    assert.equal(result.truncated, false);
    assert.deepEqual(requests, [
      {method: 'GET', url: '/v1/models', key: 'Bearer synthetic-list-key', body: ''},
    ]);
    assert.doesNotMatch(JSON.stringify(result), /synthetic-list-key/);
  });
}

test('Claude model discovery follows after_id pages and keeps display names', async (t) => {
  const seen = [];
  const origin = await serve(t, (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    seen.push(url);
    assert.equal(req.headers['x-api-key'], 'synthetic-list-key');
    assert.equal(req.headers['anthropic-version'], '2023-06-01');
    reply(
      res,
      url.searchParams.has('after_id')
        ? {data: [{id: 'model-b', display_name: '模型乙'}], has_more: false}
        : {data: [{id: 'model-a', display_name: '模型甲'}], has_more: true, last_id: 'model-a'},
    );
  });
  const result = await listRemoteModels(profile('anthropic', origin + '/v1'));
  assert.equal(seen.length, 2);
  assert.equal(seen[1].searchParams.get('after_id'), 'model-a');
  assert.deepEqual(result.models, [
    {id: 'model-a', name: '模型甲'},
    {id: 'model-b', name: '模型乙'},
  ]);
});

test('Gemini model discovery follows pageToken and excludes embedding-only models', async (t) => {
  const seen = [];
  const origin = await serve(t, (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    seen.push(url);
    assert.equal(req.headers['x-goog-api-key'], 'synthetic-list-key');
    const model = (name, methods) => ({
      name: 'models/' + name,
      displayName: name,
      supportedGenerationMethods: methods,
    });
    reply(
      res,
      url.searchParams.has('pageToken')
        ? {models: [model('model-b', ['generateContent'])]}
        : {
            models: [
              model('model-a', ['generateContent']),
              model('embedding', ['embedContent']),
              {name: 123},
            ],
            nextPageToken: 'page-two',
          },
    );
  });
  const result = await listRemoteModels(profile('gemini', origin + '/v1beta'));
  assert.equal(seen[1].searchParams.get('pageToken'), 'page-two');
  assert.equal(seen[0].pathname, '/v1beta/models');
  assert.deepEqual(
    result.models.map((item) => item.id),
    ['model-a', 'model-b'],
  );
});

test('model discovery rejects repeated cursors and malformed response shapes', async () => {
  const p = profile('anthropic', 'http://localhost/v1');
  await assert.rejects(
    () =>
      listRemoteModels(p, {
        fetchImpl: async () => Response.json({data: [{id: 'a'}], has_more: true, last_id: 'same'}),
      }),
    /分页异常/,
  );
  await assert.rejects(
    () => listRemoteModels(p, {fetchImpl: async () => Response.json({data: 'wrong'})}),
    /标准模型列表/,
  );
});

test('model discovery bounds pagination and result counts', async () => {
  let count = 0;
  const result = await listRemoteModels(profile('gemini', 'http://localhost/v1beta'), {
    fetchImpl: async () => {
      count++;
      return Response.json({models: [{name: `models/model-${count}`}], nextPageToken: `page-${count}`});
    },
  });
  assert.equal(count, 10);
  assert.equal(result.truncated, true);
  const many = await listRemoteModels(profile('compatible', 'http://localhost/v1'), {
    fetchImpl: async () => Response.json({data: Array.from({length: 1100}, (_, i) => ({id: 'model-' + i}))}),
  });
  assert.equal(many.models.length, 1000);
  assert.equal(many.truncated, true);
});

test('unsupported listing endpoints offer manual entry and HTTP errors do not echo response bodies', async (t) => {
  let status = 404;
  const origin = await serve(t, (req, res) => {
    res.writeHead(status);
    res.end('synthetic-list-key private-response');
  });
  const p = profile('compatible', origin + '/v1');
  await assert.rejects(() => listRemoteModels(p), /手动输入/);
  status = 401;
  await assert.rejects(
    () => listRemoteModels(p),
    (error) => /401/.test(error.message) && !/synthetic-list-key|private-response/.test(error.message),
  );
});

test('listing refuses redirects instead of forwarding credentials', async (t) => {
  let requests = 0;
  const origin = await serve(t, (req, res) => {
    requests++;
    res.writeHead(302, {location: '/target'});
    res.end();
  });
  await assert.rejects(() => listRemoteModels(profile('compatible', origin + '/v1')));
  assert.equal(requests, 1);
});

test('an aborted model-list request stops immediately', async () => {
  const controller = new AbortController();
  controller.abort();
  let called = false;
  await assert.rejects(
    () =>
      listRemoteModels(profile('compatible', 'http://localhost/v1'), {
        signal: controller.signal,
        fetchImpl: async () => {
          called = true;
          return Response.json({data: []});
        },
      }),
    /取消/,
  );
  assert.equal(called, false);
});

test('setup lists models before a model is selected, preserves saved keys and does not save drafts', async (t) => {
  const parent = process.env.API_SUBAGENTS_TEST_ROOT || path.join(os.tmpdir(), 'api-subagents-tests');
  await fs.mkdir(parent, {recursive: true});
  const dir = await fs.mkdtemp(path.join(parent, 'catalog-'));
  const file = path.join(dir, 'models.json');
  await saveConfig(
    {
      version: 1,
      models: {worker: {...profile('compatible', 'http://localhost:9999/v1'), model: 'old-model'}},
    },
    file,
  );
  const received = [];
  const ui = await startSetup({
    htmlPath: path.join(root, 'dist/config.html'),
    configFile: file,
    listModelsImpl: async (p) => {
      received.push(p);
      return {models: [{id: 'new-model', name: 'New model'}], truncated: false};
    },
  });
  t.after(
    () =>
      new Promise((resolve) => {
        ui.server.closeAllConnections();
        ui.server.close(resolve);
      }),
  );
  const headers = {authorization: 'Bearer ' + ui.token, 'content-type': 'application/json'};
  const config = publicConfig(await readConfig(file));
  config.models.worker.model = '';
  const post = (route, more = {}) =>
    fetch(ui.origin + route, {
      method: 'POST',
      headers: {...headers, ...more},
      body: JSON.stringify({config, name: 'worker'}),
    });
  const unauthorized = await fetch(ui.origin + '/api/models', {method: 'POST'});
  assert.equal(unauthorized.status, 401);
  assert.equal((await post('/api/models', {origin: 'https://other.invalid'})).status, 403);
  const res = await post('/api/models');
  assert.equal(res.status, 200);
  assert.equal(received[0].model, '');
  assert.equal(received[0].apiKey, 'synthetic-list-key');
  assert.doesNotMatch(await res.text(), /synthetic-list-key/);
  assert.equal((await readConfig(file)).models.worker.model, 'old-model');
  assert.equal((await post('/api/config')).status, 400);
  config.models.worker.baseUrl = 'https://another-service.invalid/v1';
  assert.equal((await post('/api/models')).status, 400);
  assert.equal(received.length, 1);
  const css = await fetch(ui.origin + '/config.css');
  assert.equal(css.status, 200);
  assert.match(css.headers.get('content-type'), /text\/css/);
});
