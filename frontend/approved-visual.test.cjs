// 已确认原型与正式界面的同数据、同尺寸对照；不访问本机连接，不允许非本地请求。
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {pathToFileURL} = require('node:url');
const {chromium} = require('playwright');
const {PNG} = require('pngjs');
const {serve, createPage, openConnection, output} = require('./workbench.test.cjs');
const prototype = path.resolve(process.env.APPROVED_PROTOTYPE || path.join(__dirname, '../output/ui-prototype-2026-10-10-access-simplified/interactive-prototype.html'));

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
    const nodes = [root, ...root.querySelectorAll('h1, h2, h3, .panel, .form-card, .access-status, .access-status-copy, .editor-title-line, .editor-name-field, .editor-footer, .button, .badge, .input, .switch, .checkbox, .tab-trigger, .member-item, .scope-model-row, .connection-model-row, .drag-handle, .model-id, .field, .segmented, svg')];
    return nodes.map(node => {const r = node.getBoundingClientRect(), s = getComputedStyle(node); return {tag: node.localName, rect: [r.x - base.x, r.y - base.y, r.width, r.height].map(value => Math.round(value * 100) / 100), font: [s.fontFamily, s.fontSize, s.fontWeight, s.lineHeight, s.letterSpacing], color: [s.color, s.backgroundColor, s.borderColor], spacing: [s.padding, s.margin, s.gap], shape: [s.borderWidth, s.borderRadius, s.boxShadow], alignment: [s.display, s.alignItems, s.justifyContent], icon: node.localName === 'svg' ? [node.getAttribute('viewBox'), node.getAttribute('stroke-width'), node.innerHTML] : null};});
  }));
}
/** 先等待字体及绘制提交，再从整页截图裁出主内容；不临时扩大视口，避免改变 SVG 栅格化状态。 */
async function capture(page, selector) {
  await page.evaluate(async () => {await document.fonts.ready; await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));});
  if (selector !== '#main') return page.locator(selector).screenshot({animations: 'disabled'});
  const rect = await page.locator(selector).evaluate(node => {const r = node.getBoundingClientRect(); return {x: r.x + scrollX, y: r.y + scrollY, width: r.width, height: r.height};});
  const full = PNG.sync.read(await page.screenshot({fullPage: true, animations: 'disabled'}));
  const clip = new PNG({width: Math.round(rect.width), height: Math.round(rect.height)});
  PNG.bitblt(full, clip, Math.round(rect.x), Math.round(rect.y), clip.width, clip.height, 0, 0);
  return PNG.sync.write(clip);
}
/** 对照整个共享主内容区域，连接身份验证的真实文案另外标注为产品边界而非静默掩盖。 */
async function compare(reference, actual, selector, label, report) {
  await reference.mouse.move(0, 0); await actual.mouse.move(0, 0); if (selector !== '.dialog-content') {await reference.locator('body').click({position: {x: 1, y: 1}}); await actual.locator('body').click({position: {x: 1, y: 1}});}
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
/** 将正式页面和原型分别导航到同一业务页面，正式页面没有示例预览入口。 */
async function navigateActual(page, preview) {
  if (['connection', 'models', 'purpose', 'advanced'].includes(preview)) await openConnection(page, 'cpa', {connection: '连接信息', models: '模型选择', purpose: '任务分工', advanced: '高级设置'}[preview]);
  else if (preview === 'plugins') await page.locator('.sidebar .nav-button').filter({hasText: '插件管理'}).click();
  else if (preview === 'help') {if (await page.locator('.sidebar').isVisible()) await page.locator('.sidebar .nav-button').filter({hasText: '使用帮助'}).click(); else {await page.getByRole('button', {name: '打开导航', exact: true}).click(); await page.locator('.mobile-sidebar .nav-button').filter({hasText: '使用帮助'}).click();}}
  else if (preview === 'connections') {if (await page.locator('.sidebar').isVisible()) await page.locator('.sidebar .nav-button').filter({hasText: '我的连接'}).click(); else {await page.getByRole('button', {name: '打开导航', exact: true}).click(); await page.locator('.mobile-sidebar .nav-button').filter({hasText: '我的连接'}).click();}}
}
/** 全部对照在独立浏览器及合成绑定内完成，输出可复查的截图和测量报告。 */
async function main() {
  assert(fs.existsSync(prototype), 'Set APPROVED_PROTOTYPE to the approved self-contained HTML.');
  fs.mkdirSync(output, {recursive: true});
  const {fixtureConfig, fixtureState} = await import(pathToFileURL(path.join(__dirname, 'tests/fixtures.mjs')));
  const {server, origin} = await serve(), browser = await chromium.launch({headless: true, executablePath: process.env.CHROME_PATH || (process.platform === 'win32' ? 'C:/Program Files/Google/Chrome/Application/chrome.exe' : undefined)});
  const report = {reference: prototype, comparisons: [], errors: [], external: [], productDifferences: ['正式版不显示演示场景入口及原型标记', '身份验证使用真实可编辑/脱敏 Key；新建连接提供接口类型选择', '更新弹窗使用后台版本与下载校验结果，并保留自动检查、跳过及重启安装的实际操作']};
  try {
    for (const width of [390, 1079, 1204, 1440]) {
      for (const preview of ['plans-a', 'plans-many', 'connections', 'models', 'purpose', 'advanced', 'help', ...(width > 640 ? ['plugins'] : [])]) {
        const config = fixtureConfig(preview === 'plans-many' ? 24 : 2), fixture = {config, state: fixtureState(config)};
        const actual = await createPage(browser, origin, fixture, report, {width, height: 884});
        const reference = await browser.newPage({viewport: {width, height: 884}});
        reference.on('pageerror', error => report.errors.push(error.message));
        await reference.route('**/*', route => {if (route.request().url().startsWith('file:')) return route.continue(); report.external.push(route.request().url()); return route.abort();});
        await reference.goto(pathToFileURL(prototype).href + '?preview=' + preview); await reference.waitForFunction(() => Boolean(window.prototypeAudit));
        await navigateActual(actual, preview);
        if (width < 640 && preview.startsWith('plans')) {await reference.locator('[data-plan-id="plan-a"]').click(); await actual.locator('[data-plan-id="plan-a"]').click();}
        await compare(reference, actual, '#main', `${width}-${preview}`, report);
        if (preview === 'models') {
          await reference.getByRole('button', {name: '模型设置 gpt-5-extra', exact: true}).click(); await actual.getByRole('button', {name: '模型设置 gpt-5-extra', exact: true}).click();
          await compare(reference, actual, '.dialog-content', `${width}-model-dialog`, report);
        }
        await reference.close(); await actual.close();
      }
    }
    assert.deepEqual(report.errors, []); assert.deepEqual(report.external, []);
    fs.writeFileSync(path.join(output, 'visual-comparison.json'), JSON.stringify(report, null, 2));
    console.log(`Verified ${report.comparisons.length} pixel/style comparisons (maximum accepted raster difference 1/255 on <0.01% pixels).`);
  } catch (error) {fs.writeFileSync(path.join(output, 'visual-comparison-failure.json'), JSON.stringify({...report, failure: error.stack}, null, 2)); throw error;}
  finally {await browser.close(); server.close();}
}
main().catch(error => {console.error(error); process.exitCode = 1;});
