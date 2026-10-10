const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

/** 创建最小 DOM 测试替身；不启动真实模型请求，不访问用户配置或浏览器。 */
function harness() {
  const notices = [];
  let copied = '';
  const doc = {activeElement: null};
  class Element {
    /** 保存节点状态，并拒绝 innerHTML 赋值以验证所有服务端字段均按文本呈现。 */
    constructor(tag) { this.tag = tag; this.children = []; this.dataset = {}; this.attributes = {}; this.events = {}; this.textContent = ''; }
    /** 收集子节点，模拟动态请求列表的安全构建。 */
    append(...children) { this.children.push(...children); }
    /** 替换列表但不改变其他区域的焦点。 */
    replaceChildren(...children) { this.children = children; }
    /** 记录可访问属性供测试校验。 */
    setAttribute(key, value) { this.attributes[key] = value; }
    /** 保留按钮事件，以便测试用户主动复制。 */
    addEventListener(name, callback) { this.events[name] = callback; }
    /** 查询焦点是否位于当前请求列表。 */
    contains(node) { return this === node || this.children.some(child => child.contains(node)); }
    /** 按标签查询后代按钮，验证刷新后的焦点恢复。 */
    querySelectorAll(tag) { return this.children.flatMap(child => [...(child.tag === tag ? [child] : []), ...child.querySelectorAll(tag)]); }
    /** 模拟聚焦，不触发页面外副作用。 */
    focus() { doc.activeElement = this; }
    /** 禁止 HTML 拼接，发现不安全渲染时立即失败。 */
    set innerHTML(_) { throw new Error('HTML injection is forbidden'); }
  }
  const nodes = Object.fromEntries(['relay-diagnostics', 'relay-diagnostics-summary', 'relay-diagnostics-list', 'relay-diagnostics-empty'].map(id => [id, new Element('div')]));
  doc.getElementById = id => nodes[id];
  doc.createElement = tag => new Element(tag);
  const window = {notices: {show: (...args) => notices.push(args)}};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'relay-diagnostics-ui.js'), 'utf8'), {window, document: doc, navigator: {clipboard: {writeText: async text => { copied = text; }}}});
  return {ui: window.relayDiagnostics, doc, nodes, notices, copied: () => copied};
}

const fixture = {id: 'local-1', connection: 'demo', model: 'mock-model', startedAt: '2026-10-07T00:00:00Z', outcome: 'receiving', httpStatus: 200, upstreamRequestId: 'req-safe', firstByteMs: 20, durationMs: 100, bytesReceived: 40};

test('HTTP 200 and unknown states are not classified as generated success', () => {
  const {ui} = harness();
  assert.equal(ui.describe('completed')[1], 'success');
  for (const outcome of ['http_completed', 'receiving', 'client_cancelled']) assert.equal(ui.describe(outcome)[1], 'neutral');
  for (const outcome of ['upstream_failed', 'unexpected_eof', 'first_byte_timeout', 'unknown']) assert.equal(ui.describe(outcome)[1], 'warning');
});

test('render, notify once, preserve keyboard focus, and copy only explicit safe fields', async () => {
  const h = harness();
  h.ui.render({enabled: true, recentRequests: [fixture]});
  h.nodes['relay-diagnostics-list'].querySelectorAll('button')[0].focus();
  const failed = {...fixture, connection: '<img src=x onerror=alert(1)>', outcome: 'upstream_failed', apiKey: 'private-key', prompt: 'private-prompt', response: 'private-response'};
  h.ui.render({enabled: true, recentRequests: [failed]});
  assert.equal(h.nodes['relay-diagnostics-summary'].dataset.kind, 'warning');
  assert.equal(h.notices.length, 1);
  assert.equal(h.doc.activeElement.dataset.requestId, fixture.id);
  h.ui.render({enabled: true, recentRequests: [failed]});
  assert.equal(h.notices.length, 1);
  await h.nodes['relay-diagnostics-list'].querySelectorAll('button')[0].events.click();
  assert.match(h.copied(), /req-safe/);
  assert.doesNotMatch(h.copied(), /private-key|private-prompt|private-response/);
  h.ui.render({enabled: false, recentRequests: []});
  assert.equal(h.nodes['relay-diagnostics'].hidden, true);
});

test('independent requests keep latest request status and list remains bounded', () => {
  const h = harness();
  const items = Array.from({length: 25}, (_, i) => ({...fixture, id: 'request-' + i, outcome: i === 0 ? 'completed' : 'upstream_failed'}));
  h.ui.render({enabled: true, recentRequests: items});
  assert.equal(h.nodes['relay-diagnostics-list'].children.length, 20);
  assert.equal(h.nodes['relay-diagnostics-summary'].dataset.kind, 'success');
  assert.equal(h.notices.length, 0);
});

test('prototype theme, production resources, and automatic context limit agree', () => {
  const css = fs.readFileSync(path.join(__dirname, 'approved-workbench.css'), 'utf8');
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const embed = fs.readFileSync(path.join(__dirname, 'assets.go'), 'utf8');
  assert.match(css, /--ink:\s*#24262c/);
  assert.match(css, /--accent:\s*#b64b20/);
  assert.match(css, /--surface:\s*#f8f9fb/);
  for (const name of ['approved-workbench.css','workbench-views.js','relay-diagnostics-ui.js']) { assert.ok(html.includes('/' + name)); assert.ok(embed.includes(name)); }
  for (const name of ['radix-palette.css','workbench-theme.css','workbench.css']) assert.equal(html.includes('/' + name),false);
  assert.match(html, /id="help-request-diagnostics"/); assert.match(html, /<dialog id="relay-diagnostics-dialog"/);
  const source = fs.readFileSync(path.join(__dirname, 'model-picker-ui.js'), 'utf8');
  const start = source.indexOf('function updateContextHint(');
  const end = source.indexOf('\n  /**', start);
  const context = {window: {configOperations: require('./config-operations.js')}};
  vm.runInNewContext(source.slice(start, end) + '\nthis.update = updateContextHint;', context);
  const fields = {'#model-context-window': {value: '500000', validity: {}}, '#edit-model-id': {value: 'mock'}, '#reset-model-context': {}, '#model-context-help': {}};
  const root = {querySelector: selector => fields[selector]};
  for (const [tokens, expected] of [[1000000, 256000], [128000, 128000]]) {
    context.update(root, {catalog: {models: [{id: 'mock', contextWindow: tokens}]}, profile: {}});
    assert.equal(fields['#model-context-window'].value, String(expected));
    assert.equal(fields['#model-context-window'].readOnly, true);
    assert.equal(fields['#reset-model-context'].disabled, true);
    assert.ok(fields['#model-context-help'].textContent.includes(expected.toLocaleString()));
  }
  context.update(root, {profile: {modelOfficialContexts: {mock: 64000}}});
  assert.equal(fields['#model-context-window'].value, '64000');
  context.update(root, {profile: {}});
  assert.equal(fields['#model-context-window'].value, '256000');
  assert.equal(fields['#model-context-window'].readOnly, false);
  fields['#model-context-window'].value = '500000';
  context.update(root, {profile: {}});
  assert.match(fields['#model-context-help'].textContent, /已有手动配置 500,000/);
  assert.equal(fields['#reset-model-context'].disabled, false);
  fields['#model-context-window'].value = '256000';
  context.update(root, {profile: {}});
  assert.match(fields['#model-context-help'].textContent, /官方容量待确认/);
  assert.equal(fields['#reset-model-context'].disabled, true);
});
