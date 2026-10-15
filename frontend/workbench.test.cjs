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
    await check('多配置常驻滚动列表、查看不改变运行范围', async page => {
      assert.equal(await page.locator('.plan-list [data-plan-id]').count(),24);
      assert.equal(await page.locator('.plan-list-footer').count(),0);
      await page.locator('.plan-list [data-plan-id="plan-24"]').click();
      assert.equal((await snapshot(page)).state.relay.access.id,'plan-a');
      await layout(page,'many-plans-scroll',report);
    }, {config: manyConfig, state: fixtureState(manyConfig)});
    const longConfig = fixtureConfig(), longName = '这是用于验证超长连接名称与右端菜单对齐的测试连接'.repeat(3);
    longConfig.models[longName] = longConfig.models.cpa; delete longConfig.models.cpa;
    longConfig.accessConfigs[0].name = '这是一份很长的接入配置名称用于检查标签对齐'.repeat(2);
    longConfig.lastAccess = JSON.parse(JSON.stringify(longConfig.accessConfigs[0]));
    await check('超长名称不挤出布局，同页连接操作及确认取消保留数据', async page => {
      await layout(page,'long-plan-name',report);
      await page.locator('.sidebar .nav-button').filter({hasText:'我的连接'}).click();
      await page.locator('[data-directory-connection="cpa"] .connection-directory-open').click();
      await layout(page,'long-connection-master-detail',report);
      const trigger=page.locator('.connection-workbench-editor').getByRole('button',{name:/^删除连接/});
      await trigger.click(); await page.getByRole('alertdialog').waitFor(); await page.keyboard.press('Escape'); await page.getByRole('alertdialog').waitFor({state:'hidden'});
      assert.equal(await page.locator('.connection-workbench-list').count(),1);
    }, {config:longConfig,state:fixtureState(longConfig)});
    await check('接入工作台、固定标题、无连接子导航/多余上下文', async page => {
      assert.equal(await page.locator('.scope-model-row').count(), 8); assert.equal(await page.locator('.scope-model-row .capacity').count(), 8); assert.equal(await page.locator('.sidebar .nav-button').count(), 5);
      assert.equal(await page.locator('.heading-actions').getByRole('button', {name: '添加连接'}).count(), 0);
      assert.equal(await page.locator('.save-message').count(), 0);
      const before = await page.locator('.access-status-copy').evaluate(el => ({text: el.innerText, rect: el.getBoundingClientRect().toJSON()}));
      await page.getByRole('switch', {name: '主对话接入', exact: true}).click();
      await page.waitForFunction(() => !window.__mock.snapshot().state.relay.enabled);
      const after = await page.locator('.access-status-copy').evaluate(el => ({text: el.innerText, rect: el.getBoundingClientRect().toJSON()}));
      assert.deepEqual(after, before); await shot(page, '1204-access-off', report);
    });
    await check('macOS 主从工作区保留目录留白，模型比例和固定保存动作保持空间', async page => {
      await page.setViewportSize({width: 1481, height: 884});
      assert.equal(await page.locator('.same-page-workbench>.plan-list').evaluate(node => node.getBoundingClientRect().width), 216);
      const cardInsets = await page.locator('.same-page-workbench>.plan-list').evaluate(node => {
        const panel = node.getBoundingClientRect(), card = node.querySelector('.plan-list-item').getBoundingClientRect();
        return {left: card.left - panel.left, right: panel.right - card.right};
      });
      assert.ok(cardInsets.left <= 16 && cardInsets.right <= 16, 'list rows retain balanced insets without enlarging the column');
      assert.equal(await page.locator('.mac-plan-workspace').evaluate(node => getComputedStyle(node).columnGap), '0px');
      const before = await snapshot(page);
      const groups = await page.locator('.scope-model-grid').evaluateAll(nodes => nodes.map(node => ({width: node.getBoundingClientRect().width, rows: [...node.querySelectorAll('.scope-model-row')].map(row => row.getBoundingClientRect().width)})));
      assert.ok(groups.length > 0);
      assert.ok(groups.every(group => group.rows.every(width => width >= group.width * .95)));
      const update = await page.locator('.sidebar .update-trigger').evaluate(node => ({button: node.getBoundingClientRect().height, icon: node.querySelector('svg').getBoundingClientRect().width}));
      assert.equal(update.icon, 16); assert.ok(update.button >= 32);
      assert.equal(await page.locator('.plan-editor .editor-name-field input').evaluate(node => getComputedStyle(node).fontSize), '21px');
      const footer = await page.locator('.plan-editor .save-actions').boundingBox();
      assert.ok(footer.y + footer.height <= 884, 'save actions must remain visible at desktop height');
      assert.deepEqual((await snapshot(page)).saved.accessConfigs, before.saved.accessConfigs);
      await layout(page, '1481-polished-plans', report); await shot(page, '1481-polished-plans', report);
      await page.setViewportSize({width: 1222, height: 884});
      assert.equal(await page.locator('.same-page-workbench>.plan-list').evaluate(node => node.getBoundingClientRect().width), 216);
      assert.ok(await page.locator('.plan-editor').evaluate(node => node.getBoundingClientRect().width >= 690));
      await layout(page, '1222-filled-plan-list', report); await shot(page, '1222-filled-plan-list', report);
      await page.setViewportSize({width: 1160, height: 884});
      assert.equal(await page.locator('.same-page-workbench>.plan-list').evaluate(node => node.getBoundingClientRect().width), 208);
      await layout(page, '1160-filled-plan-list', report);
      await page.setViewportSize({width: 375, height: 884});
      const status = await page.locator('.access-status').evaluate(node => {
        const copy = node.querySelector('.access-status-copy').getBoundingClientRect(), control = node.querySelector('.access-status-control').getBoundingClientRect();
        return {copyRight: copy.right, controlLeft: control.left, centers: Math.abs(copy.y + copy.height / 2 - control.y - control.height / 2)};
      });
      assert.ok(status.controlLeft >= status.copyRight, 'mobile toggle stays to the right of status copy');
      assert.ok(status.centers < 8, 'mobile toggle stays on the same row');
      const capacities = await page.locator('.scope-model-row .capacity').evaluateAll(nodes => nodes.map(node => node.getBoundingClientRect().width));
      assert.ok(capacities.every(width => width < 100), 'capacity labels must not stretch across mobile rows');
      await layout(page, '375-polished-plans', report); await shot(page, '375-polished-plans', report);
    });
    await check('连接页共用接入配置的视觉语言，查看连接仍不保存或切换配置', async page => {
      await page.setViewportSize({width: 1067, height: 884});
      const before = await snapshot(page);
      const planStyle = await page.locator('.plan-list').evaluate(node => {
        const card = getComputedStyle(node.querySelector('.plan-list-item.selected'));
        return {width: node.getBoundingClientRect().width, padding: getComputedStyle(node).padding, heading: getComputedStyle(node.querySelector('h2')).fontSize, background: card.backgroundColor, border: card.borderColor, shadow: card.boxShadow, cardPadding: card.padding};
      });
      await openConnection(page);
      const connectionStyle = await page.locator('.connection-workbench-list').evaluate(node => {
        const button = node.querySelector('.connection-directory-open[aria-pressed="true"]'), card = getComputedStyle(button), panel = node.getBoundingClientRect(), rect = button.getBoundingClientRect();
        return {appearance: {width: panel.width, padding: getComputedStyle(node).padding, heading: getComputedStyle(node.querySelector('h2')).fontSize, background: card.backgroundColor, border: card.borderColor, shadow: card.boxShadow, cardPadding: card.padding}, left: rect.left - panel.left, right: panel.right - rect.right};
      });
      assert.equal(connectionStyle.appearance.width, 188);
      assert.equal(connectionStyle.appearance.background, planStyle.background);
      assert.equal(connectionStyle.appearance.shadow, 'none');
      assert.equal(connectionStyle.appearance.heading, planStyle.heading);
      assert.equal(connectionStyle.appearance.cardPadding, '10px 12px');
      assert.equal(await page.locator('.app-shell.macos-settings.macos-workspace').count(), 1);
      assert.equal(connectionStyle.left, 10);
      assert.equal(connectionStyle.right, 11);
      assert.equal(await page.locator('.connection-workbench-list .directory-provider-icon').first().isVisible(), true);
      assert.ok(await page.locator('.connection-workbench-editor').evaluate(node => node.getBoundingClientRect().width >= 560));
      await page.getByRole('button', {name: '管理连接 worker', exact: true}).click();
      assert.equal(await page.getByRole('textbox', {name: '连接名称', exact: true}).inputValue(), 'worker');
      assert.deepEqual((await snapshot(page)).saved, before.saved);
      assert.deepEqual((await snapshot(page)).state.relay, before.state.relay);
      await layout(page, '1067-matched-connection-list', report); await shot(page, '1067-matched-connection-list', report);
      await page.setViewportSize({width: 375, height: 884});
      const columns = await page.locator('.connection-list-items').evaluate(node => getComputedStyle(node).gridTemplateColumns.split(' ').length);
      assert.equal(columns, 1);
      await layout(page, '375-matched-connection-list', report); await shot(page, '375-matched-connection-list', report);
    });
    await check('各页面共用材质侧栏、主从面板与蓝色选中项，键盘焦点和保存边界保持不变', async page => {
      const before = await snapshot(page);
      const surface = await page.locator('.app-main').evaluate(node => {
        const editor = getComputedStyle(node.querySelector('.plan-editor')), list = getComputedStyle(node.querySelector('.plan-list')), card = getComputedStyle(node.querySelector('.plan-list-item.selected'));
        return {canvas: getComputedStyle(node).backgroundColor, editor: editor.backgroundColor, list: list.backgroundColor, listBorder: list.borderRightColor, selected: card.backgroundColor, selectedBorder: card.borderColor, selectedShadow: card.boxShadow};
      });
      assert.equal(surface.canvas, 'rgb(245, 245, 247)');
      assert.equal(surface.editor, 'rgb(255, 255, 255)');
      assert.equal(await page.locator('.sidebar').evaluate(node => getComputedStyle(node).backgroundColor), 'rgb(238, 238, 241)');
      assert.equal(surface.list, 'rgb(248, 248, 250)');
      assert.equal(surface.listBorder, 'rgb(227, 227, 232)');
      assert.equal(surface.selected, 'rgb(0, 106, 220)');
      assert.equal(surface.selectedBorder, 'rgba(0, 0, 0, 0)'); assert.notEqual(surface.selectedShadow, 'none');
      assert.equal(await page.locator('.plan-list-item.selected strong').evaluate(node => getComputedStyle(node).fontWeight), '600');
      assert.ok(await page.locator('.scope-group').evaluateAll(nodes => nodes.every(node => getComputedStyle(node).borderColor === 'rgb(227, 227, 232)')));
      await page.locator('.plan-list-item').first().focus(); await page.keyboard.press('Tab');
      const focusedPlan = page.locator('.plan-list-item:focus-visible'); assert.equal(await focusedPlan.count(), 1);
      assert.equal(await focusedPlan.evaluate(node => getComputedStyle(node).outlineWidth), '2px');
      assert.equal(await focusedPlan.evaluate(node => getComputedStyle(node).outlineOffset), '-2px');
      await openConnection(page);
      assert.equal(await page.locator('.app-main').evaluate(node => getComputedStyle(node).backgroundColor), surface.canvas);
      assert.equal(await page.locator('.connection-workbench-list').evaluate(node => getComputedStyle(node).backgroundColor), surface.list);
      assert.equal(await page.locator('.connection-directory-open[aria-pressed="true"]').evaluate(node => getComputedStyle(node).backgroundColor), surface.selected);
      assert.ok(await page.locator('.connection-workbench-editor .panel').evaluateAll(nodes => nodes.every(node => getComputedStyle(node).borderWidth === '0px' && getComputedStyle(node).backgroundColor === 'rgb(255, 255, 255)')));
      await page.locator('.connection-directory-open').first().focus(); await page.keyboard.press('Tab');
      const focusedConnection = page.locator('.connection-directory-open:focus-visible'); assert.equal(await focusedConnection.count(), 1);
      assert.equal(await focusedConnection.evaluate(node => getComputedStyle(node).outlineWidth), '2px');
      assert.deepEqual((await snapshot(page)).saved, before.saved);
      assert.deepEqual((await snapshot(page)).state.relay, before.state.relay);
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
      await page.getByRole('button', {name: '编辑配置名称', exact: true}).click();
      await page.getByRole('textbox', {name: '配置名称', exact: true}).fill('修改后的 A');
      await page.evaluate(() => {window.__mock.failure = '/api/access/save';});
      await page.getByRole('button', {name: '仅保存', exact: true}).click(); await page.getByText('synthetic operation failed', {exact: true}).waitFor();
      assert.equal(await page.getByRole('textbox', {name: '配置名称', exact: true}).inputValue(), '修改后的 A');
      await page.getByRole('button', {name: '仅保存', exact: true}).click(); await page.waitForFunction(() => window.__mock.snapshot().saved.accessConfigs[0].name === '修改后的 A');
    });
    await check('默认模型提示已精简，但默认校验及显式星标仍有效', async page => {
      await page.locator('[data-plan-id="plan-b"]').click();
      await page.getByRole('button', {name: '设为此配置默认 review / review-model', exact: true}).click();
      await page.getByRole('button', {name: '仅保存', exact: true}).click(); await page.waitForFunction(() => window.__mock.snapshot().saved.accessConfigs[1].defaultKey === '["review","review-model"]');
      assert.equal((await snapshot(page)).saved.models.cpa.model, 'gpt-5-extra');
      await page.getByRole('checkbox', {name: '配置中选择 review / review-model', exact: true}).click();
      assert.equal(await page.getByRole('button', {name: '保存并启用', exact: true}).isEnabled(), true);
      await page.getByRole('button', {name: '保存并启用', exact: true}).click();
      await page.getByRole('alertdialog').waitFor(); assert.match(await page.getByRole('alertdialog').innerText(), /请星标/);
      await page.keyboard.press('Escape');
      assert.equal(await page.locator('.inline-error').count(), 0);
      assert.equal(await page.locator('.editor-footer .save-message').count(), 0);
      assert.equal((await page.locator('.plan-editor').innerText()).includes('请星标'), false);
      assert.equal(await page.getByRole('button', {name: '仅保存', exact: true}).isEnabled(), true);
      await page.getByRole('button', {name: '仅保存', exact: true}).click();
      await page.getByRole('alertdialog').waitFor(); assert.match(await page.getByRole('alertdialog').innerText(), /请星标/);
      await page.keyboard.press('Escape');
      assert.equal(await page.getByRole('button', {name: /配置模型排序操作/}).count(), 0);
      await page.getByRole('checkbox', {name: '配置中选择 cpa / gpt-5.1-fast-high', exact: true}).check();
      await page.getByRole('button', {name: '设为此配置默认 cpa / gpt-5.1-fast-high', exact: true}).click();
      assert.equal(await page.getByRole('button', {name: '仅保存', exact: true}).isEnabled(), true);
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
      const before = await snapshot(page);
      const input = await page.context().newCDPSession(page);
      try {
        await input.send('Emulation.setTouchEmulationEnabled', {enabled: true, maxTouchPoints: 1});
        // 先启用触控再测量坐标；粗指针模式会扩大控件和行高，不能沿用桌面尺寸下的落点。
        await page.waitForFunction(() => matchMedia('(pointer: coarse)').matches);
        const from = await page.getByRole('button', {name: '拖动排序 gpt-5-extra', exact: true}).boundingBox(), to = await page.locator('[data-connection-model="deepseek-v4-flash"]').boundingBox();
        const x = from.x + from.width / 2, y = from.y + from.height / 2, end = to.y + to.height / 2;
        await input.send('Input.dispatchTouchEvent', {type: 'touchStart', touchPoints: [{x, y, id: 1}]});
        for (let i = 1; i <= 12; i++) await input.send('Input.dispatchTouchEvent', {type: 'touchMove', touchPoints: [{x, y: y + (end - y) * i / 12, id: 1}]});
        await page.locator('.model-drag-preview').waitFor({state: 'visible'});
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
      // 等待真实拖动状态与落点提交，避免按键快于传感器挂载导致回归检查偶发读取旧顺序。
      const handle = page.getByRole('button', {name: '拖动排序 gpt-5-extra', exact: true}); await handle.focus(); await handle.press('Space');
      await page.locator('.model-drag-preview').waitFor({state: 'visible'});
      await handle.press('ArrowDown');
      await page.locator('[data-connection-model="gpt-5.1-fast-high"].is-drop-target').waitFor({state: 'visible'});
      await handle.press('Space');
      await page.locator('.model-drag-preview').waitFor({state: 'detached'});
      await page.waitForFunction(() => document.querySelector('[data-connection-model]')?.dataset.connectionModel === 'gpt-5.1-fast-high');
      await page.getByRole('searchbox', {name: '搜索模型名称或 ID', exact: true}).fill('');
      const rows = await page.locator('[data-connection-model]').evaluateAll(nodes => nodes.map(n => n.dataset.connectionModel)); assert.equal(rows[2], 'deepseek-v4-flash'); assert.equal(rows[0], 'gpt-5.1-fast-high');
    });
    await check('刷新仅保留按钮进度与失败提示，连接测试取消仍隔离迟到结果', async page => {
      await openConnection(page); await page.getByRole('button', {name: '测试连接', exact: true}).click(); await page.getByRole('button', {name: '正在测试', exact: true}).waitFor(); assert.equal(await page.locator('.panel .spinner').count(), 1);
      await page.getByRole('button', {name: '取消', exact: true}).click(); await page.evaluate(() => window.__mock.completeProbe()); await page.getByText('已取消', {exact: true}).waitFor();
      await page.getByRole('tab', {name: '模型选择', exact: false}).click();
      assert.equal(await page.getByRole('button', {name: /^更多操作 /}).count(), 0);
      await page.getByRole('button', {name: '刷新', exact: true}).click();
      await page.getByRole('button', {name: '正在刷新', exact: true}).waitFor();
      assert.equal(await page.getByRole('button', {name: '正在刷新', exact: true}).getAttribute('aria-busy'), 'true');
      assert.equal(await page.getByRole('button', {name: '取消', exact: true}).count(), 0);
      assert.equal(await page.locator('.job-inline').count(), 0);
      await page.evaluate(() => window.__mock.completeProbe());
      await page.getByRole('button', {name: '刷新', exact: true}).waitFor();
      assert.equal(await page.locator('.job-inline').count(), 0);
      assert.equal(await page.getByRole('checkbox', {name: '连接中选择模型 new-local-model', exact: true}).isChecked(), false);
      await page.evaluate(() => {window.__mock.failure = '/api/models';});
      await page.getByRole('button', {name: '刷新', exact: true}).click();
      await page.locator('.job-inline[role="alert"]').filter({hasText: 'synthetic operation failed'}).waitFor();
      await page.getByRole('button', {name: '刷新', exact: true}).click();
      await page.getByRole('button', {name: '正在刷新', exact: true}).waitFor();
      assert.equal(await page.locator('.job-inline').count(), 0);
      await page.evaluate(() => window.__mock.completeProbe());
      await page.getByRole('button', {name: '刷新', exact: true}).waitFor();
      assert.equal(await page.locator('.job-inline').count(), 0);
    });
    await check('应用更新文字前旋转、取消、减少动态效果、插件状态', async page => {
      const spinner = page.locator('.update-trigger:visible .spinner');
      await page.getByRole('button', {name: '检查更新', exact: true}).click(); await spinner.waitFor();
      assert.equal(await spinner.count(), 1); assert.equal(await spinner.evaluate(el => getComputedStyle(el).animationName), 'wait-spin');
      await page.emulateMedia({reducedMotion: 'reduce'}); assert.equal(await spinner.evaluate(el => getComputedStyle(el).animationName), 'none'); await page.emulateMedia({reducedMotion: 'no-preference'});
      await shot(page, '1204-update-checking', report); await page.getByRole('button', {name: '取消操作', exact: true}).click(); await page.evaluate(() => window.__mock.completeUpdate()); assert.equal((await snapshot(page)).state.update.phase, 'cancelled');
      await page.getByRole('button', {name: '关闭弹窗', exact: true}).click(); await page.locator('.sidebar .nav-button').filter({hasText: '插件管理'}).click(); await page.getByRole('button', {name: '检查更新', exact: true}).last().click(); await page.getByRole('button', {name: '正在检查', exact: true}).waitFor(); assert.equal(await page.locator('.plugin-actions .spinner').count(), 1); await page.getByRole('button', {name: '取消', exact: true}).click();
    });
    await check('破坏性确认的焦点与 Escape、原生关闭保护', async page => {
      await openConnection(page); await page.getByRole('button', {name: '删除连接 cpa', exact: true}).click(); assert.equal(await page.getByRole('alertdialog').count(), 1); assert.equal(await page.getByRole('button', {name: '取消', exact: true}).evaluate(el => el === document.activeElement), true); await page.keyboard.press('Escape'); assert.equal(await page.getByRole('alertdialog').count(), 0);
      await page.getByRole('textbox', {name: '连接名称', exact: true}).fill('草稿'); await page.waitForFunction(() => window.__mock.snapshot().dirty); await page.evaluate(() => window.__mock.requestClose()); await page.getByRole('alertdialog').waitFor(); await page.getByRole('button', {name: '取消', exact: true}).click(); assert.equal(await page.getByRole('textbox', {name: '连接名称', exact: true}).inputValue(), '草稿'); assert.equal((await snapshot(page)).state.relay.enabled, true);
    });
    await check('已发布诊断入口保留、脱敏白名单、实时状态和弹窗焦点', async page => {
      await page.locator('.sidebar .nav-button').filter({hasText: '使用帮助'}).click();
      await page.getByText('关闭后重新打开会使用什么配置？', {exact: true}).click();
      const trigger = page.getByRole('button', {name: '查看最近请求', exact: true});
      await trigger.click(); await page.getByRole('dialog', {name: '最近请求', exact: true}).waitFor();
      await page.getByText('尚无请求记录', {exact: true}).waitFor();
      await page.evaluate(() => window.__mock.publishDiagnostics([{id: 'local-test-id', connection: 'cpa', model: '<img src=x>', startedAt: '2026-10-12T00:00:00Z', outcome: 'upstream_failed', httpStatus: 200, upstreamRequestId: 'safe-test-id', firstByteMs: 12, durationMs: 24, bytesReceived: 100, apiKey: 'must-not-render', prompt: 'must-not-render'}]));
      const row = page.locator('.request-diagnostics-list details');
      await row.getByText('上游报告生成失败', {exact: true}).waitFor(); await row.locator('summary').click();
      assert.equal(await page.locator('.request-diagnostics img').count(), 0);
      assert.equal((await row.locator('pre').textContent()).includes('must-not-render'), false);
      assert.equal((await row.locator('pre').textContent()).includes('上游报告生成失败'), true);
      await page.getByRole('button', {name: '复制 cpa 的请求诊断', exact: true}).click();
      await page.locator('.request-diagnostics [role=status]').filter({hasText: /已复制|无法访问剪贴板/}).waitFor();
      await page.setViewportSize({width: 320, height: 884}); await layout(page, '320-request-diagnostics', report); await shot(page, '320-request-diagnostics', report);
      await page.keyboard.press('Escape'); assert.equal(await page.getByRole('dialog').count(), 0);
      assert.equal(await trigger.evaluate(el => el === document.activeElement), true);
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
