import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import crypto from 'node:crypto';
import http from 'node:http';
import {
  TaskManager,
  turn,
  initialState,
  saveConfig,
  readConfig,
  validateConfig,
  startSetup,
  publicConfig,
} from '../dist/server.mjs';

const frame = (data, event = '') =>
  `${event ? `event: ${event}\r\n` : ''}data: ${typeof data === 'string' ? data : JSON.stringify(data)}\r\n\r\n`;
const chat = (delta, finish_reason = null) => frame({choices: [{index: 0, delta, finish_reason}]});
const profile = (protocol = 'compatible', baseUrl = 'https://offline.invalid/v1', overrides = {}) => ({
  protocol,
  baseUrl,
  model: 'test-model',
  apiKey: 'synthetic-stream-key',
  maxTokens: 4096,
  ...overrides,
});
async function serve(t, handler) {
  const server = http.createServer(async (req, res) => {
    try {
      let body = '';
      for await (const chunk of req) body += chunk;
      await handler(req, res, body ? JSON.parse(body) : null);
    } catch (error) {
      if (!res.headersSent) res.writeHead(500);
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
  return `http://127.0.0.1:${server.address().port}/v1`;
}
function emit(res, frames) {
  res.setHeader('content-type', 'text/event-stream; charset=utf-8');
  // 每字节切分在解码器单测中验证；HTTP 集成同时覆盖真实 socket 和 CRLF。
  const bytes = Buffer.from(frames.join(''));
  for (let offset = 0; offset < bytes.length; offset += 17)
    res.write(bytes.subarray(offset, offset + 17));
  res.end();
}
function responseFrames(protocol, tool, args, id) {
  const final = !tool;
  if (protocol === 'compatible') {
    if (final)
      return [
        chat({role: 'assistant', content: '审查'}),
        chat({content: '完成。'}, 'stop'),
        frame('[DONE]'),
      ];
    const json = JSON.stringify(args),
      split = Math.floor(json.length / 2);
    return [
      ': keep-alive\r\n\r\n',
      chat({role: 'assistant', reasoning_content: 'private reasoning'}),
      chat({
        tool_calls: [
          {index: 0, id, type: 'function', function: {name: tool, arguments: json.slice(0, split)}},
        ],
      }),
      chat({tool_calls: [{index: 0, function: {arguments: json.slice(split)}}]}, 'tool_calls'),
      frame('[DONE]'),
    ];
  }
  if (protocol === 'responses') {
    const output = final
      ? [{type: 'message', role: 'assistant', content: [{type: 'output_text', text: '审查完成。'}]}]
      : [
          {type: 'reasoning', id: 'reasoning-' + id, summary: [], encrypted_content: 'encrypted-' + id},
          {type: 'function_call', call_id: id, name: tool, arguments: JSON.stringify(args)},
        ];
    return [
      frame({type: 'response.created', response: {status: 'in_progress'}}),
      frame({type: 'response.output_text.delta', delta: '审查'}),
      frame({type: 'response.completed', response: {status: 'completed', output}}),
    ];
  }
  if (protocol === 'anthropic') {
    const frames = [
      frame({type: 'message_start', message: {role: 'assistant', content: [], stop_reason: null}}),
      frame({type: 'ping'}),
    ];
    if (final)
      frames.push(
        frame({type: 'content_block_start', index: 0, content_block: {type: 'text', text: ''}}),
        frame({type: 'content_block_delta', index: 0, delta: {type: 'text_delta', text: '审查完成。'}}),
        frame({type: 'content_block_stop', index: 0}),
      );
    else {
      const json = JSON.stringify(args),
        split = Math.floor(json.length / 2);
      frames.push(
        frame({
          type: 'content_block_start',
          index: 0,
          content_block: {type: 'thinking', thinking: '', signature: ''},
        }),
        frame({
          type: 'content_block_delta',
          index: 0,
          delta: {type: 'thinking_delta', thinking: 'private reasoning'},
        }),
        frame({
          type: 'content_block_delta',
          index: 0,
          delta: {type: 'signature_delta', signature: 'signature-' + id},
        }),
        frame({type: 'content_block_stop', index: 0}),
        frame({
          type: 'content_block_start',
          index: 1,
          content_block: {type: 'tool_use', id, name: tool, input: {}},
        }),
        frame({
          type: 'content_block_delta',
          index: 1,
          delta: {type: 'input_json_delta', partial_json: json.slice(0, split)},
        }),
        frame({
          type: 'content_block_delta',
          index: 1,
          delta: {type: 'input_json_delta', partial_json: json.slice(split)},
        }),
        frame({type: 'content_block_stop', index: 1}),
      );
    }
    return [
      ...frames,
      frame({type: 'message_delta', delta: {stop_reason: final ? 'end_turn' : 'tool_use'}}),
      frame({type: 'message_stop'}),
    ];
  }
  return [
    frame({
      candidates: [
        {
          index: 0,
          content: {
            role: 'model',
            parts: final ? [{text: '审查'}] : [{thought: true, text: 'private reasoning'}],
          },
        },
      ],
    }),
    frame({
      candidates: [
        {
          index: 0,
          content: {
            role: 'model',
            parts: final
              ? [{text: '完成。'}]
              : [{functionCall: {id, name: tool, args}, thoughtSignature: 'signature-' + id}],
          },
          finishReason: 'STOP',
        },
      ],
    }),
  ];
}
async function fixture(t, baseUrl, overrides = {}, options = {}) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'api-subagents-stream-'));
  const configFile = path.join(dir, 'models.json');
  await saveConfig(
    {version: 1, models: {worker: profile('compatible', baseUrl, overrides)}},
    configFile,
  );
  const manager = new TaskManager({configFile, storageDir: path.join(dir, 'results'), ...options});
  t.after(() => manager.close());
  return {
    dir,
    configFile,
    manager,
    submit: () => manager.submit({model: 'worker', task: 'Local streaming fixture', workspace: dir}),
  };
}

