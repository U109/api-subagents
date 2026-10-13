// 接入配置的 macOS 风格回归：只使用合成内存绑定，不访问用户配置、真实模型接口或付费 API。
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const assert = require('node:assert/strict');
const {pathToFileURL} = require('node:url');
const {chromium} = require('playwright');
const {mockBindings} = require('./tests/mock-bindings.cjs');
const output = path.resolve(__dirname, '../output/macos-settings-qa');
const assets = ['index.html', 'workbench-app.js', 'workbench-app.css', 'bridge.js'];

/** 仅提供正式白名单资源；可选预览注入合成绑定，不提供本机配置或模型代理接口。 */
async function serve(previewFixture) {
  const server = http.createServer((request, response) => {
    const name = new URL(request.url, 'http://localhost').pathname.slice(1) || 'index.html';
    if (!assets.includes(name)) {response.writeHead(404); response.end(); return;}
    response.setHeader('Content-Type', name.endsWith('.css') ? 'text/css' : name.endsWith('.js') ? 'text/javascript' : 'text/html; charset=utf-8');
    let content = fs.readFileSync(path.join(__dirname, name));
    if (name === 'index.html' && previewFixture) content = content.toString().replace('</head>', `<script>(${mockBindings.toString()})(${JSON.stringify(previewFixture)});</script></head>`);
    response.end(content);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return {server, origin: `http://127.0.0.1:${server.address().port}`};
}

/** 从实际渲染的 RGB 颜色计算对比度，确保蓝色选中项的文字不是仅凭肉眼判断。 */
function contrast(foreground, background) {
  /** 将 sRGB 通道转为相对亮度；测试只处理计算样式返回的不透明 RGB 颜色。 */
  function luminance(color) {
    const values = color.match(/[\d.]+/g).slice(0, 3).map(value => Number(value) / 255).map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4);
    return values[0] * .2126 + values[1] * .7152 + values[2] * .0722;
  }
  const a = luminance(foreground), b = luminance(background);
  return (Math.max(a, b) + .05) / (Math.min(a, b) + .05);
}

/** 检查状态、保存边界、响应式和键盘反馈；遵守拖动库的点击隔离期，失败诊断与截图仅含合成数据。 */
async function main() {
  fs.mkdirSync(output, {recursive: true});
  const {fixtureConfig, fixtureState} = await import(pathToFileURL(path.join(__dirname, 'tests/fixtures.mjs')));
  const config = fixtureConfig(), fixture = {config, state: fixtureState(config)};
  if (process.argv.includes('--preview')) {
    const {origin} = await serve(fixture);
    console.log(`Synthetic macOS settings preview: ${origin}`);
    return;
  }
  const {server, origin} = await serve();
  const browser = await chromium.launch({headless: true, executablePath: process.env.CHROME_PATH || (process.platform === 'win32' ? 'C:/Program Files/Google/Chrome/Application/chrome.exe' : undefined)});
  const report = {checks: [], layouts: [], errors: [], external: []};
  let page;
  /** 每个场景使用隔离页面与模拟后台，所有非本地网络请求都记录并阻断。 */
  async function open(data = fixture, viewport = {width: 1200, height: 878}) {
    if (page) await page.close();
    page = await browser.newPage({viewport});
    page.on('pageerror', error => report.errors.push(error.message));
    await page.route('**/*', route => {if (route.request().url().startsWith(origin)) return route.continue(); report.external.push(route.request().url()); return route.abort();});
    await page.addInitScript(mockBindings, data);
    await page.goto(origin);
    await page.waitForFunction(() => window.__mock?.ready?.configLoaded);
    await page.evaluate(() => document.fonts.ready);
  }
  /** 保存真实页面截图；只允许当前测试生成的合成配置出现在图中。 */
  async function shot(name) {await page.screenshot({path: path.join(output, name + '.png'), fullPage: true});}
  /** 从公开模型行读取指定连接的展示顺序，不访问控制器内部状态。 */
  async function scopedOrder(connection = 'cpa') {return page.locator('[data-scoped-model]').evaluateAll((nodes, id) => nodes.map(node => JSON.parse(node.dataset.scopedModel)).filter(key => key[0] === id).map(key => key[1]), connection);}
  try {
    await open();
    assert.equal(await page.locator('.app-shell.macos-settings').count(), 1);
    assert.equal(await page.locator('.scope-model-row').count(), 8);
    const colors = await page.locator('.plan-list-item.selected').evaluate(node => ({background: getComputedStyle(node).backgroundColor, foreground: getComputedStyle(node.querySelector('strong')).color, secondary: getComputedStyle(node.querySelector('.plan-list-connections')).color}));
    assert.ok(contrast(colors.foreground, colors.background) >= 4.5);
    assert.ok(contrast(colors.secondary, colors.background) >= 4.5);
    const dragColors = await page.locator('.scope-model-row').first().evaluate(node => ({background: getComputedStyle(node).backgroundColor, handle: getComputedStyle(node.querySelector('.scope-drag-handle')).color}));
    assert.ok(contrast(dragColors.handle, dragColors.background) >= 3, 'active drag handles retain non-text contrast');
    await shot('1200-macos-settings');
    const footer = await page.locator('.plan-editor .save-actions').boundingBox();
    assert.ok(footer.y + footer.height <= 878, `desktop save controls remain in the viewport: ${JSON.stringify(footer)}`);
    report.checks.push('蓝色选中态的主次文字对比度、默认模型及固定保存栏');

    const initial = await page.evaluate(() => window.__mock.snapshot());
    assert.equal(await page.locator('[data-plan-connection]').count(), 2);
    assert.equal(await page.locator('[data-plan-connection="review"]').count(), 0);
    const addConnection = page.getByRole('button', {name: '添加连接', exact: true});
    await addConnection.click();
    assert.equal(await page.getByRole('menuitem').count(), 1);
    await shot('1200-connection-picker');
    await page.keyboard.press('Escape');
    assert.equal(await addConnection.evaluate(node => node === document.activeElement), true);
    await addConnection.click();
    await page.getByRole('menuitem', {name: /^review/}).click();
    await page.locator('[data-plan-connection="review"]').waitFor();
    assert.equal(await page.locator('[data-plan-connection]').count(), 3);
    assert.deepEqual((await page.evaluate(() => window.__mock.snapshot())).saved, initial.saved);
    assert.deepEqual((await page.evaluate(() => window.__mock.snapshot())).state.relay.access, initial.state.relay.access);
    await addConnection.click();
    await page.getByText('所有连接都已加入此配置', {exact: true}).waitFor();
    await page.keyboard.press('Escape');
    await page.getByRole('button', {name: '移除连接 review', exact: true}).click();
    await page.locator('[data-plan-connection="review"]').waitFor({state: 'detached'});
    assert.equal(await page.locator('[data-plan-connection]').count(), 2);
    assert.deepEqual((await page.evaluate(() => window.__mock.snapshot())).saved.models, initial.saved.models);
    report.checks.push('连接只展示已加入成员，按需添加与移除仅修改当前草稿，菜单 Escape 恢复焦点');

    const cpaGroup = page.getByRole('region', {name: 'cpa 的配置模型', exact: true});
    await page.getByRole('button', {name: '收起 cpa 的配置模型', exact: true}).click();
    assert.equal(await page.getByRole('button', {name: '展开 cpa 的配置模型', exact: true}).getAttribute('aria-expanded'), 'false');
    assert.equal(await cpaGroup.locator('.scope-model-row').count(), 0);
    assert.equal(await cpaGroup.locator('.scope-group-count').innerText(), '6 / 6');
    const groupSearch = page.getByRole('searchbox', {name: '搜索模型名称或 ID'});
    await groupSearch.fill('gpt-5.1-fast-high');
    await cpaGroup.locator('.scope-model-row').waitFor({state: 'visible'});
    assert.equal(await page.getByRole('button', {name: '收起 cpa 的配置模型', exact: true}).getAttribute('aria-expanded'), 'true');
    await page.getByRole('button', {name: '清空搜索', exact: true}).click();
    assert.equal(await page.locator('.scope-model-row').count(), 8);
    assert.ok(await cpaGroup.locator('.scope-model-row').evaluateAll(nodes => nodes.every(node => node.getBoundingClientRect().height <= 38)));
    assert.deepEqual((await page.evaluate(() => window.__mock.snapshot())).saved, initial.saved);
    report.checks.push('紧凑分组支持折叠和搜索自动展开，数量与真实模型选择保持一致');

    const dragHandle = page.getByRole('button', {name: '拖动排序 cpa / gpt-5-extra', exact: true});
    const from = await dragHandle.boundingBox(), to = await cpaGroup.locator('.scope-model-row').nth(2).boundingBox();
    await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
    await page.mouse.down();
    await page.mouse.move(from.x + from.width / 2, to.y + to.height / 2, {steps: 14});
    await page.locator('.scope-drag-preview').waitFor({state: 'visible'});
    await page.mouse.up();
    await page.waitForFunction(() => JSON.parse(document.querySelector('[data-scoped-model]').dataset.scopedModel)[1] === 'gpt-5.1-fast-high');
    assert.deepEqual((await scopedOrder()).slice(0, 3), ['gpt-5.1-fast-high', 'deepseek-v4-flash', 'gpt-5-extra']);
    assert.deepEqual((await page.evaluate(() => window.__mock.snapshot())).saved, initial.saved);
    await page.locator('.scope-drag-preview').waitFor({state: 'hidden'});
    // dnd-kit PointerSensor 放手后仍捕获 click 50ms；超过隔离期再模拟下一次独立点击，不绕过真实保存按钮。
    await page.waitForTimeout(70);
    await page.getByRole('button', {name: '仅保存', exact: true}).click();
    await page.waitForFunction(() => window.__mock.snapshot().saved.accessConfigs[0].modelIDs.cpa[0] === 'gpt-5.1-fast-high');
    const reordered = await page.evaluate(() => window.__mock.snapshot());
    assert.deepEqual(reordered.saved.models, initial.saved.models);
    assert.deepEqual(reordered.state.relay.access, initial.state.relay.access);
    assert.equal(reordered.saved.accessConfigs[0].defaultKey, initial.saved.accessConfigs[0].defaultKey);
    assert.deepEqual(reordered.saved.accessConfigs[1], initial.saved.accessConfigs[1]);
    await open({config: reordered.saved, state: reordered.state});
    assert.equal((await scopedOrder())[0], 'gpt-5.1-fast-high');
    report.checks.push('真实手柄拖动独立保存配置模型顺序，重新加载仍恢复，不改原连接、默认或其他配置');

    const keyboardHandle = page.getByRole('button', {name: '拖动排序 cpa / gpt-5.1-fast-high', exact: true});
    await keyboardHandle.focus(); await keyboardHandle.press('Space');
    await page.locator('.scope-drag-preview').waitFor({state: 'visible'});
    await keyboardHandle.press('ArrowDown');
    await page.locator('[data-scoped-model]').filter({has: page.getByText('deepseek-v4-flash', {exact: true})}).and(page.locator('.is-drop-target')).waitFor({state: 'visible'});
    await keyboardHandle.press('Space');
    await page.waitForFunction(() => JSON.parse(document.querySelector('[data-scoped-model]').dataset.scopedModel)[1] === 'deepseek-v4-flash');
    assert.deepEqual((await scopedOrder()).slice(0, 3), ['deepseek-v4-flash', 'gpt-5.1-fast-high', 'gpt-5-extra']);
    await page.getByRole('button', {name: '调整 cpa 的模型顺序', exact: true}).click();
    await page.getByRole('menuitem', {name: '上移 cpa / gpt-5-extra', exact: true}).click();
    await page.waitForFunction(() => JSON.parse(document.querySelectorAll('[data-scoped-model]')[1].dataset.scopedModel)[1] === 'gpt-5-extra');
    assert.deepEqual((await scopedOrder()).slice(0, 3), ['deepseek-v4-flash', 'gpt-5-extra', 'gpt-5.1-fast-high']);
    await page.getByRole('checkbox', {name: '配置中选择 cpa / gpt-4', exact: true}).click();
    assert.equal(await page.getByRole('button', {name: '拖动排序 cpa / gpt-4', exact: true}).isDisabled(), true);
    report.checks.push('键盘与菜单可替代拖动，未选模型不参与排序');
    await open();

    const before = await page.evaluate(() => window.__mock.snapshot());
    await page.locator('[data-plan-id="plan-b"]').click();
    assert.equal(await page.getByRole('textbox', {name: '配置名称', exact: true}).inputValue(), '配置 B');
    const after = await page.evaluate(() => window.__mock.snapshot());
    assert.deepEqual(after.saved.accessConfigs, before.saved.accessConfigs);
    assert.equal(after.state.relay.access.id, 'plan-a');
    assert.equal(await page.locator('[data-plan-id="plan-a"] .badge-active').innerText(), '使用中');
    await shot('1200-macos-viewing-inactive');
    report.checks.push('查看配置 B 不会保存或替换运行中的配置 A');

    await page.locator('[data-plan-id="plan-a"]').click();
    await page.getByRole('textbox', {name: '配置名称', exact: true}).fill('开发配置');
    await page.locator('.editor-save-status').getByText('有未保存的修改', {exact: true}).waitFor();
    await shot('1200-macos-draft');
    await page.getByRole('button', {name: '仅保存', exact: true}).click();
    await page.waitForFunction(() => window.__mock.snapshot().saved.accessConfigs[0].name === '开发配置');
    assert.equal((await page.evaluate(() => window.__mock.snapshot())).state.relay.access.name, '配置 A');
    await page.locator('.editor-save-status').getByText('已保存 · 等待更新接入', {exact: true}).waitFor();
    await page.getByRole('button', {name: '启用此配置', exact: true}).click();
    await page.waitForFunction(() => window.__mock.snapshot().state.relay.access.name === '开发配置');
    await page.locator('.editor-save-status').getByText('已与当前接入同步', {exact: true}).waitFor();
    report.checks.push('草稿、仅保存和明确启用沿用原有配置快照边界');

    const star = page.locator('.scope-model-row .star-button').nth(1);
    await star.click();
    await page.waitForFunction(() => document.querySelectorAll('.scope-model-row .star-button')[1]?.getAttribute('aria-pressed') === 'true');
    assert.equal(await star.getAttribute('aria-pressed'), 'true');
    await page.getByRole('button', {name: '还原', exact: true}).click();
    await page.getByRole('alertdialog').getByRole('button', {name: '还原配置', exact: true}).click();
    await page.getByRole('alertdialog').waitFor({state: 'hidden'});
    await page.waitForFunction(() => document.querySelector('.scope-model-row .star-button')?.getAttribute('aria-pressed') === 'true');
    assert.equal(await page.locator('.scope-model-row .star-button').first().getAttribute('aria-pressed'), 'true');
    const toggle = page.getByRole('switch', {name: '主对话接入', exact: true});
    const summary = await page.locator('.access-status-copy').innerText();
    await toggle.click();
    await page.waitForFunction(() => !window.__mock.snapshot().state.relay.enabled);
    assert.equal(await page.locator('.access-state-label').innerText(), '已关闭');
    assert.equal(await page.locator('.access-status-copy').innerText(), summary);
    await toggle.click();
    await page.waitForFunction(() => window.__mock.snapshot().state.relay.enabled);
    report.checks.push('星标与还原、关闭与恢复不隐式切换范围');

    const search = page.getByRole('searchbox', {name: '搜索模型名称或 ID'});
    await search.fill('no-such-model');
    assert.equal(await page.locator('.scope-model-row').count(), 0);
    await page.getByRole('button', {name: '清空搜索', exact: true}).click();
    assert.equal(await page.locator('.scope-model-row').count(), 8);
    await search.focus();
    await page.keyboard.press('Tab');
    assert.equal(await page.getByRole('button', {name: '全部', exact: true}).evaluate(node => node === document.activeElement && getComputedStyle(node).outlineStyle !== 'none'), true);
    await page.emulateMedia({reducedMotion: 'reduce'});
    assert.equal(await page.locator('.plan-list-item').first().evaluate(node => getComputedStyle(node).transitionDuration), '0s');
    report.checks.push('搜索与清空、键盘焦点和减少动态效果');
    const dismissToast = page.getByRole('button', {name: '关闭提示', exact: true});
    if (await dismissToast.isVisible()) await dismissToast.click();
    await page.getByRole('heading', {name: '接入配置', exact: true}).click();

    for (const width of [1440, 1280, 1200, 1024, 900, 768, 640, 639, 375, 320]) {
      await page.setViewportSize({width, height: 878});
      const layout = await page.evaluate(() => ({width: innerWidth, document: document.documentElement.scrollWidth, nestedButtons: document.querySelectorAll('button button').length, saveButtons: [...document.querySelectorAll('.save-actions .button')].map(node => ({left: node.getBoundingClientRect().left, right: node.getBoundingClientRect().right}))}));
      assert.ok(layout.document <= width + 1, `horizontal overflow at ${width}px: ${JSON.stringify(layout)}`);
      assert.equal(layout.nestedButtons, 0);
      assert.ok(layout.saveButtons.every(button => button.left >= 0 && button.right <= width), `save buttons must not be clipped at ${width}px: ${JSON.stringify(layout)}`);
      report.layouts.push(layout);
      if (width === 1440 || width === 375) await shot(`${width}-macos-settings`);
    }
    await page.setViewportSize({width: 1200, height: 560});
    assert.ok(await page.locator('.save-actions').isVisible());
    await page.getByRole('textbox', {name: '配置名称', exact: true}).fill('用于验证配置名称在较窄窗口中自然换行且状态不被挤出的长名称'.repeat(2));
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
    report.checks.push('320–1440px、短窗口和长名称不产生横向溢出');

    const many = fixtureConfig(24);
    await open({config: many, state: fixtureState(many)});
    assert.ok(await page.locator('.plan-list-items').evaluate(node => node.scrollHeight > node.clientHeight));
    await page.locator('[data-plan-id="plan-24"]').click();
    assert.equal((await page.evaluate(() => window.__mock.snapshot())).state.relay.access.id, 'plan-a');
    report.checks.push('多配置目录独立滚动，选择末项不会切换实际接入');

    const empty = {version: 1, models: {}, accessConfigs: [], lastAccess: null};
    await open({config: empty, state: {...fixtureState(empty), relay: {enabled: false}}});
    assert.equal(await page.locator('.scope-model-row').count(), 0);
    assert.equal(await page.getByRole('switch', {name: '主对话接入', exact: true}).getAttribute('aria-checked'), 'false');
    await shot('1200-macos-empty');
    report.checks.push('空工作区不伪造配置、模型或开启状态');
    assert.deepEqual(report.errors, []);
    assert.deepEqual(report.external, []);
    fs.writeFileSync(path.join(output, 'report.json'), JSON.stringify(report, null, 2));
    console.log(JSON.stringify(report, null, 2));
  } catch (error) {
    // 失败只记录合成配置的顺序与公开控件，不导出连接 Key，便于区分交互回归和测试时序问题。
    const diagnostic = page && !page.isClosed() ? await page.evaluate(() => {
      const snapshot = window.__mock?.snapshot?.();
      return {
        savedPlans: snapshot?.saved?.accessConfigs,
        planSaves: (snapshot?.calls || []).filter(call => call.route === '/api/access/save').map(call => JSON.parse(call.body).plan),
        displayedOrder: [...document.querySelectorAll('[data-scoped-model]')].map(node => JSON.parse(node.dataset.scopedModel)),
        status: document.querySelector('.editor-save-status')?.textContent,
        errors: [...document.querySelectorAll('.field-error')].map(node => node.textContent),
        buttons: [...document.querySelectorAll('.save-actions button')].map(node => ({text: node.textContent, disabled: node.disabled})),
      };
    }) : null;
    fs.writeFileSync(path.join(output, 'failure.json'), JSON.stringify({...report, failure: error.stack, diagnostic}, null, 2));
    if (page && !page.isClosed()) await page.screenshot({path: path.join(output, 'failure.png'), fullPage: true});
    throw error;
  } finally {await browser.close(); await new Promise(resolve => server.close(resolve));}
}

main().catch(error => {console.error(error); process.exitCode = 1;});
