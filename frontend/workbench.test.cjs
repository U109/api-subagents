// 正式前端浏览器回归：全部绑定使用合成内存数据，阻断任何非本地请求。
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const assert = require('node:assert/strict');
const {pathToFileURL} = require('node:url');
const {chromium} = require('playwright');
const {mockBindings} = require('./tests/mock-bindings.cjs');
const assets = ['index.html', 'workbench-app.js', 'workbench-app.css', 'bridge.js'];
const output = path.resolve(process.env.WORKBENCH_TEST_OUTPUT || path.join(__dirname, '../output/approved-implementation-qa'));

/** 仅提供正式嵌入白名单资源，测试服务器没有任何模型或文件操作接口。 */
async function serve() {
  const server = http.createServer((req, res) => {
    const name = new URL(req.url, 'http://localhost').pathname.slice(1) || 'index.html';
    if (!assets.includes(name)) {res.writeHead(404); res.end(); return;}
    res.setHeader('Content-Type', name.endsWith('.css') ? 'text/css' : name.endsWith('.js') ? 'text/javascript' : 'text/html');
    res.end(fs.readFileSync(path.join(__dirname, name)));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return {server, origin: `http://127.0.0.1:${server.address().port}`};
}
/** 创建带真实资源与隔离 Go 绑定的页面，记录脚本错误及非本地请求。 */
async function createPage(browser, origin, fixture, report, viewport = {width: 1204, height: 884}) {
  const page = await browser.newPage({viewport});
  page.on('pageerror', error => report.errors.push(error.message));
  await page.route('**/*', route => {if (route.request().url().startsWith(origin)) return route.continue(); report.external.push(route.request().url()); return route.abort();});
  await page.addInitScript(mockBindings, fixture);
  await page.goto(origin); await page.waitForFunction(() => window.__mock?.ready?.configLoaded); await page.evaluate(() => document.fonts.ready);
  return page;
}
/** 查看连接仅通过公开导航进入，不访问前端内部状态或改变接入配置。 */
async function openConnection(page, id = 'cpa', tab = '连接信息') {
  const nav = page.locator('.sidebar .nav-button').filter({hasText: '我的连接'});
  if (await nav.isVisible()) await nav.click(); else {await page.getByRole('button', {name: '打开导航', exact: true}).click(); await page.locator('.mobile-sidebar .nav-button').filter({hasText: '我的连接'}).click();}
  await page.getByRole('button', {name: `管理连接 ${id}`, exact: true}).click();
  await page.getByRole('tab', {name: tab, exact: false}).click();
}
/** 返回仅属于测试的保存和运行副本。 */
async function snapshot(page) {return page.evaluate(() => window.__mock.snapshot());}
/** 检查视口横向适配及控制器错误，长表格只能纵向增长。 */
async function layout(page, label, report) {
  const result = await page.evaluate(() => ({viewport: innerWidth, document: document.documentElement.scrollWidth, body: document.body.scrollWidth, invalidNesting: document.querySelectorAll('button button').length}));
  assert(result.document <= result.viewport + 1, `${label}: horizontal overflow ${JSON.stringify(result)}`); assert.equal(result.invalidNesting, 0);
  report.layouts.push({label, ...result});
}
/** 截图只包含合成配置，用于人工复核所有页面及关键状态。 */
async function shot(page, name, report) {await page.screenshot({path: path.join(output, name + '.png'), fullPage: true}); report.screenshots.push(name + '.png');}
/** 按原型语义验证配置作用域、持久化、拖动、取消、弹窗和响应式布局。 */
async function main() {
  fs.mkdirSync(output, {recursive: true});
  const {fixtureConfig, fixtureState} = await import(pathToFileURL(path.join(__dirname, 'tests/fixtures.mjs')));
  const fixture = {config: fixtureConfig(), state: fixtureState()};
  const {server, origin} = await serve();
  const browser = await chromium.launch({headless: true, executablePath: process.env.CHROME_PATH || (process.platform === 'win32' ? 'C:/Program Files/Google/Chrome/Application/chrome.exe' : undefined)});
  const report = {groups: [], layouts: [], screenshots: [], errors: [], external: []};
  let page;
  /** 每组使用独立页面，测试副作用不会污染下一组。 */
  async function check(name, test, data = fixture) {if (page) await page.close(); page = await createPage(browser, origin, data, report); await test(page); report.groups.push(name); console.log('PASS ' + name);}
  try {
    const emptyConfig = {version: 1, models: {}, accessConfigs: []};
    await check('首次启动为空工作区，单一创建入口且不会自动开启接入', async page => {
      assert.equal(await page.locator('.scope-model-row, [data-directory-connection]').count(), 0);
      const toggle = page.getByRole('switch', {name: '主对话接入', exact: true});
      assert.equal(await toggle.getAttribute('aria-checked'), 'false');
      await toggle.click();
      await page.getByText('先选择配置，点击「启用此配置」。', {exact: true}).waitFor({state: 'visible'});
      assert.equal((await snapshot(page)).state.relay.enabled, false);
      await page.locator('.sidebar .nav-button').filter({hasText: '我的连接'}).click();
      assert.equal(await page.locator('#main').getByRole('button', {name: '添加连接', exact: true}).count(), 1);
      await page.locator('#main').getByRole('button', {name: '添加连接', exact: true}).click();
      assert.equal(await page.getByRole('dialog').count(), 1);
      assert.equal(await page.getByRole('textbox', {name: '连接名称', exact: true}).inputValue(), '');
      await page.getByRole('button', {name: '取消', exact: true}).click();
      assert.deepEqual((await snapshot(page)).saved.models, {});
      await layout(page, 'first-use-empty', report);
    }, {config: emptyConfig, state: {...fixtureState(emptyConfig), relay: {enabled: false}}});
    const manyConfig = fixtureConfig(24);
    await check('多配置分页、名称搜索和查看均不改变运行范围', async page => {
      assert.equal(await page.locator('.plan-list [data-plan-id]').count(), 20);
      await page.getByRole('button', {name: '下一页配置', exact: true}).click();
      assert.equal(await page.locator('.plan-list [data-plan-id]').count(), 4);
      assert.match(await page.locator('.plan-list-footer').innerText(), /21–24 \/ 24/);
      await page.getByRole('searchbox', {name: '搜索配置或连接', exact: true}).fill('代码审查');
      const names = await page.locator('.plan-list-title strong').allTextContents();
      assert.equal(names.length, 3); assert(names.every(name => name.includes('代码审查')));
      await page.locator('.plan-list [data-plan-id]').first().click();
      assert.equal((await snapshot(page)).state.relay.access.id, 'plan-a');
      await page.getByRole('searchbox', {name: '搜索配置或连接', exact: true}).fill('不存在的配置');
      assert.equal(await page.locator('.plan-list [data-plan-id]').count(), 0);
      await page.locator('.plan-list .empty').getByRole('button', {name: '清空搜索', exact: true}).click();
      assert.equal(await page.locator('.plan-list [data-plan-id]').count(), 20);
      await layout(page, 'many-plans-search', report);
    }, {config: manyConfig, state: fixtureState(manyConfig)});
    const longConfig = fixtureConfig(), longName = '这是用于验证超长连接名称与右端菜单对齐的测试连接'.repeat(3);
    longConfig.models[longName] = longConfig.models.cpa; delete longConfig.models.cpa;
    longConfig.accessConfigs[0].name = '这是一份很长的接入配置名称用于检查标签对齐'.repeat(2);
    longConfig.lastAccess = JSON.parse(JSON.stringify(longConfig.accessConfigs[0]));
    await check('超长名称不挤出布局，连接更多菜单靠右且 Escape 恢复焦点', async page => {
      await layout(page, 'long-plan-name', report);
      await page.locator('.sidebar .nav-button').filter({hasText: '我的连接'}).click();
      await layout(page, 'long-connection-name', report);
      const trigger = page.locator('[data-directory-connection="cpa"] .icon-button'), anchor = await trigger.boundingBox();
      await trigger.click(); const menu = page.getByRole('menu'); await menu.waitFor({state: 'visible'});
      const bounds = await menu.boundingBox();
      assert(Math.abs(bounds.x + bounds.width - anchor.x - anchor.width) <= 1, 'connection menu must align to its right-side trigger');
      assert(bounds.x >= 0 && bounds.x + bounds.width <= 1204);
      await page.keyboard.press('Escape'); assert.equal(await trigger.evaluate(node => node === document.activeElement), true);
      await page.locator('[data-directory-connection="cpa"] .connection-directory-open').click();
      await layout(page, 'long-connection-detail', report);
    }, {config: longConfig, state: fixtureState(longConfig)});
    await check('接入工作台、固定标题、无连接子导航/多余上下文', async page => {
      assert.equal(await page.locator('.scope-model-row').count(), 8); assert.equal(await page.locator('.scope-model-row .capacity').count(), 0); assert.equal(await page.locator('.sidebar .nav-button').count(), 4);
      assert.equal(await page.locator('.heading-actions').getByRole('button', {name: '添加连接'}).count(), 0);
      assert.equal(await page.locator('.save-message').count(), 0);
      const before = await page.locator('.access-status-copy').evaluate(el => ({text: el.innerText, rect: el.getBoundingClientRect().toJSON()}));
      await page.getByRole('switch', {name: '主对话接入', exact: true}).click();
      await page.waitForFunction(() => !window.__mock.snapshot().state.relay.enabled);
      const after = await page.locator('.access-status-copy').evaluate(el => ({text: el.innerText, rect: el.getBoundingClientRect().toJSON()}));
      assert.deepEqual(after, before); await shot(page, '1204-access-off', report);
    });
    await check('查看 B 和关闭重开不切换 A 的运行范围', async page => {
      await page.locator('[data-plan-id="plan-b"]').click();
      assert.equal((await snapshot(page)).state.relay.access.id, 'plan-a');
      await page.getByRole('switch', {name: '主对话接入', exact: true}).click(); await page.waitForFunction(() => !window.__mock.snapshot().state.relay.enabled);
      await page.getByRole('switch', {name: '主对话接入', exact: true}).click(); await page.waitForFunction(() => window.__mock.snapshot().state.relay.enabled);
      const value = await snapshot(page); assert.equal(value.state.relay.access.id, 'plan-a'); assert(value.state.relay.access.connectionIDs.includes('worker')); assert(!value.state.relay.access.connectionIDs.includes('review'));
    });
    await check('预览选择只改当前配置，仅保存不接入，明确启用才更新', async page => {
      await page.locator('[data-plan-id="plan-b"]').click(); const before = await snapshot(page);
      await page.getByRole('checkbox', {name: '配置中选择 cpa / claude-4.7-sonnet', exact: true}).click();
      await page.getByRole('button', {name: '仅保存', exact: true}).click();
      await page.waitForFunction(() => window.__mock.snapshot().saved.accessConfigs.find(p => p.id === 'plan-b').modelIDs.cpa.length === 1);
      let value = await snapshot(page); assert.deepEqual(value.saved.models, before.saved.models); assert.equal(value.state.relay.access.id, 'plan-a');
      await page.getByRole('button', {name: '启用此配置', exact: true}).click(); await page.waitForFunction(() => window.__mock.snapshot().state.relay.access.id === 'plan-b');
      value = await snapshot(page); assert.deepEqual(value.state.relay.access.connectionIDs, ['cpa', 'review']); assert.equal(value.state.relay.access.modelIDs.cpa.length, 1); assert.deepEqual(value.saved.models, before.saved.models);
    });
    await check('失败保存保留草稿并可重试，错误不只在 toast', async page => {
      await page.getByRole('textbox', {name: '配置名称', exact: true}).fill('修改后的 A');
      await page.evaluate(() => {window.__mock.failure = '/api/access/save';});
      await page.getByRole('button', {name: '仅保存', exact: true}).click(); await page.getByText('synthetic operation failed', {exact: true}).waitFor();
      assert.equal(await page.getByRole('textbox', {name: '配置名称', exact: true}).inputValue(), '修改后的 A');
      await page.getByRole('button', {name: '仅保存', exact: true}).click(); await page.waitForFunction(() => window.__mock.snapshot().saved.accessConfigs[0].name === '修改后的 A');
    });
    await check('配置默认模型独立且移除默认需显式星标', async page => {
      await page.locator('[data-plan-id="plan-b"]').click();
      await page.getByRole('button', {name: '设为此配置默认 review / review-model', exact: true}).click();
      await page.getByRole('button', {name: '仅保存', exact: true}).click(); await page.waitForFunction(() => window.__mock.snapshot().saved.accessConfigs[1].defaultKey === '["review","review-model"]');
      assert.equal((await snapshot(page)).saved.models.cpa.model, 'gpt-5-extra');
      await page.getByRole('checkbox', {name: '配置中选择 review / review-model', exact: true}).click();
      assert.equal(await page.getByRole('button', {name: '保存并启用', exact: true}).isDisabled(), true);
      assert.equal(await page.locator('.inline-error').count(), 1);
    });
    await check('连接定点保存、改名、Key 脱敏及引用不变', async page => {
      await openConnection(page); assert.equal(await page.getByLabel('API Key', {exact: true}).inputValue(), '');
      assert.equal(await page.locator('input[autocomplete="new-password"]').inputValue(), '');
      await page.getByRole('textbox', {name: '连接名称', exact: true}).fill('新的 cpa');
      await page.getByRole('button', {name: '保存连接', exact: true}).click(); await page.waitForFunction(() => Boolean(window.__mock.snapshot().saved.models['新的 cpa']));
      const value = await snapshot(page); assert(!value.saved.models.cpa); assert.equal(value.saved.models['新的 cpa'].apiKey, 'synthetic-fixture-secret'); assert.equal(value.saved.models['新的 cpa'].id, 'cpa'); assert(value.saved.accessConfigs.every(p => p.connectionIDs.includes('cpa')));
    });
    await check('复制连接先是独立草稿，不覆盖原连接或暴露 Key', async page => {
      await openConnection(page); await page.getByRole('button', {name: '复制连接 cpa', exact: true}).click();
      assert.equal(Object.keys((await snapshot(page)).saved.models).length, 3);
      assert.equal(await page.getByRole('textbox', {name: '连接名称', exact: true}).inputValue(), 'cpa 副本');
      await page.getByRole('button', {name: '保存连接', exact: true}).click(); await page.waitForFunction(() => Object.keys(window.__mock.snapshot().saved.models).length === 4);
      const value = await snapshot(page); assert.notEqual(value.saved.models['cpa 副本'].id, value.saved.models.cpa.id); assert.equal(value.saved.models['cpa 副本'].apiKey, 'synthetic-fixture-secret');
    });
    await check('模型上下文开关、原生容量、模型设置取消隔离', async page => {
      await openConnection(page, 'cpa', '模型选择'); await page.getByRole('button', {name: '模型设置 gpt-5-extra', exact: true}).click();
      assert.equal(await page.getByRole('spinbutton', {name: '上下文大小（tokens）', exact: true}).inputValue(), '256000');
      await page.getByRole('switch', {name: '设置上下文', exact: true}).click(); assert.equal(await page.getByRole('spinbutton', {name: '上下文大小（tokens）', exact: true}).isDisabled(), true);
      await page.getByRole('button', {name: '保存模型', exact: true}).click(); await page.getByRole('button', {name: '保存连接', exact: true}).click();
      await page.waitForFunction(() => window.__mock.snapshot().saved.models.cpa.modelContextOverrides['gpt-5-extra'] === false);
      const p = (await snapshot(page)).saved.models.cpa; assert.equal(p.modelOfficialContexts['gpt-5-extra'], 1000000); assert.equal(p.modelContextWindows['gpt-5-extra'], undefined);
      await page.getByRole('button', {name: '模型设置 deepseek-v4-flash', exact: true}).click(); assert.equal(await page.getByRole('spinbutton', {name: '上下文大小（tokens）', exact: true}).inputValue(), '128000');
      await page.getByRole('spinbutton', {name: '上下文大小（tokens）', exact: true}).fill('64000'); await page.getByRole('button', {name: '取消', exact: true}).click();
      assert.equal(await page.getByRole('button', {name: '保存连接', exact: true}).isDisabled(), true);
    });
    await check('真实鼠标拖动排序，保存后保持目录顺序且配置范围不变', async page => {
      await openConnection(page, 'cpa', '模型选择'); const before = await snapshot(page), from = await page.getByRole('button', {name: '拖动排序 gpt-5-extra', exact: true}).boundingBox(), to = await page.locator('[data-connection-model="deepseek-v4-flash"]').boundingBox();
      await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2); await page.mouse.down(); await page.mouse.move(from.x + from.width / 2, to.y + to.height / 2, {steps: 14}); assert.equal(await page.locator('.model-drag-preview').count(), 1); await page.mouse.up();
      const rows = await page.locator('[data-connection-model]').evaluateAll(nodes => nodes.map(n => n.dataset.connectionModel)); assert.equal(rows[0], 'gpt-5.1-fast-high'); assert.equal(rows[2], 'gpt-5-extra');
      // dnd-kit 在释放后保留 50ms 的点击抑制，等待其手势清理，避免把紧邻的合成保存点击当作拖动点击。
      await page.waitForTimeout(80);
      await page.getByRole('button', {name: '保存连接', exact: true}).click(); await page.waitForFunction(() => window.__mock.snapshot().saved.models.cpa.modelOrder[0] === 'gpt-5.1-fast-high');
      assert.deepEqual((await snapshot(page)).saved.accessConfigs, before.saved.accessConfigs);
    });
    await check('真实触屏手势从手柄拖动，排序与保存不改变配置范围', async page => {
      await openConnection(page, 'cpa', '模型选择');
      const before = await snapshot(page), from = await page.getByRole('button', {name: '拖动排序 gpt-5-extra', exact: true}).boundingBox(), to = await page.locator('[data-connection-model="deepseek-v4-flash"]').boundingBox();
      const input = await page.context().newCDPSession(page);
      try {
        await input.send('Emulation.setTouchEmulationEnabled', {enabled: true, maxTouchPoints: 1});
        const x = from.x + from.width / 2, y = from.y + from.height / 2, end = to.y + to.height / 2;
        await input.send('Input.dispatchTouchEvent', {type: 'touchStart', touchPoints: [{x, y, id: 1}]});
        for (let i = 1; i <= 12; i++) await input.send('Input.dispatchTouchEvent', {type: 'touchMove', touchPoints: [{x, y: y + (end - y) * i / 12, id: 1}]});
        assert.equal(await page.locator('.model-drag-preview').count(), 1);
        await input.send('Input.dispatchTouchEvent', {type: 'touchEnd', touchPoints: []});
        const rows = await page.locator('[data-connection-model]').evaluateAll(nodes => nodes.map(n => n.dataset.connectionModel));
        assert.equal(rows[0], 'gpt-5.1-fast-high'); assert.equal(rows[2], 'gpt-5-extra');
        await page.waitForTimeout(80);
        await page.getByRole('button', {name: '保存连接', exact: true}).click();
        await page.waitForFunction(() => window.__mock.snapshot().saved.models.cpa.modelOrder[0] === 'gpt-5.1-fast-high');
        assert.deepEqual((await snapshot(page)).saved.accessConfigs, before.saved.accessConfigs);
      } finally {await input.detach();}
    });
    await check('键盘拖动及过滤排序不移动隐藏模型', async page => {
      await openConnection(page, 'cpa', '模型选择'); await page.getByRole('searchbox', {name: '搜索模型名称或 ID', exact: true}).fill('gpt');
      const handle = page.getByRole('button', {name: '拖动排序 gpt-5-extra', exact: true}); await handle.focus(); await handle.press('Space'); await handle.press('ArrowDown'); await handle.press('Space');
      await page.getByRole('searchbox', {name: '搜索模型名称或 ID', exact: true}).fill('');
      const rows = await page.locator('[data-connection-model]').evaluateAll(nodes => nodes.map(n => n.dataset.connectionModel)); assert.equal(rows[2], 'deepseek-v4-flash'); assert.equal(rows[0], 'gpt-5.1-fast-high');
    });
    await check('目录刷新和连接测试可取消且不接受迟到结果', async page => {
      await openConnection(page); await page.getByRole('button', {name: '测试连接', exact: true}).click(); await page.getByRole('button', {name: '正在测试', exact: true}).waitFor(); assert.equal(await page.locator('.panel .spinner').count(), 1);
      await page.getByRole('button', {name: '取消', exact: true}).click(); await page.evaluate(() => window.__mock.completeProbe()); await page.getByText('已取消', {exact: true}).waitFor();
      await page.getByRole('tab', {name: '模型选择', exact: false}).click(); await page.getByRole('button', {name: '刷新', exact: true}).click(); await page.evaluate(() => window.__mock.completeProbe()); await page.getByText('已读取 1 个模型', {exact: true}).waitFor();
      assert.equal(await page.getByRole('checkbox', {name: '连接中选择模型 new-local-model', exact: true}).isChecked(), false);
    });
    await check('应用更新文字前旋转、取消、减少动态效果、插件状态', async page => {
      await page.getByRole('button', {name: '检查更新', exact: true}).click(); await page.locator('.update-trigger .spinner').waitFor();
      assert.equal(await page.locator('.update-trigger .spinner').count(), 1); assert.equal(await page.locator('.update-trigger .spinner').evaluate(el => getComputedStyle(el).animationName), 'wait-spin');
      await page.emulateMedia({reducedMotion: 'reduce'}); assert.equal(await page.locator('.update-trigger .spinner').evaluate(el => getComputedStyle(el).animationName), 'none'); await page.emulateMedia({reducedMotion: 'no-preference'});
      await shot(page, '1204-update-checking', report); await page.getByRole('button', {name: '取消操作', exact: true}).click(); await page.evaluate(() => window.__mock.completeUpdate()); assert.equal((await snapshot(page)).state.update.phase, 'cancelled');
      await page.getByRole('button', {name: '关闭弹窗', exact: true}).click(); await page.locator('.sidebar .nav-button').filter({hasText: '插件管理'}).click(); await page.getByRole('button', {name: '检查更新', exact: true}).last().click(); await page.getByRole('button', {name: '正在检查', exact: true}).waitFor(); assert.equal(await page.locator('.plugin-actions .spinner').count(), 1); await page.getByRole('button', {name: '取消', exact: true}).click();
    });
    await check('破坏性确认的焦点与 Escape、原生关闭保护', async page => {
      await openConnection(page); await page.getByRole('button', {name: '删除连接 cpa', exact: true}).click(); assert.equal(await page.getByRole('alertdialog').count(), 1); assert.equal(await page.getByRole('button', {name: '取消', exact: true}).evaluate(el => el === document.activeElement), true); await page.keyboard.press('Escape'); assert.equal(await page.getByRole('alertdialog').count(), 0);
      await page.getByRole('textbox', {name: '连接名称', exact: true}).fill('草稿'); await page.waitForFunction(() => window.__mock.snapshot().dirty); await page.evaluate(() => window.__mock.requestClose()); await page.getByRole('alertdialog').waitFor(); await page.getByRole('button', {name: '取消', exact: true}).click(); assert.equal(await page.getByRole('textbox', {name: '连接名称', exact: true}).inputValue(), '草稿'); assert.equal((await snapshot(page)).state.relay.enabled, true);
    });
    if (page) await page.close(); page = null;
    for (const width of [320, 390, 640, 820, 1079, 1204, 1440, 1920]) {
      const p = await createPage(browser, origin, fixture, report, {width, height: 884});
      await layout(p, `${width}-plans-list`, report); await shot(p, `${width}-plans`, report);
      await p.locator('[data-plan-id="plan-a"]').click(); await layout(p, `${width}-plans-editor`, report);
      await openConnection(p); await layout(p, `${width}-connection-info`, report);
      for (const [tab, label] of [['模型选择', 'models'], ['任务分工', 'purpose'], ['高级设置', 'advanced']]) {await p.getByRole('tab', {name: tab, exact: false}).click(); await layout(p, `${width}-${label}`, report); if ([390, 1079, 1440].includes(width)) await shot(p, `${width}-${label}`, report);}
      await p.close();
    }
    assert.deepEqual(report.errors, []); assert.deepEqual(report.external, []);
    fs.writeFileSync(path.join(output, 'browser-qa.json'), JSON.stringify(report, null, 2));
    console.log(`Verified ${report.groups.length} groups, ${report.layouts.length} layouts, ${report.screenshots.length} screenshots; no script errors or external requests.`);
  } catch (error) {fs.writeFileSync(path.join(output, 'browser-failure.json'), JSON.stringify({...report, failure: error.stack}, null, 2)); if (page && !page.isClosed()) await shot(page, 'failure', report); throw error;} finally {if (page) await page.close(); await browser.close(); server.close();}
}
if (require.main === module) main().catch(error => {console.error(error); process.exitCode = 1;});
module.exports = {serve, createPage, openConnection, snapshot, layout, assets, mockBindings, output};
