const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require('playwright');
const {serve, createPage, openConnection, layout, snapshot} = require('./workbench.test.cjs');

const output = path.resolve(__dirname, '../output/workbench-implementation-20261015');

/** 通过公开导航切换页面，小屏使用 Radix 抽屉，不调用组件内部状态。 */
async function navigate(page, name) {
  const nav = page.locator('.sidebar .nav-button').filter({hasText: name});
  if (await nav.isVisible()) await nav.click();
  else {await page.getByRole('button', {name: '打开导航', exact: true}).click(); await page.locator('.mobile-sidebar .nav-button').filter({hasText: name}).click();}
}

/** 度量真实滚动容器，确认滚动列表时页面与固定工具条都不移动。 */
async function localScroll(page, selector, fixed) {
  const before = await page.locator(fixed).boundingBox();
  const shape = await page.locator(selector).evaluate(node => {
    const result = {scroll: node.scrollHeight, client: node.clientHeight};
    node.scrollTop = 150; return {...result, top: node.scrollTop};
  });
  assert.ok(shape.scroll > shape.client && shape.top > 0, `${selector}: ${JSON.stringify(shape)}`);
  const after = await page.locator(fixed).boundingBox();
  assert.ok(Math.abs(before.y - after.y) < 1);
  assert.ok(await page.evaluate(() => document.documentElement.scrollHeight <= innerHeight + 1));
}

