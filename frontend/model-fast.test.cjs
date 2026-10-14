const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const {chromium} = require('playwright');
const {mockBindings} = require('./tests/mock-bindings.cjs');

/** 只提供正式静态资源白名单，所有绑定与价格均来自合成内存数据，不触碰真实连接。 */
async function serve() {
  const names = ['index.html', 'workbench-app.js', 'workbench-app.css', 'bridge.js'];
  const server = http.createServer((req, res) => {
    const name = new URL(req.url, 'http://localhost').pathname.slice(1) || 'index.html';
    if (!names.includes(name)) {res.writeHead(404); res.end(); return;}
    res.setHeader('Content-Type', name.endsWith('.js') ? 'text/javascript' : name.endsWith('.css') ? 'text/css' : 'text/html');
    res.end(fs.readFileSync(path.join(__dirname, name)));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return {server, origin: `http://127.0.0.1:${server.address().port}`};
}

/** 从用户可见导航进入页面；窄屏走导航弹窗，不使用内部控制器或假造点击。 */
async function navigate(page, label) {
  const toggle = page.getByRole('button', {name: '打开导航', exact: true});
  if (await toggle.isVisible()) {await toggle.click(); await page.getByRole('dialog').getByRole('button', {name: new RegExp('^' + label)}).click();}
  else await page.locator('.sidebar .primary-nav').getByRole('button', {name: new RegExp('^' + label)}).click();
}

/** 验证真实工作区的 Fast 保存／取消、雷电、Ultra 移除、价格范围、精度和移动布局，阻断所有外部请求。 */
async function run() {
  const {fixtureConfig, fixtureState} = await import('./tests/fixtures.mjs');
  const config = fixtureConfig(), key = JSON.stringify(['cpa', 'gpt-5-extra']), hidden = JSON.stringify(['deleted', 'historical']);
  const fixture = {config, state: fixtureState(config), usageRecords: [{id: 'old', logicalId: 'old', attempt: 1, connectionId: 'deleted', connection: '已删除连接', model: 'historical', source: 'main', startedAt: Date.now()-1000, outcome: 'success', quality: 'complete', input: 10, output: 2, total: 12, costMicroUSD: 12}], usagePrices: {revision: '', rates: {[key]: {input: 2.5, output: 15, read: .25, write: 2.5, fast: {input: 5, output: 30, read: .5, write: 5}}, [hidden]: {input: 7, output: 8, read: 1, write: 2}}}};
  fixture.state.update.releaseNotes = '本地合成版本说明，用于验证长内容仅在弹窗正文滚动。\n'.repeat(80);
  const {server, origin} = await serve(), browser = await chromium.launch({headless: true, executablePath: process.env.CHROME_PATH || (process.platform === 'win32' ? 'C:/Program Files/Google/Chrome/Application/chrome.exe' : undefined)});
  const output = path.resolve(__dirname, '../output/production-model-fast-2026-10-14'); fs.mkdirSync(output, {recursive: true});
  const report = {checks: [], errors: [], external: [], layouts: []};
  try {
    const page = await browser.newPage({viewport: {width: 1230, height: 884}, reducedMotion: 'reduce'});
    page.on('pageerror', error => report.errors.push(error.message));
    await page.route('**/*', route => {if (route.request().url().startsWith(origin)) return route.continue(); report.external.push(route.request().url()); return route.abort();});
    await page.addInitScript(mockBindings, fixture); await page.goto(origin); await page.waitForFunction(() => window.__mock?.ready?.configLoaded);
    await navigate(page, '我的连接'); await page.getByRole('button', {name: '管理连接 cpa', exact: true}).click();
    await page.getByRole('tab', {name: '模型选择', exact: false}).click();
    assert.equal(await page.getByRole('button', {name: '极致', exact: true}).count(), 0);
    await page.getByRole('button', {name: '模型设置 gpt-5-extra', exact: true}).click();
    const toggle = page.getByRole('switch', {name: 'Fast 模式', exact: true});
    assert.equal(await toggle.getAttribute('aria-checked'), 'false'); await toggle.click(); await page.keyboard.press('Escape');
    assert.equal(await page.locator('.connection-model-name .model-fast-mark').count(), 0);
    await page.getByRole('button', {name: '模型设置 gpt-5-extra', exact: true}).click(); await toggle.click();
    await page.getByRole('button', {name: '保存模型', exact: true}).click();
    assert.equal(await page.locator('[data-connection-model="gpt-5-extra"] .model-fast-mark').count(), 1);
    assert.equal((await page.evaluate(() => window.__mock.snapshot().saved.models.cpa.modelFastModes))?.['gpt-5-extra'], undefined);
    await page.getByRole('button', {name: '保存连接', exact: true}).click(); await page.locator('.toast').filter({hasText: '连接已保存'}).waitFor();
    assert.equal(await page.evaluate(() => window.__mock.snapshot().saved.models.cpa.modelFastModes['gpt-5-extra']), true);
    assert.ok(await page.locator('.toast-region').evaluate(node => node.getBoundingClientRect().top < 80));
    await navigate(page, '接入配置'); assert.equal(await page.locator('.scope-model-name .model-fast-mark').count(), 1);
    const height = await page.evaluate(() => ({scroll: document.documentElement.scrollHeight, viewport: innerHeight})); assert.ok(height.scroll <= height.viewport + 1);
    await page.screenshot({path: path.join(output, '01-plans-fast.png')}); report.checks.push('Fast 默认关闭、取消隔离、两阶段保存、两处雷电、Ultra 隐藏与桌面自适应');

    await navigate(page, '我的连接'); await page.getByRole('tab', {name: '模型选择', exact: false}).click();
    await page.getByRole('button', {name: '模型设置 gpt-5-extra', exact: true}).click(); await toggle.click(); await page.getByRole('button', {name: '保存模型', exact: true}).click();
    await navigate(page, '接入配置');
    const enable = page.getByRole('button', {name: '启用此配置', exact: true});
    const beforeEnable = await page.evaluate(() => window.__mock.snapshot().calls.filter(call => call.enableAccess).length);
    await enable.click(); await page.getByRole('alertdialog').filter({hasText: '暂时无法启用配置'}).waitFor();
    assert.match(await page.getByRole('alertdialog').innerText(), /未保存的连接修改/);
    assert.equal(await page.evaluate(() => window.__mock.snapshot().calls.filter(call => call.enableAccess).length), beforeEnable);
    await page.keyboard.press('Escape'); assert.equal(await enable.evaluate(node => node === document.activeElement), true);
    report.checks.push('未保存连接通过提示弹窗拦截启用，不隐式保存，Escape 恢复触发点');

    await navigate(page, '用量统计'); await page.getByRole('tab', {name: '价格统计', exact: true}).click();
    const expected = Object.values(config.models).reduce((sum, profile) => sum + 1 + (profile.relayModels || []).length, 0);
    assert.equal(await page.locator('.u-prices fieldset').count(), expected);
    assert.equal(await page.locator('.u-prices legend').filter({hasText: 'historical'}).count(), 0);
    assert.equal(await page.locator('.u-prices legend').filter({hasText: 'code-helper'}).count(), 0);
    const fastRead = page.getByRole('spinbutton', {name: 'gpt-5-extra Fast 模式 缓存读取单价', exact: true});
    assert.equal(await fastRead.inputValue(), '0.5'); await fastRead.fill('0.000125');
    await page.getByRole('button', {name: '保存费率', exact: true}).click(); await page.getByRole('status').filter({hasText: '费率已保存'}).waitFor();
    const priceCall = await page.evaluate(() => window.__mock.snapshot().calls.filter(call => call.route === '/api/usage/prices').at(-1));
    const payload = typeof priceCall.body === 'string' ? JSON.parse(priceCall.body) : priceCall.body;
    assert.equal(payload.rates[key].fast.read, .000125); assert.equal(payload.rates[key].input, 2.5); assert.equal(payload.rates[hidden].input, 7);
    await page.evaluate(() => {document.scrollingElement.scrollTop = 0;});
    await page.screenshot({path: path.join(output, '02-prices-fast.png')}); report.checks.push('价格仅含已配置模型、Fast 独立编辑、高精度小数、隐藏历史价格保留');

    for (const viewport of [{width: 375, height: 812}, {width: 884, height: 375}]) {
      await page.setViewportSize(viewport); await navigate(page, '我的连接');
      await page.getByRole('tab', {name: '模型选择', exact: false}).click(); await page.getByRole('button', {name: '模型设置 gpt-5-extra', exact: true}).click();
      const shape = await page.evaluate(() => ({width: innerWidth, scroll: document.documentElement.scrollWidth, bottom: document.querySelector('.model-settings-form .dialog-actions').getBoundingClientRect().bottom, viewport: innerHeight, body: document.querySelector('.model-settings-form .dialog-body').getBoundingClientRect().height, headingTop: document.querySelector('.dialog-heading').getBoundingClientRect().top, toastLayer: Number(getComputedStyle(document.querySelector('.toast-region')).zIndex), overlayLayer: Number(getComputedStyle(document.querySelector('.dialog-overlay')).zIndex)}));
      assert.ok(shape.scroll <= shape.width + 1); assert.ok(shape.bottom <= shape.viewport + 1); assert.ok(shape.body > 0); report.layouts.push(shape);
      assert.ok(shape.headingTop >= 0); assert.ok(shape.toastLayer < shape.overlayLayer);
      await page.screenshot({path: path.join(output, `03-model-${viewport.width}x${viewport.height}.png`)}); await page.keyboard.press('Escape');
      await page.locator('.update-trigger:visible').click(); await page.getByRole('button', {name: '取消操作', exact: true}).waitFor();
      const updateShape = await page.locator('.update-panel').evaluate(panel => {
        const body = panel.querySelector('.update-panel-body'), actions = panel.querySelector('.dialog-actions'), preference = panel.querySelector('.update-preference');
        const contentBottom = Math.max(...[...preference.children].map(node => node.getBoundingClientRect().bottom));
        return {type: 'update', width: innerWidth, scroll: document.documentElement.scrollWidth, bottom: actions.getBoundingClientRect().bottom, viewport: innerHeight, body: body.clientHeight, scrollable: body.scrollHeight > body.clientHeight, gap: actions.getBoundingClientRect().top - contentBottom};
      });
      assert.ok(updateShape.scroll <= viewport.width + 1); assert.ok(updateShape.bottom <= viewport.height + 1);
      assert.ok(updateShape.body > 0 && updateShape.scrollable); assert.ok(updateShape.gap >= 20); report.layouts.push(updateShape);
      const close = page.getByRole('button', {name: '关闭弹窗', exact: true});
      assert.ok((await close.boundingBox()).y >= 0);
      await page.screenshot({path: path.join(output, `04-update-${viewport.width}x${viewport.height}.png`)});
      await page.getByRole('button', {name: '取消操作', exact: true}).click(); await close.click();
    }
    report.checks.push('窄屏与横屏 Fast 弹窗：标题与操作固定、表单可滚动、无横向溢出、降低动态效果，置顶提示不遮挡标题');
    report.checks.push('长更新说明仅正文滚动，窄屏和横屏关闭、偏好与操作保持可达且间距至少 20px');
    await page.setViewportSize({width: 1230, height: 884});
    await page.locator('.update-trigger:visible').click(); await page.getByRole('button', {name: '取消操作', exact: true}).waitFor();
    await page.evaluate(() => window.__mock.completeUpdate()); await page.getByRole('button', {name: '下载更新', exact: true}).click();
    await page.getByRole('progressbar', {name: '应用下载进度', exact: true}).waitFor(); await page.evaluate(() => window.__mock.completeUpdate());
    const install = page.getByRole('button', {name: '重启并安装', exact: true}); await install.waitFor(); assert.equal(await install.isDisabled(), true);
    assert.match(await page.locator('.update-panel').innerText(), /请先保存或还原草稿/);
    await page.getByRole('button', {name: '关闭弹窗', exact: true}).click();
    await navigate(page, '插件管理'); await page.locator('.plugin-actions').getByRole('button', {name: '检查更新', exact: true}).click();
    await page.getByRole('button', {name: '正在检查', exact: true}).waitFor(); assert.equal(await page.locator('.plugin-actions .spinner').count(), 1);
    assert.equal(await page.locator('.plugin-install .job-inline').count(), 0); assert.match(await page.locator('.plugin-title').innerText(), /处理中/);
    await page.getByRole('button', {name: '取消', exact: true}).click();
    assert.equal(await page.evaluate(() => window.__mock.snapshot().state.update.phase), 'downloaded');
    report.checks.push('草稿阻止安装，插件检查不重复展示状态行且取消不改变应用更新状态');
    assert.deepEqual(report.errors, []); assert.deepEqual(report.external, []);
    fs.writeFileSync(path.join(output, 'report.json'), JSON.stringify({...report, passed: true}, null, 2));
    console.log(JSON.stringify({checks: report.checks.length, layouts: report.layouts.length, errors: report.errors, external: report.external}));
  } finally {await browser.close(); await new Promise(resolve => server.close(resolve));}
}

run().catch(error => {console.error(error); process.exitCode = 1;});
