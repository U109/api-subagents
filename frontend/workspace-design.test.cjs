// 工作区视觉统一回归只使用合成绑定和静态白名单；不读取本机连接，不请求真实模型服务。
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const assert = require('node:assert/strict');
const {pathToFileURL} = require('node:url');
const {chromium} = require('playwright');
const {serve, createPage, openConnection, snapshot, layout, assets, mockBindings} = require('./workbench.test.cjs');
const output = path.resolve(__dirname, '../output/ui-prototype-2026-10-13-unified-workspace');

/** 构造连接、版本与用量示例；所有内容来自测试夹具，时间按当前测试时刻生成。 */
async function makeFixture() {
  const {fixtureConfig, fixtureState} = await import('./tests/fixtures.mjs');
  const config = fixtureConfig(), now = Date.now();
  const usageRecords = Array.from({length: 36}, (_, index) => {
    const connectionId = index % 3 ? 'cpa' : 'worker', input = 12000 + index * 720, output = 1800 + index * 180;
    return {id: `demo-attempt-${index}`, logicalId: `demo-request-${index}`, attempt: 1, connectionId, connection: connectionId, model: connectionId === 'cpa' ? 'gpt-5-extra' : 'gpt-5-mini', source: index % 4 ? 'main' : 'proposal', startedAt: now - (index + 1) * 35 * 60000, durationMS: 1100 + index * 36, firstByteMS: 110 + index * 4, outcome: index % 11 ? 'success' : 'error', quality: 'complete', input, output, total: input + output, read: index % 3 ? 4200 : 0, write: index % 5 ? 1800 : 0, reasoning: 640 + index * 24, costMicroUSD: 36000 + index * 2500};
  });
  return {config, state: fixtureState(config), usageRecords};
}

/** 原型查询只通过公开侧栏按钮进入已知页面；等待合成绑定就绪，不读取内部状态或提交配置。 */
function previewPage() {
  const name = {connections: '我的连接', usage: '用量统计', plugins: '插件管理', help: '使用帮助'}[new URLSearchParams(location.search).get('page')];
  if (!name) return;
  let attempts = 0;
  const timer = setInterval(() => {
    const button = [...document.querySelectorAll('.sidebar .nav-button')].find(node => node.querySelector('.nav-caption')?.textContent === name);
    if (window.__mock?.ready?.configLoaded && button) {clearInterval(timer); button.click();}
    else if (++attempts >= 100) clearInterval(timer);
  }, 50);
}

/** 内联当前构建白名单与合成绑定，生成可离线点击的独立原型；不会打包目录或任何配置文件。 */
function writePrototype(fixture) {
  /** 防止资源文本中的脚本结束标签提前终止 HTML 的内联脚本。 */
  function inlineScript(source) {return `<script>${source.replace(/<\/script/gi, '<\\/script')}</script>`;}
  const read = name => fs.readFileSync(path.join(__dirname, name), 'utf8');
  const html = read('index.html')
    .replace('<title>API Subagents</title>', '<title>API Subagents · 统一工作区原型</title>')
    .replace('<link rel="stylesheet" href="workbench-app.css">', () => `<style>${read('workbench-app.css')}</style>`)
    .replace('<script src="bridge.js" defer></script>', () => inlineScript(`(${mockBindings.toString()})(${JSON.stringify(fixture)});\n${read('bridge.js')}`))
    .replace('<script src="workbench-app.js" defer></script>', '')
    .replace('</body>', () => `${inlineScript(read('workbench-app.js'))}${inlineScript(`(${previewPage.toString()})();`)}</body>`);
  // 使用函数替换，防止 String.replace 将 React 的 $$typeof 或合成文案中的 $& 当作替换模板。
  assert(html.includes(inlineScript(read('workbench-app.js'))), 'The offline prototype must preserve the production bundle verbatim.');
  fs.mkdirSync(output, {recursive: true});
  fs.writeFileSync(path.join(output, 'interactive-prototype.html'), html);
  fs.writeFileSync(path.join(output, 'README.md'), '# 统一工作区原型\n\n以已认可的接入配置为视觉基准，覆盖我的连接与四个详情页签、用量统计的四个页签、插件管理、使用帮助及弹窗。\n\n打开 interactive-prototype.html 即可离线点击；页面内仅有合成测试数据，所有保存、测试连接、接入和更新操作都由内存模拟，不会操作真实配置或请求付费 API。\n\n重新构建与验证：在 frontend 目录运行 npm run build 与 npm run test:workspace-design。截图和 report.json 与原型位于同一目录。\n');
}