for (const protocol of ['compatible', 'responses', 'anthropic', 'gemini']) {
  test(`${protocol}: streamed tool arguments, history and final answer survive three real HTTP turns`, async (t) => {
    const requests = [];
    const url = await serve(t, (req, res, body) => {
      requests.push(body);
      assert.equal(req.headers.accept, 'text/event-stream, application/json');
      if (protocol === 'gemini')
        assert.equal(req.url, '/v1/models/test-model:streamGenerateContent?alt=sse');
      else assert.equal(body.stream, true);
      const n = requests.length;
      const args =
        n === 1
          ? {path: '你好.txt'}
          : {
              path: '你好.txt',
              content: 'after',
              expectedSha256: crypto.createHash('sha256').update('before').digest('hex'),
            };
      emit(
        res,
        responseFrames(
          protocol,
          n === 3 ? null : n === 1 ? 'read_file' : 'propose_file',
          args,
          'call-' + n,
        ),
      );
    });
    const {dir, manager, submit} = await fixture(t, url, {protocol});
    await fs.writeFile(path.join(dir, '你好.txt'), 'before');
    const job = await submit();
    const result = await manager.wait(job.task_id, 5000);
    assert.equal(result.status, 'completed', result.error);
    assert.equal(result.result, '审查完成。');
    assert.equal(result.toolCalls, 2);
    assert.equal(result.changes[0].content, 'after');
    assert.equal(await fs.readFile(path.join(dir, '你好.txt'), 'utf8'), 'before');
    assert.match(JSON.stringify(requests[1]), /before/);
    if (protocol === 'compatible') assert.match(JSON.stringify(requests[1]), /reasoning_content/);
    if (protocol === 'responses') assert.match(JSON.stringify(requests[1]), /encrypted-call-1/);
    if (['anthropic', 'gemini'].includes(protocol))
      assert.match(JSON.stringify(requests[1]), /signature-call-1/);
    assert.ok(result.progress.receivedBytes > 0);
    assert.ok(result.progress.events > 0);
    assert.ok(result.progress.firstByteAt);
    assert.doesNotMatch(JSON.stringify(result), /synthetic-stream-key|private reasoning/);
  });
}

test('SSE decoder handles one-byte UTF-8, CRLF, comments and multiline data with interleaved tools', async () => {
  const frames =
    ': heartbeat\r\n\r\n' +
    'data: {"choices":\r\ndata: [{"index":0,"delta":{"content":"中文"}}]}\r\n\r\n' +
    chat({
      tool_calls: [
        {index: 1, id: 'second', function: {name: 'read_file', arguments: '{"path":"'}},
        {index: 0, id: 'first', function: {name: 'read_file', arguments: '{"path":"one'}},
      ],
    }) +
    chat(
      {
        tool_calls: [
          {index: 0, function: {arguments: '.txt"}'}},
          {index: 1, function: {arguments: 'two.txt"}'}},
        ],
      },
      'tool_calls',
    ) +
    frame('[DONE]');
  const bytes = Buffer.from(frames);
  let index = 0;
  const body = new ReadableStream({
    pull(controller) {
      if (index < bytes.length) controller.enqueue(bytes.subarray(index, ++index));
      else controller.close();
    },
  });
  const p = profile(),
    state = initialState(p, 'test');
  const answer = await turn(
    p,
    state,
    '',
    [],
    new AbortController().signal,
    async () => new Response(body, {headers: {'content-type': 'text/event-stream'}}),
  );
  assert.equal(answer.text, '中文');
  assert.deepEqual(
    answer.calls.map((c) => [c.id, JSON.parse(c.args).path]),
    [
      ['first', 'one.txt'],
      ['second', 'two.txt'],
    ],
  );
});

