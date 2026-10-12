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

test('approved bundled resources, retained diagnostics, and explicit context policy agree', async () => {
  const css = fs.readFileSync(path.join(__dirname, 'src/styles.css'), 'utf8');
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const embed = fs.readFileSync(path.join(__dirname, 'assets.go'), 'utf8');
  assert.match(css, /--ink:\s*#25272e/);
  assert.match(css, /--accent:\s*#394254/);
  assert.match(css, /--surface:\s*#f6f7f9/);
  for (const name of ['workbench-app.css', 'workbench-app.js', 'bridge.js']) {assert.ok(html.includes(name)); assert.ok(embed.includes(name));}
  for (const name of ['approved-workbench.css', 'workbench-views.js', 'radix-palette.css', 'workbench-theme.css', 'workbench.css']) assert.equal(html.includes(name), false);
  const app = fs.readFileSync(path.join(__dirname, 'src/app.jsx'), 'utf8');
  const diagnostics = fs.readFileSync(path.join(__dirname, 'src/diagnostics.jsx'), 'utf8');
  assert.match(app, /查看最近请求/); assert.match(app, /<RequestDiagnostics relay={backend\.relay}/);
  assert.match(diagnostics, /import '\.\.\/relay-diagnostics-ui\.js'/);
  const {defaultContextSettings, effectiveContext} = await import(require('node:url').pathToFileURL(path.join(__dirname, 'src/domain.mjs')));
  for (const [tokens, expected] of [[1000000, 256000], [128000, 128000]]) {
    assert.deepEqual(defaultContextSettings(tokens), {contextEnabled: true, contextOverride: expected});
    assert.equal(effectiveContext({context: tokens, contextEnabled: false, contextOverride: 500000}), tokens);
  }
  assert.deepEqual(defaultContextSettings(null), {contextEnabled: false, contextOverride: null});
  assert.equal(effectiveContext({context: null, contextEnabled: false}), null);
  assert.equal(effectiveContext({context: 1000000, contextEnabled: true, contextOverride: 500000}), 500000);
});
