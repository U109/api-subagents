// 用量工作台真实绑定回归：只用合成响应和本地静态资源，不调用 Go 的真实连接或付费模型。
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const {chromium} = require('playwright');
const {mockBindings} = require('./tests/mock-bindings.cjs');

/** 提供固定静态资源白名单，不接受任何测试模型或本机文件操作请求。 */
async function serve() {
  const names=['index.html','workbench-app.js','workbench-app.css','bridge.js'];
  const server=http.createServer((req,res)=>{const name=new URL(req.url,'http://localhost').pathname.slice(1)||'index.html';if(!names.includes(name)){res.writeHead(404);res.end();return;}res.setHeader('Content-Type',name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html');res.end(fs.readFileSync(path.join(__dirname,name)));});
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));return {server,origin:`http://127.0.0.1:${server.address().port}`};
}

/** 验证真实绑定、精简后的图表键盘访问、费率保存及响应式布局；只读取合成记录和本地静态资源。 */
async function run() {
  const {fixtureConfig,fixtureState}=await import('./tests/fixtures.mjs');
  const config=fixtureConfig(),now=Date.now();
  const record={id:'attempt-1',logicalId:'request-1',attempt:1,connectionId:'cpa',connection:'cpa',model:'gpt-5-extra',source:'main',startedAt:now-10000,durationMS:1000,firstByteMS:100,outcome:'success',quality:'complete',input:100,output:20,total:120,read:0,write:0,reasoning:4,costMicroUSD:400};
  const fixture={config,state:fixtureState(config),usageRecords:[record,{...record,id:'attempt-2',attempt:2},{...record,id:'unknown',logicalId:'request-2',source:'proposal',input:null,output:null,total:null,read:null,write:null,reasoning:null,costMicroUSD:null,quality:'unreported'}]};
  const {server,origin}=await serve(),browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const errors=[],network=[],checks=[],layouts=[];
  try {
    const page=await browser.newPage({viewport:{width:1204,height:884}});
    page.on('pageerror',error=>errors.push(error.message));
    await page.route('**/*',route=>{if(route.request().url().startsWith(origin))return route.continue();network.push(route.request().url());return route.abort();});
    await page.addInitScript(mockBindings,fixture);await page.goto(origin);await page.waitForFunction(()=>window.__mock?.ready?.configLoaded);
    await page.locator('.sidebar .primary-nav').getByRole('button',{name:'用量统计',exact:true}).click();
    const panel=page.locator('.usage-reference');await panel.locator('.u-kpi').first().waitFor();
    assert.equal(await panel.getByRole('tab').count(),4);assert.equal(await panel.locator('.u-filters select').count(),4);
    assert.equal(await panel.locator('.u-kpi strong').first().innerText(),'2');assert.match(await panel.innerText(),/上游上报/);assert.equal((await panel.innerText()).includes('合成数据'),false);
    checks.push('real-record-shape-no-production-samples');
    assert.equal(await panel.locator('.u-chart-footer').count(),0);
    assert.equal(await panel.locator('.u-breakdown .u-secondary').count(),0);
    const reader=panel.getByRole('slider',{name:'选择趋势时段',exact:true});
    await reader.focus();await reader.press('Home');assert.equal(await reader.inputValue(),'0');
    await panel.getByRole('tooltip').waitFor();assert.match(await reader.getAttribute('aria-valuetext'),/Token.*请求/);
    assert.equal(await panel.locator('.u-plot').evaluate(el=>getComputedStyle(el).outlineStyle),'solid');
    await reader.press('ArrowRight');assert.equal(await reader.inputValue(),'1');
    await reader.press('End');assert.equal(await reader.inputValue(),'23');
    await panel.getByRole('combobox',{name:'用量时间范围',exact:true}).selectOption('7d');
    assert.equal(await reader.inputValue(),'6');
    await reader.focus();await reader.press('Home');assert.equal(await reader.inputValue(),'0');
    await panel.getByRole('combobox',{name:'用量时间范围',exact:true}).selectOption('24h');
    checks.push('compact-chart-retains-keyboard-readout-and-safe-range-changes');
    await panel.getByRole('tab',{name:'请求明细',exact:true}).click();assert.equal(await panel.locator('tbody tr').count(),2);
    const unknown=panel.locator('tbody tr').filter({hasText:'未上报'});assert.equal(await unknown.count(),1);assert.match(await unknown.innerText(),/—/);
    await panel.getByRole('button',{name:'查看请求 request-1',exact:true}).click();await page.getByRole('dialog').waitFor();assert.match(await page.getByRole('dialog').innerText(),/上游尝试/);await page.keyboard.press('Escape');await page.getByRole('dialog').waitFor({state:'hidden'});
    checks.push('logical-request-retry-and-unknown-details');
    await panel.getByRole('tab',{name:'价格统计',exact:true}).click();
    assert.equal((await panel.innerText()).includes('缓存与推理不会重复计费'),false);
    assert.equal((await panel.innerText()).includes('仅用于这份离线原型'),false);
    const inputs=panel.getByRole('spinbutton');await inputs.first().fill('2');await inputs.nth(1).fill('8');await inputs.nth(2).fill('2');await inputs.nth(3).fill('2');
    await panel.getByRole('button',{name:'保存费率',exact:true}).click();await panel.getByRole('status').filter({hasText:'费率已保存'}).waitFor();
    const calls=await page.evaluate(()=>window.__mock.snapshot().calls);assert.ok(calls.some(call=>call.route==='/api/usage/prices'));
    checks.push('manual-rate-save-through-fixed-api');
    await page.getByRole('button',{name:'收起侧栏',exact:true}).click();await page.locator('.sidebar .primary-nav').getByRole('button',{name:/我的连接/}).click();
    assert.equal(await page.locator('.connection-workbench-list').count(),1);await page.getByRole('button',{name:'管理连接 worker',exact:true}).click();assert.match(await page.locator('.connection-workbench-editor h2').first().innerText(),/worker/);
    checks.push('production-sidebar-and-master-detail');
    await page.getByRole('button',{name:'展开侧栏',exact:true}).click();
    for(const width of [375,640,900,1204]){
      await page.setViewportSize({width,height:884});
      if(width===375){await page.getByRole('button',{name:'打开导航',exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'用量统计',exact:true}).click();}
      else await page.locator('.sidebar .primary-nav').getByRole('button',{name:'用量统计',exact:true}).click();
      await panel.getByRole('tab',{name:'概览',exact:true}).click();await panel.locator('.u-kpi').first().waitFor();
      const shape=await page.evaluate(()=>({width:innerWidth,scroll:document.documentElement.scrollWidth}));assert.ok(shape.scroll<=width+1);layouts.push(shape);
    }
    assert.deepEqual(errors,[]);assert.deepEqual(network,[]);
    console.log(JSON.stringify({checks:checks.length,layouts:layouts.length,errors:errors.length,externalRequests:network.length}));
  } finally {await browser.close();await new Promise(resolve=>server.close(resolve));}
}

run().catch(error=>{console.error(error);process.exitCode=1;});