for (const protocol of ['compatible', 'responses', 'anthropic', 'gemini']) {
  test(`${protocol}: a disconnected stream cannot commit partial tools or history`, async () => {
    const p = profile(protocol),
      state = initialState(p, 'test'),
      before = structuredClone(state);
    const frames = responseFrames(protocol, 'read_file', {path: 'hello.txt'}, 'partial');
    // 所有协议都丢弃最后的明确完成事件，即使工具参数看起来已经完整。
    frames.pop();
    await assert.rejects(
      () =>
        turn(
          p,
          state,
          '',
          [],
          new AbortController().signal,
          async () => new Response(frames.join(''), {headers: {'content-type': 'text/event-stream'}}),
        ),
      /中断/,
    );
    assert.deepEqual(state, before);
  });
}

test('stream errors and malformed JSON do not echo API bodies or commit history', async () => {
  for (const payload of [
    frame({type: 'error', error: {message: 'synthetic-stream-key private-body'}}),
    frame('bad private-body'),
  ]) {
    const p = profile(),
      state = initialState(p, 'test');
    await assert.rejects(
      () =>
        turn(
          p,
          state,
          '',
          [],
          new AbortController().signal,
          async () => new Response(payload, {headers: {'content-type': 'text/event-stream'}}),
        ),
      (e) => !/synthetic|private-body/.test(e.message),
    );
    assert.equal(state.length, 1);
  }
});

test('token-truncated streamed answers fail without adding partial history', async () => {
  const p = profile(),
    state = initialState(p, 'test');
  await assert.rejects(
    () =>
      turn(
        p,
        state,
        '',
        [],
        new AbortController().signal,
        async () =>
          new Response(chat({content: 'partial'}, 'length') + frame('[DONE]'), {
            headers: {'content-type': 'text/event-stream'},
          }),
      ),
    /截断/,
  );
  assert.equal(state.length, 1);
});

test('SSE heartbeats keep an active response alive beyond its initial waiting limit', async (t) => {
  const url = await serve(t, async (req, res) => {
    res.setHeader('content-type', 'text/event-stream');
    for (let i = 0; i < 6; i++) {
      res.write(': heartbeat\n\n');
      await new Promise((resolve) => setTimeout(resolve, 40));
    }
    res.end(chat({content: 'done'}, 'stop') + frame('[DONE]'));
  });
  const p = profile('compatible', url, {
    firstResponseTimeoutSeconds: 0.1,
    streamIdleTimeoutSeconds: 0.25,
  });
  const progress = [];
  const started = Date.now();
  const answer = await turn(
    p,
    initialState(p, 'test'),
    '',
    [],
    new AbortController().signal,
    fetch,
    (value) => progress.push(value),
  );
  assert.equal(answer.text, 'done');
  assert.ok(Date.now() - started > 100);
  assert.ok(progress.some((value) => value.phase === 'streaming' && value.receivedBytes > 0));
});

for (const headersOnly of [false, true]) {
  test(`first-data timeout covers ${headersOnly ? 'headers received but no body' : 'waiting for HTTP headers'}`, async (t) => {
    let requests = 0;
    const url = await serve(t, (req, res) => {
      requests++;
      if (headersOnly) {
        res.setHeader('content-type', 'text/event-stream');
        res.flushHeaders();
      }
    });
    const p = profile('compatible', url, {firstResponseTimeoutSeconds: 0.08});
    await assert.rejects(
      () => turn(p, initialState(p, 'test'), '', [], new AbortController().signal),
      (error) => error.code === 'FIRST_RESPONSE_TIMEOUT' && /首个/.test(error.message),
    );
    assert.equal(requests, 1);
  });
}

test('an idle stream fails with a distinct timeout and never retries the generation', async (t) => {
  let requests = 0;
  const url = await serve(t, (req, res) => {
    requests++;
    res.setHeader('content-type', 'text/event-stream');
    res.write(chat({content: 'partial'}));
  });
  const p = profile('compatible', url, {streamIdleTimeoutSeconds: 0.08});
  await assert.rejects(
    () => turn(p, initialState(p, 'test'), '', [], new AbortController().signal),
    (error) => error.code === 'STREAM_IDLE_TIMEOUT' && /没有新数据/.test(error.message),
  );
  assert.equal(requests, 1);
});

