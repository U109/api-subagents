// 正式资源 → 带令牌的 Go 配置服务 → 本地模拟上游；没有注入 mockBindings，也不访问真实账户。
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const {spawn, execFileSync} = require('node:child_process');
const {chromium} = require('playwright');
const root = path.resolve(__dirname, '..');

/** 创建可控制响应时机的测试屏障；只延迟真实响应，不替换 Go 的保存结果。 */
function deferred() {let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve};}

/** 等待可观察的后台条件，超时报出具体断言而不依赖固定保存耗时。 */
async function until(read, message) {
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) {if (await read()) return; await new Promise(resolve => setTimeout(resolve, 40));}
  throw new Error(message);
}

/** 仅在回环提供合成目录与响应；记录凭据和取消是否真的到达上游。 */
async function upstream() {
  const state = {requests: [], slow: false, aborted: 0};
  const server = http.createServer(async (req, res) => {
    let body = ''; for await (const chunk of req) body += chunk;
    state.requests.push({path: req.url, authorization: req.headers.authorization, body: body ? JSON.parse(body) : null});
    res.setHeader('Content-Type', 'application/json');
    if (req.url === '/v1/models') {res.end(JSON.stringify({data: [{id: 'local-alpha', context_window: 128000}, {id: 'local-beta', context_window: 512000}]})); return;}
    if (req.url !== '/v1/responses') {res.writeHead(404); res.end('{}'); return;}
    if (state.slow) {
      res.on('close', () => {if (!res.writableEnded) state.aborted++;});
      return;
    }
    res.end(JSON.stringify({id: 'synthetic-response', status: 'completed', output: [{type: 'message', role: 'assistant', content: [{type: 'output_text', text: 'LOCAL OK'}]}], usage: {input_tokens: 100, output_tokens: 20, total_tokens: 120, input_tokens_details: {cached_tokens: 0, cache_write_tokens: 0}, output_tokens_details: {reasoning_tokens: 4}}}));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return {server, state, url: `http://127.0.0.1:${server.address().port}/v1`};
}

/** 启动独立 Go worker 的真实配置服务，所有配置与 Codex 文件均在专用测试目录。 */
async function startService(executable, env) {
  const process = spawn(executable, ['--configure', '--no-open'], {cwd: root, env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe']});
  let log = '';
  const url = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {process.kill(); reject(new Error('真实 Go 配置服务启动超时'));}, 15000);
    process.on('error', error => {clearTimeout(timeout); reject(error);});
    process.on('exit', code => {clearTimeout(timeout); reject(new Error('Go 配置服务意外退出：' + code));});
    process.stderr.on('data', data => {log += data.toString(); const match = log.match(/http:\/\/127\.0\.0\.1:\d+\/#([a-f0-9]+)/); if (match) {clearTimeout(timeout); resolve(match[0]);}});
  });
  return {process, url, origin: new URL(url).origin, token: new URL(url).hash.slice(1)};
}

/** 读取真实后台脱敏配置或模拟第二个受权窗口，未授权请求不会携带测试令牌。 */
async function request(service, route, body) {
  const response = await fetch(service.origin + route, {method: body === undefined ? 'GET' : 'POST', headers: {'Authorization': 'Bearer ' + service.token, 'Content-Type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body)});
  const result = await response.json(); if (!response.ok || result.error) throw new Error(result.error || 'Go API failed');
  return result;
}

/** 使用用户可见导航进入页面，不调用 React 内部状态或测试专属绑定。 */
async function navigate(page, name) {await page.locator('.sidebar .primary-nav .nav-button').filter({hasText: name}).click();}

/** 保存连接并等待真实响应完成，草稿未保存时浏览器对话框仅由测试显式放弃。 */
async function saveConnection(page) {
  const response = page.waitForResponse(value => value.url().endsWith('/api/config/save'));
  await page.getByRole('button', {name: '保存连接', exact: true}).click();
  assert.equal((await response).status(), 200);
  await page.getByRole('button', {name: '保存连接', exact: true}).waitFor();
  await until(() => page.getByRole('button', {name: '保存连接', exact: true}).isDisabled(), '连接未进入已保存状态');
}

/** 验证完整真实操作链、重启持久化、迟到响应、草稿保留、用量和独立费率文件。 */
async function main() {
  fs.mkdirSync(path.join(root, '.tools'), {recursive: true});
  const testRoot = fs.mkdtempSync(path.join(root, '.tools', 'real-service-'));
  const env = {...process.env, API_SUBAGENTS_HOME: path.join(testRoot, 'config'), CODEX_HOME: path.join(testRoot, 'codex')};
  fs.mkdirSync(env.API_SUBAGENTS_HOME); fs.mkdirSync(env.CODEX_HOME);
  fs.writeFileSync(path.join(env.CODEX_HOME, 'config.toml'), "model = 'isolated-original'\n");
  const executable = path.join(testRoot, process.platform === 'win32' ? 'worker.exe' : 'worker');
  const go = process.env.GO_EXE || (process.platform === 'win32' ? path.join(root, '.tools/go/bin/go.exe') : 'go');
  execFileSync(go, ['build', '-trimpath', '-o', executable, './cmd/worker'], {cwd: root, env, stdio: 'pipe'});
  const mock = await upstream(), report = {groups: [], errors: [], external: [], testRoot, realGo: true, injectedBindings: false};
  let service, browser, page;
  /** 记录已通过的真实功能组，不把未运行的检查计作成功。 */
  function passed(name) {report.groups.push(name); console.log('PASS ' + name);}
  /** 新窗口沿用真实令牌并限制浏览器只访问配置服务；实际模型请求由 Go 发往本地模拟上游。 */
  async function open() {
    page = await browser.newPage({viewport: {width: 1180, height: 840}});
    page.setDefaultTimeout(12000);
    page.on('pageerror', error => report.errors.push(error.message));
    page.on('dialog', dialog => dialog.accept());
    await page.route('**/*', route => {if (new URL(route.request().url()).origin === service.origin) return route.continue(); report.external.push(route.request().url()); return route.abort();});
    await page.goto(service.url); await page.locator('.app-shell').waitFor();
    await until(() => page.locator('.loading-state').count().then(count => count === 0), '工作区初始化未完成');
  }
  try {
    service = await startService(executable, env);
    browser = await chromium.launch({headless: true, executablePath: process.env.CHROME_PATH || (process.platform === 'win32' ? 'C:/Program Files/Google/Chrome/Application/chrome.exe' : undefined)});
    const denied = await fetch(service.origin + '/api/config'); assert.equal(denied.status, 401);
    assert.deepEqual((await request(service, '/api/config')).config.models, {});
    await open(); assert.equal(await page.evaluate(() => Boolean(window.desktopApp || window.__mock)), false);
    passed('首次启动为空配置；真实 Go API 需要令牌，无模拟绑定');

    await navigate(page, '我的连接'); await page.getByRole('button', {name: '添加连接', exact: true}).click();
    await page.getByRole('textbox', {name: '连接名称', exact: true}).fill('local-one');
    await page.getByRole('textbox', {name: 'API 地址', exact: true}).fill(mock.url);
    await page.getByRole('dialog').getByRole('button', {name: '添加连接', exact: true}).click();
    await page.getByLabel('API Key', {exact: true}).fill('synthetic-local-key');
    await page.getByRole('tab', {name: '模型选择', exact: false}).click();
    await page.getByRole('button', {name: '刷新', exact: true}).click();
    await page.getByRole('checkbox', {name: '连接中选择模型 local-alpha', exact: true}).waitFor();
    await page.getByRole('checkbox', {name: '连接中选择模型 local-alpha', exact: true}).click();
    await page.getByRole('checkbox', {name: '连接中选择模型 local-beta', exact: true}).click();
    await page.getByRole('button', {name: '设为连接默认 local-alpha', exact: true}).click();
    await page.getByRole('button', {name: '模型设置 local-beta', exact: true}).click();
    await page.getByRole('textbox', {name: '显示名称（可选）', exact: true}).fill('本地模型 B');
    const contextToggle = page.getByRole('switch', {name: '设置上下文', exact: true});
    if (await contextToggle.getAttribute('aria-checked') === 'false') await contextToggle.click();
    await page.getByRole('spinbutton', {name: '上下文大小（tokens）', exact: true}).fill('96000');
    await page.getByRole('spinbutton', {name: '插件输出上限（可选）', exact: true}).fill('2048');
    await page.getByRole('combobox', {name: '图像输入', exact: true}).selectOption('no');
    await page.getByRole('switch', {name: 'Fast 模式', exact: true}).click();
    await page.getByRole('button', {name: '保存模型', exact: true}).click();
    await page.getByRole('button', {name: '手动添加', exact: true}).click();
    await page.getByRole('textbox', {name: '模型 ID', exact: true}).fill('local-manual');
    await page.getByRole('button', {name: '保存模型', exact: true}).click();
    const handle = page.getByRole('button', {name: '拖动排序 local-alpha', exact: true});
    await handle.focus(); await handle.press('Space'); await page.locator('.model-drag-preview').waitFor({state: 'visible'});
    await handle.press('ArrowDown'); await page.locator('[data-connection-model="local-beta"].is-drop-target').waitFor({state: 'visible'});
    await handle.press('Space'); await page.locator('.model-drag-preview').waitFor({state: 'detached'});
    await page.waitForFunction(() => document.querySelector('[data-connection-model]')?.dataset.connectionModel === 'local-beta');
    await saveConnection(page);
    let config = (await request(service, '/api/config')).config, oneID = config.models['local-one'].id;
    assert.equal(config.models['local-one'].model, 'local-alpha'); assert.equal(config.models['local-one'].modelOrder[0], 'local-beta');
    assert.equal(config.models['local-one'].modelCatalog.length, 3); assert.equal(config.models['local-one'].apiKey, ''); assert.equal(config.models['local-one'].hasKey, true);
    assert.equal(config.models['local-one'].modelContextWindows['local-beta'], 96000); assert.equal(config.models['local-one'].modelOfficialContexts['local-beta'], 512000);
    assert.equal(config.models['local-one'].modelOutputs['local-beta'].tokens, 2048); assert.equal(config.models['local-one'].modelImageInputs['local-beta'], false);
    assert.equal(mock.state.requests[0].authorization, 'Bearer synthetic-local-key');
    passed('真实模型目录、手动添加、选择／默认／排序、上下文／输出／图像选项、保存和 Key 脱敏');

    await page.getByRole('tab', {name: '连接信息', exact: false}).click();
    await page.getByRole('textbox', {name: '连接名称', exact: true}).fill('local-renamed');
    const arrived = deferred(), release = deferred();
    await page.route('**/api/config/save', async route => {const result = await route.fetch(); arrived.resolve(); await release.promise; await route.fulfill({response: result});});
    await page.getByRole('button', {name: '保存连接', exact: true}).click();
    await page.getByRole('button', {name: '正在保存', exact: true}).waitFor(); await arrived.promise;
    const nameDuringSave = page.getByRole('textbox', {name: '连接名称', exact: true});
    await nameDuringSave.click(); await nameDuringSave.press('ControlOrMeta+A'); await nameDuringSave.pressSequentially('local-newer-draft');
    await until(() => page.locator('.connection-workbench-editor h2').first().innerText().then(text => text.includes('local-newer-draft')), '保存等待期间的名称输入没有更新 React 草稿');
    const delivered = page.waitForResponse(value => value.url().endsWith('/api/config/save')); release.resolve(); await delivered;
    await until(() => page.locator('.connection-workbench-editor h2').first().innerText().then(text => text.includes('local-newer-draft')), '迟到的保存覆盖了新名称');
    assert.equal((await request(service, '/api/config')).config.models['local-renamed'].id, oneID);
    await page.unroute('**/api/config/save'); await saveConnection(page);
    config = (await request(service, '/api/config')).config;
    assert(config.models['local-newer-draft']); assert(!config.models['local-renamed']);
    assert.equal(JSON.parse(fs.readFileSync(path.join(env.API_SUBAGENTS_HOME, 'models.json'))).models['local-newer-draft'].apiKey, 'synthetic-local-key');
    passed('真实改名及等待期间继续编辑：保存旧提交，保留新草稿，凭据引用不丢失');

    await page.getByRole('button', {name: '复制连接 local-newer-draft', exact: true}).click();
    await page.getByRole('textbox', {name: '连接名称', exact: true}).fill('local-two'); await saveConnection(page);
    config = (await request(service, '/api/config')).config; const twoID = config.models['local-two'].id;
    assert.notEqual(oneID, twoID); assert.equal(config.models['local-two'].hasKey, true);
    await page.getByRole('textbox', {name: '连接名称', exact: true}).fill('discard-this');
    await page.getByRole('button', {name: '还原', exact: true}).click(); await page.getByRole('button', {name: '还原连接', exact: true}).click();
    assert.equal(await page.getByRole('textbox', {name: '连接名称', exact: true}).inputValue(), 'local-two');
    passed('复制是独立连接并保留凭据；还原只丢弃目标草稿');

    await navigate(page, '接入配置'); await page.getByRole('button', {name: '新建配置', exact: true}).first().click();
    await page.getByRole('textbox', {name: '配置名称', exact: true}).fill('scope-one'); await page.getByRole('button', {name: '创建配置', exact: true}).click();
    await page.getByRole('button', {name: '添加连接', exact: true}).click(); await page.getByRole('menuitem').filter({hasText: 'local-newer-draft'}).click();
    await page.getByRole('button', {name: '设为此配置默认 local-newer-draft / local-beta', exact: true}).click();
    const planArrived = deferred(), planRelease = deferred();
    await page.route('**/api/access/save', async route => {const result = await route.fetch(); planArrived.resolve(); await planRelease.promise; await route.fulfill({response: result});});
    await page.getByRole('button', {name: '仅保存', exact: true}).click(); await page.getByRole('button', {name: '正在保存', exact: true}).waitFor(); await planArrived.promise;
    await page.getByRole('textbox', {name: '配置名称', exact: true}).fill('scope-one-later-draft');
    const planResponse = page.waitForResponse(value => value.url().endsWith('/api/access/save')); planRelease.resolve(); assert.equal((await planResponse).status(), 200);
    await page.unroute('**/api/access/save');
    config = (await request(service, '/api/config')).config; const planID = config.accessConfigs[0].id;
    assert.equal(config.accessConfigs[0].name, 'scope-one');
    assert.equal(await page.getByRole('textbox', {name: '配置名称', exact: true}).inputValue(), 'scope-one-later-draft');
    assert.deepEqual(config.accessConfigs[0].connectionIDs, [oneID]); assert.equal(config.accessConfigs[0].defaultKey, JSON.stringify([oneID, 'local-beta']));
    assert.equal(config.models['local-newer-draft'].model, 'local-alpha');
    await page.getByRole('button', {name: '还原', exact: true}).click(); await page.getByRole('button', {name: '还原配置', exact: true}).click();
    await page.getByRole('button', {name: '复制配置 scope-one', exact: true}).click();
    await page.getByRole('checkbox', {name: '配置中选择 local-newer-draft / local-manual', exact: true}).click();
    await page.getByRole('button', {name: '设为此配置默认 local-newer-draft / local-alpha', exact: true}).click();
    const copyPlanResponse = page.waitForResponse(value => value.url().endsWith('/api/access/save'));
    await page.getByRole('button', {name: '仅保存', exact: true}).click(); assert.equal((await copyPlanResponse).status(), 200);
    config = (await request(service, '/api/config')).config;
    assert.equal(config.accessConfigs.length, 2); assert.notEqual(config.accessConfigs[0].id, config.accessConfigs[1].id);
    const originalPlan = config.accessConfigs.find(value => value.id === planID), copiedPlan = config.accessConfigs.find(value => value.id !== planID);
    assert.equal(originalPlan.modelIDs[oneID].length, 3); assert.equal(copiedPlan.modelIDs[oneID].length, 2);
    assert.equal(originalPlan.defaultKey, JSON.stringify([oneID, 'local-beta'])); assert.equal(copiedPlan.defaultKey, JSON.stringify([oneID, 'local-alpha']));
    await page.getByRole('button', {name: '启用此配置', exact: true}).waitFor();
    await page.getByRole('button', {name: '启用此配置', exact: true}).click(); await page.getByRole('alert').filter({hasText: '桌面程序'}).waitFor();
    assert.equal(fs.readFileSync(path.join(env.CODEX_HOME, 'config.toml'), 'utf8'), "model = 'isolated-original'\n");
    passed('配置保存保留后续草稿；复制配置的模型范围和默认独立落盘，浏览器不假装接入');

    await navigate(page, '用量统计'); await page.getByRole('tab', {name: '价格统计', exact: true}).click();
    const price = page.getByRole('spinbutton', {name: 'local-alpha 普通输入单价', exact: true}).first();
    await price.fill('1.5');
    await page.getByRole('tab', {name: '概览', exact: true}).click(); await navigate(page, '我的连接'); await navigate(page, '用量统计');
    await page.getByRole('tab', {name: '价格统计', exact: true}).click(); assert.equal(await price.inputValue(), '1.5');
    const usage = await request(service, '/api/usage', {}), key = JSON.stringify([oneID, 'local-alpha']), hidden = JSON.stringify(['deleted-connection', 'historical-model']);
    await request(service, '/api/usage/prices', {revision: usage.prices.revision, rates: {[key]: {input: 3, output: 4, read: 0, write: 0}, [hidden]: {input: 7, output: 8, read: 1, write: 2}}});
    const refreshed = page.waitForResponse(value => value.url().endsWith('/api/usage'));
    await page.getByRole('button', {name: '刷新用量记录', exact: true}).click(); await refreshed;
    await page.getByRole('button', {name: '载入最新费率', exact: true}).waitFor(); assert.equal(await price.inputValue(), '1.5');
    await page.getByRole('button', {name: '保存费率', exact: true}).click(); assert.match(await page.locator('#usage-price-error').innerText(), /草稿已保留/);
    await page.getByRole('button', {name: '载入最新费率', exact: true}).click(); await page.getByRole('button', {name: '保留草稿', exact: true}).click(); assert.equal(await price.inputValue(), '1.5');
    await page.getByRole('button', {name: '载入最新费率', exact: true}).click(); await page.getByRole('button', {name: '放弃草稿并载入', exact: true}).click(); assert.equal(await price.inputValue(), '3');
    await price.fill('2');
    const priceResponse = page.waitForResponse(value => value.url().endsWith('/api/usage/prices'));
    await page.getByRole('button', {name: '保存费率', exact: true}).click(); assert.equal((await priceResponse).status(), 200);
    const savedPrices = (await request(service, '/api/usage', {})).prices;
    assert.equal(savedPrices.rates[key].input, 2); assert.equal(savedPrices.rates[hidden].input, 7);
    passed('费率跨页签保留草稿、外部修订冲突、确认恢复和真实独立存储；不删除隐藏费率');

    const readArrived = deferred(), readRelease = deferred(); let holdRead = true;
    await page.route('**/api/usage', async route => {
      if (!holdRead) {await route.continue(); return;}
      holdRead = false; const result = await route.fetch(); readArrived.resolve(); await readRelease.promise; await route.fulfill({response: result});
    });
    await page.getByRole('button', {name: '刷新用量记录', exact: true}).click(); await readArrived.promise;
    await request(service, '/api/usage/prices', {revision: savedPrices.revision, rates: {...savedPrices.rates, [key]: {input: 5, output: 4, read: 0, write: 0}}});
    const newerRead = page.waitForResponse(value => value.url().endsWith('/api/usage'));
    await page.getByRole('button', {name: '刷新用量记录', exact: true}).click(); await newerRead;
    await until(() => price.inputValue().then(value => value === '5'), '最新读取未更新权威费率');
    const olderRead = page.waitForResponse(value => value.url().endsWith('/api/usage')); readRelease.resolve(); await olderRead;
    await page.unroute('**/api/usage');
    assert.equal(await price.inputValue(), '5');
    await price.fill('2'); const restorePrice = page.waitForResponse(value => value.url().endsWith('/api/usage/prices'));
    await page.getByRole('button', {name: '保存费率', exact: true}).click(); assert.equal((await restorePrice).status(), 200);
    passed('延迟旧的真实读取后发起新读取，旧响应不能回退费率或重置已保存表单');

    // 真实 Go 按精确模型 ID 提供官方默认，未选目录模型不补价；不调用官网或付费接口。
    const beforeDefaults = (await request(service, '/api/config')).config;
    const priceProfile = {...beforeDefaults.models['local-two'], relayModels: [...(beforeDefaults.models['local-two'].relayModels || []), 'gpt-5.4'], modelCatalog: [...(beforeDefaults.models['local-two'].modelCatalog || []), {id: 'gpt-5.4'}, {id: 'gpt-6-astra'}]};
    await request(service, '/api/config/save', {name: 'local-two', config: {...beforeDefaults, models: {...beforeDefaults.models, 'local-two': priceProfile}}});
    const defaultPrices = (await request(service, '/api/usage', {})).prices;
    const official = defaultPrices.rates[JSON.stringify([twoID, 'gpt-5.4'])];
    assert.equal(official.input, 2.5); assert.equal(official.fast.input, 5); assert.equal(official.checkedAt, '2026-10-14');
    assert.equal(Object.hasOwn(defaultPrices.rates, JSON.stringify([twoID, 'gpt-6-astra'])), false);
    passed('真实 Go 返回已选模型官方普通与 Fast 默认费率，不补未选目录或私有别名');

    await navigate(page, '我的连接'); await page.getByRole('button', {name: '管理连接 local-newer-draft', exact: true}).click();
    await page.getByRole('button', {name: '测试连接', exact: true}).click(); await page.getByRole('status').filter({hasText: '连接测试成功'}).waitFor();
    const ledger = await request(service, '/api/usage', {}); assert.equal(ledger.records.length, 1); assert.equal(ledger.records[0].source, 'test'); assert.equal(ledger.records[0].input, 100); assert.equal(ledger.records[0].output, 20); assert.equal(ledger.records[0].costMicroUSD, 280);
    mock.state.slow = true; await page.getByRole('button', {name: '测试连接', exact: true}).click();
    await until(() => mock.state.requests.filter(value => value.path === '/v1/responses').length === 2, '测试请求未到达真实上游');
    await page.getByRole('button', {name: '取消', exact: true}).click(); await page.getByRole('status').filter({hasText: '已取消'}).waitFor();
    await until(() => mock.state.aborted === 1, '取消未传递到 Go 和上游'); mock.state.slow = false;
    await navigate(page, '用量统计'); await page.getByRole('tab', {name: '概览', exact: true}).click();
    await page.locator('.u-kpi strong').first().waitFor(); assert.equal(await page.locator('.u-kpi strong').first().innerText(), '0');
    await page.getByRole('button', {name: '用量来源', exact: true}).click(); await page.getByRole('menuitemradio', {name: '连接测试', exact: true}).click();
    await page.getByRole('tab', {name: '请求明细', exact: true}).click(); assert.match(await page.locator('.usage-reference').innerText(), /local-alpha/);
    passed('真实测试请求、Token 与历史计价、测试来源隔离；取消穿过浏览器、Go 到达上游');

    await page.close(); service.process.kill(); await new Promise(resolve => service.process.once('exit', resolve)); service = await startService(executable, env);
    await open(); config = (await request(service, '/api/config')).config;
    assert.equal(config.models['local-newer-draft'].id, oneID); assert.equal(config.models['local-two'].id, twoID); assert(config.accessConfigs.some(value => value.id === planID));
    assert.equal((await request(service, '/api/usage', {})).prices.rates[key].input, 2);
    await navigate(page, '我的连接'); await page.getByRole('button', {name: '管理连接 local-newer-draft', exact: true}).click();
    await page.getByRole('button', {name: '删除连接 local-newer-draft', exact: true}).click(); await page.getByRole('button', {name: '取消', exact: true}).click();
    assert((await request(service, '/api/config')).config.models['local-newer-draft']);
    await page.getByRole('button', {name: '删除连接 local-newer-draft', exact: true}).click();
    const removed = page.waitForResponse(value => value.url().endsWith('/api/config/remove'));
    await page.getByRole('button', {name: '删除连接', exact: true}).click(); assert.equal((await removed).status(), 200);
    config = (await request(service, '/api/config')).config;
    assert(!config.models['local-newer-draft']); assert(config.models['local-two']); assert(config.accessConfigs.every(value => value.connectionIDs.length === 0));
    assert.equal(fs.readFileSync(path.join(env.CODEX_HOME, 'config.toml'), 'utf8'), "model = 'isolated-original'\n");
    passed('真实服务重启后恢复连接、独立配置、模型排序、用量和费率；确认删除清理引用');
    assert.deepEqual(report.errors, []); assert.deepEqual(report.external, []);
    report.localUpstreamRequests = mock.state.requests.length;
    fs.writeFileSync(path.join(testRoot, 'report.json'), JSON.stringify(report, null, 2));
    console.log(`Verified ${report.groups.length} real Go/browser groups: ${testRoot}`);
  } catch (error) {
    if (page && !page.isClosed()) await page.screenshot({path: path.join(testRoot, 'failure.png'), fullPage: true});
    fs.writeFileSync(path.join(testRoot, 'failure.json'), JSON.stringify({...report, failure: error.stack}, null, 2)); throw error;
  } finally {
    if (browser) await browser.close(); if (service) service.process.kill();
    mock.server.closeAllConnections(); await new Promise(resolve => mock.server.close(resolve));
  }
}
main().catch(error => {console.error(error); process.exitCode = 1;});