/** 为应用侧栏入口提供桌面与手机共用的导航，所有操作只走公开控件。 */
async function navigate(page, name) {
  let nav = page.locator('.sidebar');
  if (!await nav.isVisible()) {
    await page.getByRole('button', {name: '打开导航', exact: true}).click();
    nav = page.locator('.mobile-sidebar');
  }
  await nav.getByRole('button', {name: new RegExp(`^${name}(?: |$)`)}).click();
  await page.getByRole('heading', {name, exact: true, level: 1}).waitFor();
  if (await page.locator('.mobile-sidebar').count()) await page.locator('.mobile-sidebar').waitFor({state: 'hidden'});
}

/** 保存合成场景截图并检查布局；触屏模拟只截当前视口，避免 Chromium 全页截图重置触控媒体状态。 */
async function shot(page, name, report, fullPage = true) {
  await layout(page, name, report);
  await page.screenshot({path: path.join(output, `${name}.png`), fullPage, animations: 'disabled'});
  report.screenshots.push(`${name}.png`);
}

/** 按页面检查正文、控件及辅助文字的最终字号；随 rem 与手机断点变化，防止紧凑样式再次缩小文字。 */
async function readableTypography(page, id, report) {
  const measurements = await page.evaluate(id => {
    const unit = parseFloat(getComputedStyle(document.documentElement).fontSize), mobile = innerWidth <= 639;
    const body = unit * (mobile ? 1 : .875), emphasis = unit * (mobile ? 1 : .9375), secondary = unit * .8125, caption = unit * .75;
    const selectors = {
      plans: [['.plan-list h2', body], ['.scope-sort-row .model-id', body], ['.save-actions .button', body]],
      connections: [['.connection-list-heading h2', body], ['.tab-trigger', body], ['.connection-workbench-editor .field label', body], ['.connection-workbench-editor .input', emphasis]],
      usage: [['.u-tabs button', body], ['.u-kpi > span', secondary], ['.u-kpi > small', secondary], ['.u-legend button', secondary], ['.u-axis', caption]],
      plugins: [['.plugin-content > p', emphasis], ['.plugin-install dt', secondary]],
      help: [['.help-steps span', body], ['.help-steps strong', emphasis]],
      models: [['.connection-model-row .model-id', body], ['.connection-model-toolbar .segmented button', secondary]],
      dialog: [['.dialog-content .field label', body], ['.dialog-content .input', emphasis], ['.dialog-content .button', body]],
    };
    return [['.sidebar .nav-caption', emphasis], ...selectors[id]].map(([selector, expected]) => {
      const node = document.querySelector(selector);
      return {selector, expected, actual: node ? parseFloat(getComputedStyle(node).fontSize) : null};
    });
  }, id);
  for (const item of measurements) {
    assert.notEqual(item.actual, null, `${id}: missing typography sample ${item.selector}`);
    assert.ok(Math.abs(item.actual - item.expected) < .01, `${id}: typography drift ${JSON.stringify(item)}`);
  }
  report.typography.push({page: id, width: page.viewportSize().width, measurements});
}

/** 读取图标控件的实际尺寸、颜色与 SVG 路径，用于比较两个页面，而不是仅断言各自的类名。 */
async function controlAppearance(page, selector) {
  return page.locator(selector).first().evaluate(node => {
    const style = getComputedStyle(node), box = node.getBoundingClientRect(), svg = node.querySelector('svg');
    return {width: box.width, height: box.height, padding: style.padding, radius: style.borderRadius, color: style.color, background: style.backgroundColor, svg: svg ? {width: svg.getAttribute('width'), height: svg.getAttribute('height'), color: getComputedStyle(svg).color, path: svg.innerHTML} : null};
  });
}