test('cancellation aborts an active stream and exposes only safe progress metadata', async (t) => {
  const url = await serve(t, (req, res) => {
    res.setHeader('content-type', 'text/event-stream');
    res.write(chat({reasoning_content: 'synthetic-stream-key private reasoning'}));
  });
  const {manager, submit} = await fixture(t, url);
  const job = await submit();
  for (let i = 0; i < 100; i++) {
    if ((await manager.get(job.task_id)).progress.receivedBytes > 0) break;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  const active = await manager.get(job.task_id);
  assert.equal(active.progress.phase, 'streaming');
  assert.ok(active.progress.requestElapsedMs >= 0);
  assert.doesNotMatch(JSON.stringify(active), /synthetic-stream-key|private reasoning/);
  await manager.cancel(job.task_id);
  const result = await manager.wait(job.task_id, 2000);
  assert.equal(result.status, 'cancelled');
  assert.equal(result.toolCalls, 0);
});

test('the task deadline still stops a never-ending heartbeat stream with a diagnostic code', async (t) => {
  const realTimeout = setTimeout;
  t.mock.method(globalThis, 'setTimeout', (fn, ms, ...args) =>
    realTimeout(fn, ms === 60000 ? 100 : ms, ...args),
  );
  const url = await serve(t, (req, res) => {
    res.setHeader('content-type', 'text/event-stream');
    res.write(': heartbeat\n\n');
    const interval = setInterval(() => res.write(': heartbeat\n\n'), 10);
    res.on('close', () => clearInterval(interval));
  });
  const {manager, submit} = await fixture(t, url, {taskTimeoutMinutes: 1});
  const job = await submit();
  const result = await manager.wait(job.task_id, 2000);
  assert.equal(result.status, 'failed');
  assert.equal(result.errorCode, 'TASK_TIMEOUT');
  assert.match(result.error, /第 1 轮.*1 分钟/);
});

test('streaming response size remains bounded', async () => {
  const p = profile();
  let cancelled = false;
  const body = new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array(8 * 1024 * 1024 + 1));
    },
    cancel() {
      cancelled = true;
    },
  });
  await assert.rejects(
    () =>
      turn(
        p,
        initialState(p, 'test'),
        '',
        [],
        new AbortController().signal,
        async () => new Response(body, {headers: {'content-type': 'text/event-stream'}}),
      ),
    /8 MB/,
  );
  assert.equal(cancelled, true);
});

test('an explicit ordinary-response setting preserves JSON-only gateways without extra requests', async (t) => {
  let requests = 0;
  const url = await serve(t, (req, res, body) => {
    requests++;
    assert.equal(body.stream, false);
    res.setHeader('content-type', 'application/json');
    res.end(
      JSON.stringify({choices: [{message: {role: 'assistant', content: 'OK'}, finish_reason: 'stop'}]}),
    );
  });
  const p = profile('compatible', url, {stream: false});
  assert.equal(
    (await turn(p, initialState(p, 'test'), '', [], new AbortController().signal)).text,
    'OK',
  );
  assert.equal(requests, 1);
});

test('streaming and timeout settings default safely, validate and round-trip through setup', async (t) => {
  const {configFile} = await fixture(t, 'https://offline.invalid/v1');
  const stored = await readConfig(configFile);
  assert.equal(stored.models.worker.stream, true);
  assert.equal(stored.models.worker.firstResponseTimeoutSeconds, 180);
  assert.equal(stored.models.worker.streamIdleTimeoutSeconds, 120);
  assert.equal(stored.models.worker.taskTimeoutMinutes, 15);
  for (const overrides of [
    {stream: 'true'},
    {firstResponseTimeoutSeconds: 0},
    {streamIdleTimeoutSeconds: 601},
    {taskTimeoutMinutes: 61},
  ]) {
    assert.throws(() =>
      validateConfig({version: 1, models: {worker: {...stored.models.worker, ...overrides}}}),
    );
  }
  const ui = await startSetup({configFile, htmlPath: path.resolve('dist/config.html')});
  t.after(
    () =>
      new Promise((resolve) => {
        ui.server.closeAllConnections();
        ui.server.close(resolve);
      }),
  );
  const config = publicConfig(stored);
  Object.assign(config.models.worker, {
    stream: false,
    firstResponseTimeoutSeconds: 300,
    streamIdleTimeoutSeconds: 240,
    taskTimeoutMinutes: 30,
  });
  const res = await fetch(ui.origin + '/api/config', {
    method: 'POST',
    headers: {authorization: 'Bearer ' + ui.token, 'content-type': 'application/json'},
    body: JSON.stringify({config}),
  });
  assert.equal(res.status, 200);
  const next = (await readConfig(configFile)).models.worker;
  assert.equal(next.stream, false);
  assert.equal(next.firstResponseTimeoutSeconds, 300);
  assert.equal(next.streamIdleTimeoutSeconds, 240);
  assert.equal(next.taskTimeoutMinutes, 30);
  assert.equal(next.apiKey, 'synthetic-stream-key');
});
