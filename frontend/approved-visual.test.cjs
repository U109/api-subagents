// 已确认原型与正式界面的同数据、同尺寸对照；不访问本机连接，不允许非本地请求。
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {pathToFileURL} = require('node:url');
const {chromium} = require('playwright');
const {PNG} = require('pngjs');
const {createHash} = require('node:crypto');
const {serve, openConnection, snapshot, output} = require('./workbench.test.cjs');
const prototype = path.resolve(process.env.APPROVED_PROTOTYPE || path.join(__dirname, '../output/ui-prototype-2026-10-13-unified-workspace/interactive-prototype.html'));

/** 从已确认的离线原型读取合成夹具和资源指纹，不执行 HTML，也不重新生成视觉基准。 */
function readReference() {
  assert(fs.existsSync(prototype), 'Set APPROVED_PROTOTYPE to the approved self-contained workspace HTML.');
  const html = fs.readFileSync(prototype, 'utf8');
  const match = html.match(/\)\((\{"config":[^\n]+)\);\r?\n/);
  assert(match, 'The approved workspace prototype must contain its synthetic fixture.');
  const fixture = JSON.parse(match[1]);
  const css = html.match(/<style>([\s\S]*?)<\/style>/)?.[1];
  assert(css, 'The approved prototype must contain its stylesheet.');
  assert.equal(fs.readFileSync(path.join(__dirname, 'workbench-app.css'), 'utf8'), css, 'Production CSS differs from the approved prototype; do not regenerate the baseline to hide drift.');
  const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(item => item[1]);
  const bundle = scripts.find(script => script.startsWith('(()=>{'));
  assert(bundle, 'The approved prototype must contain the bundled application.');
  // 行为修复允许 JS 指纹变化，但不更新原型基准；CSS、桥接、几何、文本和像素仍逐项严格对照。
  const actualBundle = fs.readFileSync(path.join(__dirname, 'workbench-app.js'), 'utf8').replace(/<\/script/gi, '<\\/script');
  const bundleEvidence = {identical: bundle === actualBundle, approvedSHA256: createHash('sha256').update(bundle).digest('hex'), implementationSHA256: createHash('sha256').update(actualBundle).digest('hex')};
  const bridge = fs.readFileSync(path.join(__dirname, 'bridge.js'), 'utf8');
  assert(scripts.some(script => script.endsWith(bridge.replace(/<\/script/gi, '<\\/script'))), 'The prototype and production binding bridge differ.');
  const time = Math.max(...fixture.usageRecords.map(record => record.startedAt)) + 35 * 60000;
  assert(Number.isFinite(time), 'The approved fixture must include synthetic usage timestamps.');
  const hashes = Object.fromEntries(['index.html', 'bridge.js', 'workbench-app.js', 'workbench-app.css'].map(name => [name, createHash('sha256').update(fs.readFileSync(path.join(__dirname, name))).digest('hex')]));
  return {fixture, time, hashes, bundleEvidence, prototypeSHA256: createHash('sha256').update(html).digest('hex')};
}

/** 两侧使用相同的合成数据和固定日期；只放行各自的本地静态资源，真实连接和付费接口不可达。 */
async function createVisualPage(browser, url, reference, report, viewport, actual) {
  const page = await browser.newPage({viewport, reducedMotion: 'reduce'});
  await page.clock.setFixedTime(new Date(reference.time));
  page.on('pageerror', error => report.errors.push(error.message));
  await page.route('**/*', route => {
    const address = route.request().url();
    if (actual ? new URL(address).origin === new URL(url).origin : address.startsWith('file:')) return route.continue();
    report.external.push(address); return route.abort();
  });
  if (actual) await page.addInitScript(require('./tests/mock-bindings.cjs').mockBindings, reference.fixture);
  await page.goto(url);
  await page.waitForFunction(() => window.__mock?.ready?.configLoaded);
  await page.evaluate(() => document.fonts.ready);
  return page;
}