/** 逐项复核六条浏览器标注：默认蓝星、统一手柄与标签、右靠操作列、中性删除及稳定的蓝绿黄趋势配色。 */
async function browserCommentAlignment(page, report) {
  await page.emulateMedia({reducedMotion: 'reduce'});
  const before = await snapshot(page);
  const star = await page.locator('.scope-sort-row.is-default .star-button').evaluate(node => ({color: getComputedStyle(node).color, fill: getComputedStyle(node.querySelector('svg')).fill}));
  assert.deepEqual(star, {color: 'rgb(0, 106, 220)', fill: 'rgb(0, 106, 220)'}, 'configuration default star must be blue');
  const referenceDrag = await controlAppearance(page, '.scope-sort-row .scope-drag-handle');
  const referenceDelete = await controlAppearance(page, '.editor-item-actions .icon-delete');
  const labelProperties = ['color', 'backgroundColor', 'fontSize', 'lineHeight', 'padding', 'borderRadius'];
  const referenceLabel = await page.locator('.scope-sort-row .default-caption').first().evaluate((node, properties) => Object.fromEntries(properties.map(key => [key, getComputedStyle(node)[key]])), labelProperties);
  await page.locator('.editor-item-actions .icon-delete').hover();
  const referenceDeleteHover = await controlAppearance(page, '.editor-item-actions .icon-delete');
  await page.mouse.move(0, 0);
  await shot(page, '1019-comment-blue-default-star', report);

  await openConnection(page, 'cpa', '模型选择');
  assert.deepEqual(await controlAppearance(page, '.connection-model-row .drag-handle'), referenceDrag, 'connection drag handle must match access configuration');
  const connectionLabel = await page.locator('.connection-model-row .default-caption').first().evaluate((node, properties) => Object.fromEntries(properties.map(key => [key, getComputedStyle(node)[key]])), labelProperties);
  assert.deepEqual(connectionLabel, referenceLabel, 'connection default label must match access configuration');
  assert.equal(await page.locator('.connection-model-row .default-caption svg').count(), 0, 'default label must not keep the old green star badge');
  assert.deepEqual(await controlAppearance(page, '.connection-workbench-editor .heading-actions .icon-delete'), referenceDelete, 'connection delete must match access configuration');
  await page.locator('.connection-workbench-editor .heading-actions .icon-delete').hover();
  assert.deepEqual(await controlAppearance(page, '.connection-workbench-editor .heading-actions .icon-delete'), referenceDeleteHover, 'delete hover feedback must stay consistent');
  await page.mouse.move(0, 0);
  const alignment = await page.evaluate(() => {
    const heading = document.querySelector('.model-table-head > span:last-child').getBoundingClientRect();
    const buttons = document.querySelector('.connection-model-row .model-row-actions').querySelectorAll('button');
    const first = buttons[0].getBoundingClientRect(), last = buttons[buttons.length - 1].getBoundingClientRect();
    const name = document.querySelector('.connection-model-row .model-id').getBoundingClientRect();
    const nameHeading = document.querySelector('.model-table-head > span:first-child').getBoundingClientRect();
    return {headingCenter: (heading.left + heading.right) / 2, buttonsCenter: (first.left + last.right) / 2, headingRight: heading.right, buttonsRight: last.right, nameLeft: name.left, nameHeadingLeft: nameHeading.left};
  });
  assert.ok(Math.abs(alignment.headingCenter - alignment.buttonsCenter) <= 1, `operation heading must align with actual buttons: ${JSON.stringify(alignment)}`);
  assert.ok(Math.abs(alignment.headingRight - alignment.buttonsRight) <= 1, 'operation heading must move to the right with its buttons');
  assert.ok(Math.abs(alignment.nameLeft - alignment.nameHeadingLeft) <= 1, 'model name heading must stay aligned after changing the handle');
  await shot(page, '1019-comment-aligned-connection-controls', report);

  await navigate(page, '用量统计');
  await page.locator('.u-kpi').first().waitFor();
  const fills = await page.locator('.u-plot svg rect').evaluateAll(nodes => [...new Set(nodes.filter(node => Number(node.getAttribute('height')) > 0).map(node => node.getAttribute('fill')))]);
  assert.deepEqual(fills, ['#437ef7', '#35b58c'], 'the first two data-bearing series must be blue and green');
  const legendColors = await page.locator('.u-legend button > span').evaluateAll(nodes => nodes.map(node => getComputedStyle(node).backgroundColor));
  assert.deepEqual(legendColors.slice(0, 3), ['rgb(67, 126, 247)', 'rgb(53, 181, 140)', 'rgb(212, 160, 23)'], 'legend palette must start with blue, green, yellow');
  await page.locator('.u-filters select').nth(2).selectOption('worker');
  await page.waitForFunction(() => {const bars = [...document.querySelectorAll('.u-plot svg rect')].filter(node => Number(node.getAttribute('height')) > 0); return bars.length > 0 && bars.every(node => node.getAttribute('fill') === '#35b58c');});
  assert.deepEqual(await page.locator('.u-legend button > span').evaluateAll(nodes => nodes.map(node => getComputedStyle(node).backgroundColor)), legendColors, 'filtering must not change series colors');
  await page.locator('.u-filters select').nth(2).selectOption('all');
  await shot(page, '1019-comment-blue-green-token-trend', report);
  const after = await snapshot(page);
  assert.deepEqual(after.saved, before.saved, 'visual review must not save configuration');
  assert.deepEqual(after.state.relay, before.state.relay, 'visual review must not switch access');
  report.alignment = {star, referenceDrag, referenceLabel, referenceDelete, alignment, fills, legendColors};
  report.checks.push('six-browser-comments-control-alignment-and-blue-green-yellow-series');
}