/** 使用正式构建和本地内存绑定验证已确认的原型交互、布局和破坏性操作边界。 */
async function run() {
  fs.mkdirSync(output, {recursive: true});
  const {fixtureConfig, fixtureState} = await import('./tests/fixtures.mjs');
  const config = fixtureConfig(), now = Date.now();
  for (let i = 0; i < 50; i++) {
    const id = `local-model-${String(i).padStart(2, '0')}`;
    config.models.cpa.relayModels.push(id); config.models.cpa.modelOrder.push(id);
    config.models.cpa.modelCatalog.push({id, context: 128000});
  }
  config.models.worker.relayModels.push('gpt-5-extra'); config.models.worker.modelOrder.push('gpt-5-extra');
  const rate = {input: 1, output: 2, read: 0, write: 0, fast: {input: 2, output: 4, read: 0, write: 0}, longContext: {above: 200000, rate: {input: 3, output: 6, read: 0, write: 0}}};
  const prices = {revision: 'r1', rates: {'["cpa","gpt-5-extra"]': rate, '["worker","gpt-5-extra"]': structuredClone(rate), hidden: {input: 7, output: 8, read: 0, write: 0}}};
  const usageRecords = Array.from({length: 40}, (_, i) => ({id: `attempt-${i}`, logicalId: `request-${i}`, attempt: 1, connectionId: 'cpa', connection: 'cpa', model: 'gpt-5-extra', source: 'main', startedAt: now - (i + 1) * 1000, outcome: 'success', quality: 'complete', input: 100, output: 20, total: 120, read: 0, write: 0, reasoning: 0, costMicroUSD: 100}));
  const fixture = {config, state: fixtureState(config), usageRecords, usagePrices: prices};
  const report = {groups: [], errors: [], external: [], layouts: []};
  const {server, origin} = await serve();
  const browser = await chromium.launch({headless: true, executablePath: process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  let page;
  try {
    page = await createPage(browser, origin, fixture, report, {width: 1105, height: 884});
    const name = page.getByRole('textbox', {name: '配置名称', exact: true});
    assert.equal(await name.getAttribute('readonly'), '');
    await page.getByRole('button', {name: '编辑配置名称', exact: true}).click(); await name.fill('配置重命名'); await name.press('Escape');
    assert.equal(await name.inputValue(), '配置 A'); assert.equal(await name.getAttribute('readonly'), '');
    await page.getByRole('button', {name: '编辑配置名称', exact: true}).click(); await name.fill('配置新名称'); await name.press('Enter');
    assert.equal((await snapshot(page)).saved.accessConfigs[0].name, '配置 A');
    await page.locator('[data-plan-id="plan-b"]').click(); assert.equal(await name.getAttribute('readonly'), '');
    assert.equal(await page.locator('.plan-list-meta').count(), 0);
    assert.equal(await page.locator('.plan-list-icon').count(), 2);
    await page.screenshot({path: path.join(output, 'implemented-plans.png')}); report.groups.push('配置名称只读/编辑/撤销/回车/切换与保存隔离，目录图标和计数');

    await openConnection(page);
    const card = page.getByRole('button', {name: '管理连接 cpa', exact: true});
    const before = await card.boundingBox(), textBefore = await card.locator('.directory-provider-name').boundingBox();
    await page.getByRole('textbox', {name: 'API 地址', exact: true}).fill('https://api.example.com/v2');
    const after = await card.boundingBox(), textAfter = await card.locator('.directory-provider-name').boundingBox();
    assert.deepEqual(after, before); assert.equal(textBefore.y, textAfter.y);
    assert.ok(await card.locator('.connection-list-draft').isVisible());
    const icon = await card.locator('.directory-provider-icon').boundingBox(), copy = await card.locator('.directory-provider-copy').boundingBox();
    assert.ok(Math.abs(icon.y + icon.height/2 - copy.y - copy.height/2) < 1);
    assert.equal(await page.locator('.brand-mark img').getAttribute('src').then(src => src.startsWith('data:image/svg+xml')), true);
    assert.equal(await page.locator('.connection-info-panel > .panel-heading').count(), 0);
    await page.getByRole('button', {name: '测试连接', exact: true}).click();
    assert.equal(await page.locator('.connection-info-panel .job-inline').count(), 0);
    await page.getByRole('button', {name: '取消', exact: true}).click(); await page.evaluate(() => window.__mock.completeProbe());
    assert.match(await page.locator('.connection-status-pill').innerText(), /已取消/);
    await page.getByRole('button', {name: '测试连接', exact: true}).click(); await page.evaluate(() => window.__mock.completeProbe());
    await page.locator('.connection-status-pill').filter({hasText: /正常.*ms/}).waitFor();
    const apiKey = page.getByRole('textbox', {name: 'API Key', exact: true});
    await apiKey.fill('local-only-probe-secret');
    assert.equal(await page.locator('.connection-status-pill').innerText(), '未测试');
    await apiKey.fill('');
    assert.equal(await page.locator('.connection-status-pill').innerText(), '未测试', '还原凭据也不能复活旧测试状态');
    await page.getByRole('button', {name: '测试连接', exact: true}).click();
    await page.getByRole('textbox', {name: 'API 地址', exact: true}).fill('https://api.example.com/pending-change');
    assert.equal(await page.locator('.connection-status-pill').innerText(), '测试中');
    await page.getByRole('button', {name: '取消', exact: true}).click(); await page.evaluate(() => window.__mock.completeProbe());
    assert.equal(await page.locator('.connection-status-pill').innerText(), '未测试', '来源改变仍可取消，迟到结果不归入新来源');
    await page.getByRole('textbox', {name: 'API 地址', exact: true}).fill('https://api.example.com/v2');
    assert.equal(await page.locator('.connection-status-pill').innerText(), '未测试');
    await page.getByRole('button', {name: '测试连接', exact: true}).click(); await page.evaluate(() => window.__mock.completeProbe());
    await page.locator('.connection-status-pill').filter({hasText: /正常.*ms/}).waitFor();
    await page.getByRole('textbox', {name: 'API 地址', exact: true}).fill('https://api.example.com/v3');
    assert.equal(await page.locator('.connection-status-pill').innerText(), '未测试');
    await page.screenshot({path: path.join(output, 'implemented-connection-info.png')}); report.groups.push('卡片状态不跳动、图标居中、统一 Logo、真实测试耗时/取消与修改失效');

    await page.getByRole('tab', {name: '模型选择', exact: false}).click();
    assert.equal(await page.getByText('模型目录', {exact: true}).count(), 0);
    await localScroll(page, '.connection-model-scroll', '.connection-model-toolbar');
    await page.screenshot({path: path.join(output, 'implemented-connection-models.png')});
    report.groups.push('模型表头吸顶、仅列表滚动、工具栏和保存不动');

    await navigate(page, '用量统计'); await page.getByRole('tab', {name: '价格统计', exact: true}).click();
    await page.locator('.u-price-panel legend').first().waitFor();
    assert.equal(await page.locator('.u-price-panel legend').filter({hasText: /^gpt-5-extra$/}).count(), 1);
    const price = page.getByRole('spinbutton', {name: 'gpt-5-extra Fast 模式 普通输入单价', exact: true});
    await price.fill('3.25'); await localScroll(page, '.u-prices', '.u-price-actions');
    await page.screenshot({path: path.join(output, 'implemented-usage-prices.png')});
    await page.getByRole('tab', {name: '请求明细', exact: true}).click();
    await page.setViewportSize({width: 901, height: 600});
    await localScroll(page, '.u-requests-panel .u-table-wrap', '.u-pagination');
    await page.screenshot({path: path.join(output, 'implemented-usage-requests.png')});
    const saved = (await snapshot(page)).saved;
    const clear = page.getByRole('button', {name: '清除全部请求统计', exact: true});
    await clear.click(); assert.match(await page.getByRole('alertdialog').innerText(), /所有连接、所有时间范围/);
    await page.getByRole('button', {name: '取消', exact: true}).click(); assert.equal(await page.locator('.u-request-table tbody tr').count(), 8);
    assert.equal(await clear.evaluate(node => node === document.activeElement), true);
    await page.evaluate(() => {window.__mock.failure = '/api/usage/clear';});
    await clear.click(); await page.getByRole('button', {name: '清除全部记录', exact: true}).click();
    await page.getByRole('alertdialog').getByText('synthetic operation failed', {exact: true}).waitFor();
    assert.equal(await page.locator('.u-request-table tbody tr').count(), 8);
    await page.getByRole('button', {name: '取消', exact: true}).click();
    // 模拟清除前已发起但尚未返回的账本读取，清除完成后再释放旧结果。
    await page.evaluate(() => {
      const original = window.go.desktop.App.API;
      window.go.desktop.App.API = async (route, body) => {
        const result = await original(route, body);
        if (route === '/api/usage') return new Promise(resolve => {window.__releaseUsage = () => resolve(result);});
        return result;
      };
    });
    await page.getByRole('button', {name: '刷新用量记录', exact: true}).click(); await page.waitForFunction(() => typeof window.__releaseUsage === 'function');
    await clear.click(); await page.getByRole('button', {name: '清除全部记录', exact: true}).click();
    await page.getByRole('alertdialog').waitFor({state: 'hidden'}); await page.evaluate(() => window.__releaseUsage());
    await page.getByText('当前筛选下暂无请求', {exact: true}).waitFor();
    assert.equal(await page.locator('.u-request-table tbody tr').count(), 0); assert.deepEqual((await snapshot(page)).saved, saved);
    await page.getByRole('tab', {name: '价格统计', exact: true}).click(); assert.equal(await price.inputValue(), '3.25');
    await page.getByRole('button', {name: '保存费率', exact: true}).click(); await page.getByText('费率已保存，将用于后续请求', {exact: true}).waitFor();
    const payload = (await snapshot(page)).calls.filter(call => call.route === '/api/usage/prices').at(-1).body;
    const storedPrices = JSON.parse(payload).rates;
    assert.equal(storedPrices['["worker","gpt-5-extra"]'].fast.input, 3.25);
    assert.equal(storedPrices['["cpa","gpt-5-extra"]'].longContext.above, 200000); assert.equal(storedPrices.hidden.input, 7);
    report.groups.push('按模型归并分层价格，清除确认/取消/失败/焦点/迟到读取隔离，价格草稿和配置保留');
    await page.close(); page = null;

    const specialConfig = {version: 1, models: {cpa: {...config.models.cpa, id: '__proto__'}}, accessConfigs: [], lastAccess: null};
    page = await createPage(browser, origin, {config: specialConfig, state: fixtureState(specialConfig)}, report);
    await openConnection(page);
    await page.getByRole('button', {name: '测试连接', exact: true}).click(); await page.evaluate(() => window.__mock.completeProbe());
    await page.locator('.connection-status-pill').filter({hasText: /正常.*ms/}).waitFor();
    await page.getByRole('textbox', {name: 'API 地址', exact: true}).fill('https://api.example.com/special-id');
    assert.equal(await page.locator('.connection-status-pill').innerText(), '未测试');
    report.groups.push('合法特殊连接 ID 不读取对象原型，来源代次仍能使旧测试结果失效');
    await page.close(); page = null;

    for (const viewport of [{width: 375, height: 812}, {width: 812, height: 375}, {width: 1105, height: 884}, {width: 901, height: 600}]) {
      const mobile = viewport.width < 901;
      page = await createPage(browser, origin, fixture, report, viewport);
      await page.emulateMedia({reducedMotion: 'reduce'});
      await layout(page, `${viewport.width}-plans`, report);
      await openConnection(page, 'worker', '模型选择'); await layout(page, `${viewport.width}-models`, report);
      if (mobile) assert.ok(await page.evaluate(() => document.documentElement.scrollHeight > innerHeight));
      else assert.ok(await page.evaluate(() => document.documentElement.scrollHeight <= innerHeight + 1));
      if (viewport.width === 375) await page.screenshot({path: path.join(output, 'implemented-mobile-models.png'), fullPage: true});
      await navigate(page, '插件管理'); assert.equal(await page.locator('main > .page-heading').count(), 0);
      await layout(page, `${viewport.width}-plugins`, report);
      await page.close(); page = null;
    }
    assert.deepEqual(report.errors, []); assert.deepEqual(report.external, []);
    fs.writeFileSync(path.join(output, 'implementation-qa.json'), JSON.stringify(report, null, 2));
    console.log(`Verified ${report.groups.length} feature groups, ${report.layouts.length} layouts; no script errors/external requests.`);
  } catch (error) {
    if (page && !page.isClosed()) await page.screenshot({path: path.join(output, 'implementation-failure.png'), fullPage: true});
    throw error;
  } finally {await page?.close(); await browser.close(); server.close();}
}

run().catch(error => {console.error(error); process.exitCode = 1;});