/** 比较两个相同尺寸的 RGBA 图，不以文件压缩差异代替像素差异。 */
function pixelDifference(a, b) {
  const first = PNG.sync.read(a), second = PNG.sync.read(b);
  if (first.width !== second.width || first.height !== second.height) return {dimensionsEqual: false, first: [first.width, first.height], second: [second.width, second.height]};
  let different = 0, materialDifferences = 0, maxDelta = 0;
  for (let i = 0; i < first.data.length; i += 4) {let delta = 0; for (let ch = 0; ch < 4; ch++) delta = Math.max(delta, Math.abs(first.data[i + ch] - second.data[i + ch])); if (delta) different++; if (delta > 1) materialDifferences++; maxDelta = Math.max(maxDelta, delta);}
  return {dimensionsEqual: true, width: first.width, height: first.height, differentPixels: different, materialDifferences, ratio: different / (first.width * first.height), maxDelta};
}
/** 在同样页面状态测量关键组件的尺寸、字体、边框、背景和对齐；动态输入 ID 不参与比较。 */
async function metrics(page, selector) {
  return page.locator(selector).evaluateAll(roots => roots.flatMap(root => {
    const base = root.getBoundingClientRect();
    const nodes = [root, ...root.querySelectorAll('h1, h2, h3, .sidebar, .nav-button, .nav-caption, .panel, .form-card, .access-status, .access-status-copy, .editor-title-line, .editor-name-field, .editor-footer, .button, .badge, .input, .switch, .checkbox, .tab-trigger, .member-item, .scope-model-row, .connection-model-row, .model-table-head, .connection-model-actions, .drag-handle, .model-id, .field, .segmented, .u-panel, .u-metric, .u-legend, .u-filter, .u-tabs, table, th, td, select, fieldset, svg')];
    const origin = [base.x + scrollX, base.y + scrollY].map(value => Math.round(value * 100) / 100);
    return [{origin}, ...nodes.map(node => {const r = node.getBoundingClientRect(), s = getComputedStyle(node); return {tag: node.localName, rect: [r.x - base.x, r.y - base.y, r.width, r.height].map(value => Math.round(value * 100) / 100), font: [s.fontFamily, s.fontSize, s.fontWeight, s.lineHeight, s.letterSpacing], color: [s.color, s.backgroundColor, s.borderColor], spacing: [s.padding, s.margin, s.gap], shape: [s.borderWidth, s.borderRadius, s.boxShadow], alignment: [s.display, s.alignItems, s.justifyContent], icon: node.localName === 'svg' ? [node.getAttribute('viewBox'), node.getAttribute('stroke-width'), node.innerHTML] : null};})];
  }));
}
/** 等待字体及绘制提交后从整页截图裁出组件；统一处理半像素弹窗边界，不触发元素截图的独立裁剪栅格化。 */
async function capture(page, selector) {
  await page.evaluate(async () => {await document.fonts.ready; await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));});
  const rect = await page.locator(selector).evaluate(node => {const r = node.getBoundingClientRect(); return {x: r.x + scrollX, y: r.y + scrollY, width: r.width, height: r.height};});
  const full = PNG.sync.read(await page.screenshot({fullPage: true, animations: 'disabled'}));
  const x = Math.floor(rect.x), y = Math.floor(rect.y);
  // Chromium 的整页高度会把尾部不足一像素的空白向下取整；裁剪不能读取未渲染的尾行。
  const width = Math.min(Math.ceil(rect.x + rect.width) - x, full.width - x), height = Math.min(Math.ceil(rect.y + rect.height) - y, full.height - y);
  assert(x >= 0 && y >= 0 && width > 0 && height > 0, 'The compared component must be inside the rendered page.');
  const clip = new PNG({width, height});
  PNG.bitblt(full, clip, x, y, clip.width, clip.height, 0, 0);
  return PNG.sync.write(clip);
}
/** 对照整个共享主内容区域，连接身份验证的真实文案另外标注为产品边界而非静默掩盖。 */
async function compare(reference, actual, selector, label, report) {
  await reference.mouse.move(0, 0); await actual.mouse.move(0, 0); if (selector === '.app-shell') {await reference.locator('body').click({position: {x: 1, y: 1}}); await actual.locator('body').click({position: {x: 1, y: 1}});}
  const layout = {prototype: await metrics(reference, selector), implementation: await metrics(actual, selector)};
  const a = await capture(reference, selector), b = await capture(actual, selector);
  fs.writeFileSync(path.join(output, label + '-prototype.png'), a); fs.writeFileSync(path.join(output, label + '-implementation.png'), b);
  const pixels = pixelDifference(a, b);
  const item = {label, selector, pixels, layout}; report.comparisons.push(item);
  assert.deepEqual(layout.implementation, layout.prototype, label + ': component layout/style differs');
  assert(pixels.dimensionsEqual, label + ': screenshot size differs');
  // 同一 Chrome 的独立栅格层可能产生 1/255 的图标抗锯齿差异；不放宽布局、颜色、字体或任何可见差异。
  assert.equal(pixels.materialDifferences, 0, label + ': visible pixels differ');
  assert(pixels.ratio < 0.0001, label + ': excessive anti-aliasing differences');
  console.log('MATCH ' + label);
}
/** 同时支持桌面侧栏和手机抽屉；仅通过公开控件进入页面，不读写组件内部状态。 */
async function navigate(page, name) {
  let sidebar = page.locator('.sidebar');
  if (!await sidebar.isVisible()) {
    await page.getByRole('button', {name: '打开导航', exact: true}).click();
    sidebar = page.locator('.mobile-sidebar');
  }
  await sidebar.getByRole('button', {name: new RegExp(`^${name}(?: |$)`)}).click();
  await page.getByRole('heading', {name, exact: true, level: 1}).waitFor();
  if (await page.locator('.mobile-sidebar').count()) await page.locator('.mobile-sidebar').waitFor({state: 'hidden'});
}