/** 计算不透明文本与背景的实际对比度，验证蓝色连接选中项的可读性。 */
function contrast(foreground, background) {
  /** 按 sRGB 转换相对亮度；输入仅为浏览器计算样式中的 RGB 颜色。 */
  function luminance(color) {
    const channels = color.match(/[\d.]+/g).slice(0, 3).map(value => Number(value) / 255).map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4);
    return channels[0] * .2126 + channels[1] * .7152 + channels[2] * .0722;
  }
  const a = luminance(foreground), b = luminance(background);
  return (Math.max(a, b) + .05) / (Math.min(a, b) + .05);
}

/** 预览仅提供静态白名单，并注入同一套合成绑定，不开放真实后端或任意本机文件。 */
async function servePreview(fixture) {
  const server = http.createServer((request, response) => {
    const name = new URL(request.url, 'http://localhost').pathname.slice(1) || 'index.html';
    if (!assets.includes(name)) {response.writeHead(404); response.end(); return;}
    response.setHeader('Content-Type', name.endsWith('.css') ? 'text/css' : name.endsWith('.js') ? 'text/javascript' : 'text/html; charset=utf-8');
    let content = fs.readFileSync(path.join(__dirname, name));
    if (name === 'index.html') content = content.toString().replace('</head>', () => `<script>(${mockBindings.toString()})(${JSON.stringify(fixture)});</script></head>`).replace('</body>', () => `<script>(${previewPage.toString()})();</script></body>`);
    response.end(content);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  console.log(`Synthetic workspace preview: http://127.0.0.1:${server.address().port}`);
}

/** 验证浏览器标注、跨页主题、字号与文字缩放、布局、焦点、保存边界及离线原型；全部非本地网络都被阻断。 */
async function main() {
  const fixture = await makeFixture();
  writePrototype(fixture);
  if (process.argv.includes('--preview')) {await servePreview(fixture); return;}
  const {server, origin} = await serve();
  const browser = await chromium.launch({headless: true, executablePath: process.env.CHROME_PATH || (process.platform === 'win32' ? 'C:/Program Files/Google/Chrome/Application/chrome.exe' : undefined)});
  const report = {checks: [], layouts: [], typography: [], screenshots: [], errors: [], external: []};
  let page;
  try {
    page = await createPage(browser, origin, fixture, report, {width: 1019, height: 1052});
    await browserCommentAlignment(page, report);
    await page.close(); page = null;
    for (const viewport of [{width: 1440, height: 900}, {width: 1200, height: 878}, {width: 900, height: 800}, {width: 375, height: 812}, {width: 320, height: 740}, {width: 900, height: 375}]) {
      page = await createPage(browser, origin, fixture, report, viewport);
      const before = await snapshot(page);
      const reference = await page.locator('.app-shell').evaluate(node => ({accent: getComputedStyle(node).getPropertyValue('--accent').trim(), background: getComputedStyle(node).backgroundColor, font: getComputedStyle(node).fontFamily}));
      for (const [name, id] of [['接入配置', 'plans'], ['我的连接', 'connections'], ['用量统计', 'usage'], ['插件管理', 'plugins'], ['使用帮助', 'help']]) {
        if (id !== 'plans') await navigate(page, name);
        if (id === 'connections') await page.locator('.connection-workbench-editor').waitFor();
        if (id === 'usage') await page.locator('.u-kpi').first().waitFor();
        const theme = await page.locator('.app-shell').evaluate(node => ({accent: getComputedStyle(node).getPropertyValue('--accent').trim(), background: getComputedStyle(node).backgroundColor, font: getComputedStyle(node).fontFamily}));
        assert.deepEqual(theme, reference, `${viewport.width} ${id}: workspace theme drift`);
        assert.equal(theme.accent, '#006adc');
        await readableTypography(page, id, report);
        if (viewport.width >= 640) {
          assert.equal(await page.locator('.sidebar').evaluate(node => Math.round(node.getBoundingClientRect().width)), 208);
          assert.equal(await page.locator('.sidebar .sidebar-update').isVisible(), true);
        }
        const label = `${viewport.width}x${viewport.height}-${id}`;
        if (viewport.width === 1440 || viewport.width === 375) await shot(page, label, report); else await layout(page, label, report);
      }
      const after = await snapshot(page);
      assert.deepEqual(after.saved, before.saved, 'viewing pages must not save configuration');
      assert.deepEqual(after.state.relay, before.state.relay, 'viewing pages must not switch access');
      report.checks.push(`${viewport.width}x${viewport.height}: readable-typography-shared-theme-and-read-only-navigation`);
      if (viewport.width === 1440 || viewport.width === 375) {
        await openConnection(page);
        const selected = await page.locator('.connection-directory-open[aria-pressed="true"]').evaluate(node => ({background: getComputedStyle(node).backgroundColor, color: getComputedStyle(node.querySelector('strong')).color}));
        assert(contrast(selected.color, selected.background) >= 4.5, 'selected connection contrast');
        for (const [name, id] of [['连接信息', 'info'], ['模型选择', 'models'], ['任务分工', 'purpose'], ['高级设置', 'advanced']]) {
          await page.getByRole('tab', {name, exact: false}).click();
          if (id === 'models') await readableTypography(page, 'models', report);
          await shot(page, `${viewport.width}-connection-${id}`, report);
        }
        await navigate(page, '用量统计');
        for (const [name, id] of [['概览', 'overview'], ['分析', 'analysis'], ['请求明细', 'requests'], ['价格统计', 'prices']]) {
          const tab = page.getByRole('tab', {name, exact: true});
          await tab.click();
          await shot(page, `${viewport.width}-usage-${id}`, report);
        }
      }
      await page.close(); page = null;
    }

    for (const viewport of [{width: 900, height: 800}, {width: 375, height: 812}]) {
      page = await createPage(browser, origin, fixture, report, viewport);
      const before = await snapshot(page);
      await page.evaluate(() => {document.documentElement.style.fontSize = '20px';});
      for (const [name, id] of [['接入配置', 'plans'], ['我的连接', 'connections'], ['用量统计', 'usage'], ['插件管理', 'plugins'], ['使用帮助', 'help']]) {
        if (id !== 'plans') await navigate(page, name);
        if (id === 'usage') await page.locator('.u-kpi').first().waitFor();
        await readableTypography(page, id, report);
        await layout(page, `${viewport.width}-text-125-percent-${id}`, report);
      }
      const after = await snapshot(page);
      assert.deepEqual(after.saved, before.saved, 'enlarging text must not save configuration');
      assert.deepEqual(after.state.relay, before.state.relay, 'enlarging text must not switch access');
      report.checks.push(`${viewport.width}: 125-percent-default-text-size`);
      await page.close(); page = null;
    }

    page = await createPage(browser, origin, fixture, report);
    await openConnection(page);
    const input = page.getByRole('textbox', {name: '连接名称', exact: true});
    await input.focus();
    assert.equal(await input.evaluate(node => getComputedStyle(node).outlineStyle), 'solid');
    await page.getByRole('button', {name: '添加连接', exact: true}).click();
    await page.getByRole('dialog').waitFor();
    assert.equal(await page.getByRole('dialog').evaluate(node => getComputedStyle(node).getPropertyValue('--primary').trim()), '#006adc');
    await readableTypography(page, 'dialog', report);
    await shot(page, '1204-add-connection-dialog', report);
    await page.keyboard.press('Escape');
    await page.getByRole('dialog').waitFor({state: 'hidden'});
    assert.equal(await page.getByRole('button', {name: '添加连接', exact: true}).evaluate(node => node === document.activeElement), true);
    const before = await snapshot(page);
    await input.fill('统一样式测试连接');
    await page.getByRole('button', {name: '保存连接', exact: true}).click();
    await page.waitForFunction(() => window.__mock.snapshot().saved.models['统一样式测试连接']);
    const after = await snapshot(page);
    assert.deepEqual(after.state.relay, before.state.relay, 'connection save must not switch access');
    report.checks.push('dialog-theme-focus-return-and-connection-save-boundary');
    await page.close();

    const touch = await browser.newContext({viewport: {width: 375, height: 812}, isMobile: true, hasTouch: true, reducedMotion: 'reduce'});
    page = await touch.newPage();
    page.on('pageerror', error => report.errors.push(error.message));
    await page.route('**/*', route => {if (route.request().url().startsWith(origin)) return route.continue(); report.external.push(route.request().url()); return route.abort();});
    await page.addInitScript(mockBindings, fixture); await page.goto(origin); await page.waitForFunction(() => window.__mock?.ready?.configLoaded);
    await openConnection(page, 'cpa', '模型选择');
    assert.equal(await page.getByRole('button', {name: '手动添加', exact: true}).evaluate(node => node.getBoundingClientRect().height >= 44), true);
    assert.equal(await page.locator('.connection-directory-open').first().evaluate(node => getComputedStyle(node).transitionDuration), '0s');
    await shot(page, '375-touch-models', report, false);
    await navigate(page, '插件管理');
    await page.getByRole('button', {name: '打开导航', exact: true}).click();
    await shot(page, '375-mobile-navigation', report, false);
    await page.keyboard.press('Escape');
    report.checks.push('coarse-pointer-targets-reduced-motion-and-mobile-navigation');
    await touch.close(); page = null;

    page = await browser.newPage({viewport: {width: 1200, height: 878}});
    page.on('pageerror', error => report.errors.push(error.message));
    await page.route('**/*', route => {if (route.request().url().startsWith('file:')) return route.continue(); report.external.push(route.request().url()); return route.abort();});
    await page.goto(pathToFileURL(path.join(output, 'interactive-prototype.html')).href + '?page=connections');
    await page.waitForFunction(() => window.__mock?.ready?.configLoaded);
    await page.getByRole('heading', {name: '我的连接', exact: true, level: 1}).waitFor();
    await navigate(page, '用量统计'); await page.locator('.u-kpi').first().waitFor();
    assert.equal(await page.locator('.u-filters select').count(), 4);
    report.checks.push('self-contained-offline-prototype');
    assert.deepEqual(report.errors, []); assert.deepEqual(report.external, []);
    fs.writeFileSync(path.join(output, 'report.json'), JSON.stringify(report, null, 2));
    console.log(`Verified ${report.checks.length} groups, ${report.layouts.length} layouts, ${report.screenshots.length} screenshots; no external requests.`);
  } catch (error) {
    fs.writeFileSync(path.join(output, 'failure.json'), JSON.stringify({...report, failure: error.stack}, null, 2));
    if (page && !page.isClosed()) await page.screenshot({path: path.join(output, 'failure.png'), fullPage: true});
    throw error;
  } finally {if (page && !page.isClosed()) await page.close(); await browser.close(); server.close();}
}

if (require.main === module) main().catch(error => {console.error(error); process.exitCode = 1;});
