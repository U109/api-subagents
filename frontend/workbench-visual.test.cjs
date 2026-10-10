// 使用原型的同一组脱敏数据对照正式页面；不读取本机连接，不请求任何模型服务。
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const assert = require('node:assert/strict');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'C:/Users/zhang/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const {mockBindings,assets} = require('./workbench.test.cjs');
const prototype = path.resolve(__dirname,'../output/ui-prototype-2026-10-08-cc-switch/interactive-prototype.html');
const output = path.resolve(__dirname,'../output/workbench-implementation-2026-10-09/visual-'+(process.env.VISUAL_STAGE || 'final'));

/** 提取布局、字号、边框和图标的最终计算值，避免只检查是否存在元素。 */
async function measurements(page,selectors) {
  return page.evaluate(selectors => Object.fromEntries(Object.entries(selectors).map(([name,selector]) => {
    const node = document.querySelector(selector);
    if (!node) return [name,null];
    const style = getComputedStyle(node), box = node.getBoundingClientRect();
    const keys = ['fontFamily','fontSize','fontWeight','lineHeight','color','backgroundColor','borderColor','borderRadius','padding','margin','gap','strokeWidth'];
    return [name,{box:{x:box.x,y:box.y,width:box.width,height:box.height},...Object.fromEntries(keys.map(key => [key,style[key]]))}];
  })),selectors);
}

/** 按确认原型转换成正式配置格式，凭据仅使用测试替身，目录容量保持一一对应。 */
function fixtureFrom(profiles) {
  const models = {},catalogs = {};
  for (const p of profiles) {
    const selected = p.models.filter(m => m.selected).map(m => m.id);
    models[p.name] = {protocol:'responses',baseUrl:p.url,apiKey:'synthetic-test-secret',model:p.defaultID,relayModels:selected.filter(id => id !== p.defaultID),modelOrder:selected,modelOfficialContexts:Object.fromEntries(p.models.filter(m => m.selected && m.context).map(m => [m.id,m.context])),description:p.description,reasoningEffort:p.effort,advancedOverride:p.override,maxTokens:p.maxTokens,stream:p.response==='stream',firstResponseTimeoutSeconds:p.firstTimeout,streamIdleTimeoutSeconds:p.idleTimeout,taskTimeoutMinutes:p.taskTimeout};
    catalogs[p.name] = p.models.map(m => ({id:m.id,contextWindow:m.context || undefined}));
  }
  return {models,catalogs,state:{update:{phase:'idle',autoCheck:false,availableVersion:'0.4.4',progress:0,message:'检查是否有新版本'}}};
}

/** 校验不依赖版本、真实文案或字段数量的设计值；正文高度允许真实内容变化，核心字号、卡片边框和响应式间距必须对齐原型。 */
function assertApprovedStyles(samples) {
  const rules = {
    workspace:['fontFamily','fontSize'],
    title:['fontFamily','fontSize','fontWeight','lineHeight'],
    tabs:['fontSize','gap','borderRadius'],
    card:['padding','borderRadius'],
    grid:['gap'],
    search:['fontSize','fontWeight','borderRadius','padding'],
    override:['gap'],
    switch:['borderRadius','backgroundColor'],
    footer:['padding','gap','backgroundColor'],
  };
  let checks = 0;
  for (const sample of samples) {
    for (const [name,properties] of Object.entries(rules)) {
      const reference = sample.prototype[name], formal = sample.formal[name];
      if (!reference || !formal) continue;
      for (const property of properties) {
        assert.equal(formal[property],reference[property],`${sample.width} ${sample.page} ${name}.${property} differs from the approved prototype`);
        checks++;
      }
    }
    if (['model','connection','purpose','advanced','overview','help'].includes(sample.page)) {
      for (const name of ['tabs','card']) {
        const reference = sample.prototype[name], formal = sample.formal[name];
        if (!reference || !formal || !reference.box.width || !formal.box.width) continue;
        for (const property of ['x','width']) {
          assert(Math.abs(formal.box[property]-reference.box[property])<=1,`${sample.width} ${sample.page} ${name}.${property} is misaligned`);
          checks++;
        }
      }
    }
  }
  return checks;
}