/** 两侧进入同一连接页签、用量页签或接入配置编辑态；不提交草稿、不切换运行配置。 */
async function enterScene(page, scene) {
  if (scene.connectionTab) await openConnection(page, 'cpa', scene.connectionTab);
  else {
    await navigate(page, scene.page);
    if (scene.usageTab) {
      await page.getByRole('tab', {name: scene.usageTab, exact: true}).click();
      await page.getByRole('status').filter({hasText: '正在读取本地用量'}).waitFor({state: 'hidden'});
    }
    if (scene.id === 'plans' && page.viewportSize().width < 640) await page.locator('[data-plan-id="plan-a"]').click();
  }
  await page.evaluate(() => window.scrollTo(0, 0));
}
/** 与固定原型逐页对照完整工作区、四类连接页签、四类用量页签及弹窗，保留逐像素和样式证据。 */
async function main() {
  const referenceData = readReference();
  fs.mkdirSync(output, {recursive: true});
  const {server, origin} = await serve(), browser = await chromium.launch({headless: true, args: ['--disable-gpu', '--force-color-profile=srgb'], executablePath: process.env.CHROME_PATH || (process.platform === 'win32' ? 'C:/Program Files/Google/Chrome/Application/chrome.exe' : undefined)});
  const report = {reference: prototype, prototypeSHA256: referenceData.prototypeSHA256, productionAssets: referenceData.hashes, bundleEvidence: referenceData.bundleEvidence, fixtureTime: referenceData.time, comparisons: [], errors: [], external: [], productDifferences: ['原型使用合成内存绑定，正式版使用固定 Go 绑定或带令牌的本地配置 API；功能修复允许 JS 不同，组件视觉与样式仍逐像素对照']};
  const scenes = [
    {id: 'plans', page: '接入配置'},
    ...Object.entries({connection: '连接信息', models: '模型选择', purpose: '任务分工', advanced: '高级设置'}).map(([id, connectionTab]) => ({id, connectionTab})),
    ...Object.entries({overview: '概览', analysis: '分析', requests: '请求明细', prices: '价格统计'}).map(([id, usageTab]) => ({id: `usage-${id}`, page: '用量统计', usageTab})),
    {id: 'plugins', page: '插件管理'}, {id: 'help', page: '使用帮助'},
  ];
  try {
    for (const viewport of [{width: 375, height: 812}, {width: 1019, height: 1052}, {width: 1180, height: 840}, {width: 1440, height: 900}]) {
      const actual = await createVisualPage(browser, origin, referenceData, report, viewport, true);
      const reference = await createVisualPage(browser, pathToFileURL(prototype).href, referenceData, report, viewport, false);
      const before = await Promise.all([snapshot(reference), snapshot(actual)]);
      for (const scene of scenes) {
        await enterScene(reference, scene); await enterScene(actual, scene);
        await compare(reference, actual, '.app-shell', `${viewport.width}-${scene.id}`, report);
        if (scene.id === 'models') {
          await reference.getByRole('button', {name: '模型设置 gpt-5-extra', exact: true}).click(); await actual.getByRole('button', {name: '模型设置 gpt-5-extra', exact: true}).click();
          await compare(reference, actual, '.dialog-content', `${viewport.width}-model-dialog`, report);
          await reference.keyboard.press('Escape'); await actual.keyboard.press('Escape');
        }
        if (scene.id === 'connection') {
          await reference.getByRole('button', {name: '添加连接', exact: true}).click(); await actual.getByRole('button', {name: '添加连接', exact: true}).click();
          await compare(reference, actual, '.dialog-content', `${viewport.width}-connection-dialog`, report);
          await reference.keyboard.press('Escape'); await actual.keyboard.press('Escape');
        }
      }
      const after = await Promise.all([snapshot(reference), snapshot(actual)]);
      after.forEach((value, index) => {assert.deepEqual(value.saved, before[index].saved, 'Visual comparison must not save configuration.'); assert.deepEqual(value.state.relay, before[index].state.relay, 'Visual comparison must not switch access.');});
      await reference.close(); await actual.close();
    }
    assert.deepEqual(report.errors, []); assert.deepEqual(report.external, []);
    fs.writeFileSync(path.join(output, 'visual-comparison.json'), JSON.stringify(report, null, 2));
    console.log(`Verified ${report.comparisons.length} pixel/style comparisons (maximum accepted raster difference 1/255 on <0.01% pixels).`);
  } catch (error) {fs.writeFileSync(path.join(output, 'visual-comparison-failure.json'), JSON.stringify({...report, failure: error.stack}, null, 2)); throw error;}
  finally {await browser.close(); server.close();}
}
main().catch(error => {console.error(error); process.exitCode = 1;});