/** 在隔离浏览器中生成同尺寸页面对照，并保存可复核的计算样式报告。 */
async function main() {
  fs.mkdirSync(output,{recursive:true});
  const server=http.createServer((req,res) => {
    const name=new URL(req.url,'http://localhost').pathname.slice(1) || 'index.html';
    if (!assets.includes(name)) { res.writeHead(404); res.end(); return; }
    res.setHeader('Content-Type',name.endsWith('.css')?'text/css':name.endsWith('.js')?'text/javascript':'text/html');
    res.end(fs.readFileSync(path.join(__dirname,name)));
  });
  await new Promise(resolve => server.listen(0,'127.0.0.1',resolve));
  const origin='http://127.0.0.1:'+server.address().port;
  const browser=await chromium.launch({executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe',headless:true});
  const reference=await browser.newPage({viewport:{width:1073,height:884}});
  const context=await browser.newContext({viewport:{width:1073,height:884}});
  const errors=[],outside=[],report=[];
  try {
    await reference.goto('file:///'+prototype.replace(/\\/g,'/'));
    const fixture=fixtureFrom(await reference.evaluate(() => state.profiles));
    await context.route('**/*',route => { if(route.request().url().startsWith(origin+'/')) return route.continue(); outside.push(route.request().url()); return route.abort(); });
    await context.addInitScript(mockBindings,fixture);
    const formal=await context.newPage();
    formal.on('pageerror',error => errors.push(error.message));
    await formal.goto(origin); await formal.waitForFunction(() => window.__mock?.ready);
    await formal.locator('#pull-models').click();
    await formal.waitForFunction(() => document.querySelector('#model-all-count')?.textContent === '8');
    await formal.locator('#model-filter-selected').click();
    const pairs={
      shell:[{sidebar:'.sidebar',brand:'.brand',logo:'.brand-mark',header:'.appbar',workspace:'.page',title:'.page-heading h1',subtitle:'.title-copy p',route:'.route-bar',tabs:'.tabs',footer:'.actionbar'},{sidebar:'.sidebar',brand:'.brand',logo:'.brand-icon',header:'.top-actions',workspace:'.workspace',title:'#connection-title',subtitle:'.page-intro',route:'.route-bar',tabs:'.tabs',footer:'.action-bar'}],
      model:[{card:'.models-settings',heading:'.models-settings .form-section-heading',search:'.model-toolbar .search-box',refresh:'[data-action="refresh-models"]',table:'.model-table',row:'tbody tr',name:'.model-id',icon:'.drag-handle svg',efforts:'.reasoning-options',effort:'.effort'},{card:'#panel-model',heading:'#panel-model .form-section-heading',search:'.model-search-wrap',refresh:'#pull-models',table:'.model-catalog',row:'.model-catalog-row',name:'.model-catalog-name',icon:'.model-drag-handle svg',efforts:'.reasoning-options',effort:'.effort-option'}],
      connection:[{card:'.connection-settings',field:'.connection-settings .field',input:'#connection-name',divider:'.connection-settings .form-divider'},{card:'#panel-connection',field:'#panel-connection .field',input:'#name',divider:'#panel-connection .form-divider'}],
      purpose:[{card:'#tab-panel .form-card',input:'#purpose',tags:'.purpose-tag'},{card:'#panel-purpose',input:'#description',tags:'.purpose-tag'}],
      advanced:[{card:'.advanced-settings',override:'.override-row',switch:'.override-row .switch',fields:'.three-fields'},{card:'#panel-advanced',override:'.override-row',switch:'.override-row .switch',fields:'.timeout-fields'}],
      overview:[{toolbar:'.overview-toolbar',search:'#connection-search',card:'.provider-card',top:'.provider-card-top',name:'.provider-name',chips:'.model-chips',bottom:'.provider-card-bottom'},{toolbar:'.overview-toolbar',search:'#connection-search',card:'.provider-card',top:'.provider-top',name:'.provider-name',chips:'.model-chips',bottom:'.provider-bottom'}],
      plugin:[{card:'.plugin-card',heading:'.plugin-card-heading',features:'.plugin-features',versions:'.plugin-versions',status:'.plugin-flow-status',actions:'.plugin-card .panel-actions'},{card:'.plugin-card',heading:'.plugin-card-heading',features:'.plugin-features',versions:'.plugin-versions',status:'#plugin-state',actions:'.plugin-actions'}],
      help:[{grid:'.feature-grid',card:'.feature-card',icon:'.feature-icon',block:'.help-block'},{grid:'.feature-grid',card:'.feature-card',icon:'.feature-icon',block:'.help-block'}],
      update:[{dialog:'#update-dialog',header:'#update-dialog .dialog-header',body:'.update-body',versions:'.update-versions',footer:'#update-dialog .dialog-footer'},{dialog:'#update-dialog',header:'.update-header',body:'.update-body',versions:'.update-version-grid',footer:'.update-footer'}],
    };
    for(const width of [1366,1073,751,390,320]) {
      await reference.setViewportSize({width,height:884}); await formal.setViewportSize({width,height:884});
      for(const name of ['model','connection','purpose','advanced','overview','plugin','help','update']) {
        if (['model','connection','purpose','advanced'].includes(name)) {
          await reference.evaluate(tab => { state.page='detail'; state.tab=tab==='model'?'models':tab; render(); },name);
          await formal.evaluate(() => document.querySelector('.model-select[data-name="cpa"]').click());
          await formal.locator('#tab-'+name).click();
        } else if(name==='update') {
          await reference.evaluate(() => { state.update.auto=false; });
          await reference.locator('#app-update-entry').click();
          await formal.locator('#update-entry').click();
        } else {
          await reference.evaluate(page => { state.page=page; render(); },name);
          await formal.evaluate(page => document.querySelector('#'+page+'-entry').click(),name);
        }
        await reference.mouse.move(0,0); await formal.mouse.move(0,0);
        await reference.screenshot({animations:'disabled',path:path.join(output,`${width}-${name}-prototype.png`)});
        await formal.screenshot({animations:'disabled',path:path.join(output,`${width}-${name}-formal.png`)});
        const left={...pairs.shell[0],...pairs[name][0]},right={...pairs.shell[1],...pairs[name][1]};
        report.push({width,page:name,prototype:await measurements(reference,left),formal:await measurements(formal,right)});
        if(name==='update') { await reference.locator('#update-dialog [data-close]').click(); await formal.locator('#update-dialog [data-close]').click(); }
      }
      await reference.evaluate(() => { state.page='detail'; state.tab='models'; render(); });
      await formal.evaluate(() => document.querySelector('.model-select[data-name="cpa"]').click());
      await formal.locator('#tab-model').click();
      for (const name of ['edit','manual']) {
        await reference.locator(name === 'edit' ? '[data-action="edit-model"]' : '[data-action="add-model"]').first().click();
        await formal.locator(name === 'edit' ? '[data-rename-model]' : '#add-manual-model').first().click();
        const dialog = formal.locator(name === 'edit' ? '#rename-model-dialog' : '#manual-model-dialog');
        const box = await dialog.boundingBox();
        assert(box.x >= 0 && box.y >= 0 && box.x + box.width <= width + 1 && box.y + box.height <= 885,`${width} ${name} dialog outside viewport`);
        assert(await dialog.locator('.dialog-footer').isVisible(),`${width} ${name} footer hidden`);
        await reference.mouse.move(0,0); await formal.mouse.move(0,0);
        await reference.screenshot({animations:'disabled',path:path.join(output,`${width}-${name}-prototype.png`)});
        await formal.screenshot({animations:'disabled',path:path.join(output,`${width}-${name}-formal.png`)});
        report.push({width,page:name,prototype:await measurements(reference,{dialog:'#model-dialog',header:'#model-dialog .dialog-header',footer:'#model-dialog .dialog-footer'}),formal:await measurements(formal,{dialog:name==='edit'?'#rename-model-dialog':'#manual-model-dialog',header:(name==='edit'?'#rename-model-dialog':'#manual-model-dialog')+' .dialog-header',footer:(name==='edit'?'#rename-model-dialog':'#manual-model-dialog')+' .dialog-footer'})});
        await reference.locator('#model-dialog [data-close]').first().click(); await dialog.locator('[data-close]').click();
      }
    }
    assert.deepEqual(errors,[]); assert.deepEqual(outside,[]);
    const visualAssertions = assertApprovedStyles(report);
    fs.writeFileSync(path.join(output,'comparison.json'),JSON.stringify({report,visualAssertions,scriptErrors:errors,outsideRequests:outside},null,2));
    console.log(`Visual comparison: ${report.length} page pairs, ${visualAssertions} approved-style checks saved to ${output}`);
  } finally { await browser.close(); server.close(); }
}
if(require.main===module) main().catch(error => { console.error(error); process.exitCode=1; });
