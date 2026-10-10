// 正式静态资源的浏览器回归：Go 绑定只用内存合成数据，所有外部网络均阻断，不读本机配置或调用模型。
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const assert = require('node:assert/strict');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'C:/Users/zhang/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const assets = ['index.html','config.css','model-picker.css','desktop.css','approved-workbench.css','bridge.js','shell-ui.js','notification-ui.js','select-ui.js','model-picker-ui.js','config-operations.js','workbench-views.js','config-ui.js','relay-diagnostics-ui.js','desktop-ui.js'];
const output = path.resolve(__dirname,'../output/workbench-implementation-2026-10-09');

/** 注入仅属于测试页面的固定 Go 绑定，记录定点写入和取消；已保存 Key 从不进入正式表单响应。 */
function mockBindings(fixture) {
  /** 克隆合成绑定值，避免状态对象与表单草稿互相修改。 */
  const copy = value => JSON.parse(JSON.stringify(value));
  const base = {protocol:'responses',baseUrl:'http://127.0.0.1:9/v1',apiKey:'synthetic-test-secret',model:'gpt-5-extra',relayModels:['gpt-5.1-fast-high','deepseek-v4-flash'],modelOrder:['gpt-5-extra','gpt-5.1-fast-high','deepseek-v4-flash'],modelOfficialContexts:{'gpt-5-extra':1000000,'gpt-5.1-fast-high':128000},maxTokens:8000,stream:false,firstResponseTimeoutSeconds:300,streamIdleTimeoutSeconds:200,taskTimeoutMinutes:30,advancedOverride:false,reasoningEffort:'high',description:''};
  let saved = {version:1,models:{cpa:copy(base),review:{...copy(base),description:'review saved'},batch:copy(base)}}, listener, dirty=false, token=0, pendingUpdate, pendingProbe;
  const calls = [], state = {version:'0.4.1',plugin:{phase:'installed',installedVersion:'0.5.0',bundledVersion:'0.5.0',message:'插件已安装'},pluginUpdate:{phase:'idle',progress:0},update:{phase:'idle',autoCheck:true,progress:0,message:'检查是否有新版本'},relay:{enabled:false,model:'',message:'保存连接后开启，重启 Codex 后生效',requests:0},close:{}};
  if (fixture?.models) saved.models = copy(fixture.models);
  if (fixture?.state) Object.assign(state,copy(fixture.state));
  /** 返回脱敏的完整配置快照，用 savedName 标记真实凭据来源。 */
  const editable = () => ({version:1,models:Object.fromEntries(Object.entries(saved.models).map(([name,p]) => [name,{...copy(p),apiKey:'',hasKey:Boolean(p.apiKey),savedName:name}]))});
  /** 推送克隆状态，测试不共享前端内部对象或假装访问真实服务。 */
  const emit = () => { if(listener) listener(copy(state)); return copy(state); };
  /** 根据草稿合并保存 Key，只持久化明确目标连接。 */
  const persist = (name,p) => { const source=p.savedName || name, old=saved.models[source]; if(source!==name && saved.models[name]) throw Error('名称冲突'); const next=copy(p); next.apiKey=p.apiKey || old?.apiKey || ''; delete next.savedName; delete next.hasKey; if(source!==name) delete saved.models[source]; saved.models[name]=next; };
  /** 模拟阶段切换和迟到结果；取消递增令牌并结束原操作，旧完成函数不能再次广播成功。 */
  const updateOperation = download => {
    const mine=++token;
    Object.assign(state.update,{phase:download?'downloading':'checking',message:download?'正在下载更新…':'正在检查更新…'}); emit();
    return new Promise(resolve => {
      pendingUpdate = () => { if(mine===token) Object.assign(state.update,download?{phase:'downloaded',progress:100,message:'更新已下载'}:{phase:'available',availableVersion:'0.4.4',releaseNotes:'修复连接与模型设置\n<script>untrusted</script>',message:'发现新版本 0.4.4'}); resolve(emit()); };
      if(!window.__mock.holdUpdate) pendingUpdate();
      window.__mock.resolveUpdate = pendingUpdate;
    });
  };
  window.__mock = {
    calls,holdUpdate:false,holdProbe:false,installed:0,
    /** 返回隔离的磁盘替身快照，供测试断言草稿隔离与密钥保留。 */
    saved:() => copy(saved),
    /** 后台更新进度不重建配置页面。 */
    progress(value) { state.update.progress=value; emit(); },
    /** 为测试推送可信绑定快照中的阶段，不在正式资源中提供此入口。 */
    phase(phase) { Object.assign(state.update,{phase,availableVersion:'0.4.4',message:phase,progress:phase==='downloaded'?100:0}); emit(); },
  };
  window.runtime = {
    /** 只注册本页面桌面状态回调。 */
    EventsOn(event,callback) { listener=callback; },
  };
  window.go = {desktop:{App:{
    /** 初始化和刷新只返回版本及安全状态。 */
    GetState:async () => copy(state),
    /** 实现正式固定路由语义，不调用 HTTP 或实际文件系统。 */
    async API(route,body) {
      const input=body ? JSON.parse(body):null; calls.push({route,input:copy(input)});
      if(route==='/api/config') return {config:editable(),path:'isolated-test/models.json'};
      if(route==='/api/config/save') { persist(input.name,input.config.models[input.name]); return {config:editable()}; }
      if(route==='/api/config/copy') { const name=input.name+'-copy'; persist(name,{...input.config.models[input.name],savedName:undefined,apiKey:saved.models[input.name]?.apiKey}); return {name,config:editable()}; }
      if(route==='/api/config/remove') { delete saved.models[input.name]; return {config:editable()}; }
      if(route==='/api/models') return {models:copy(fixture?.catalogs?.[input.name] || [{id:'gpt-5-extra',contextWindow:1000000},{id:'gpt-5.1-fast-high',contextWindow:128000},{id:'deepseek-v4-flash'},{id:'candidate-model',contextWindow:64000}])};
      if(route==='/api/probe') return new Promise(resolve => { pendingProbe=resolve; window.__mock.resolveProbe=()=>resolve({ok:true,message:'测试成功'}); if(!window.__mock.holdProbe) resolve({ok:true,message:'测试成功'}); });
      throw Error('Unexpected route: '+route);
    },
    /** 请求号取消只结束本次合成测试请求。 */
    CancelProbe:async () => { if(pendingProbe) pendingProbe({cancelled:true,message:'已取消'}); },
    /** 同步草稿标志给安装保护，不接收任何配置数据。 */
    SetDirty:async value => { dirty=value; },
    /** 已保存连接切换以独立快照为准，不持久化其他草稿。 */
    EnableRelay:async name => { if(!saved.models[name]) throw Error('连接未保存'); calls.push({route:'EnableRelay',name}); Object.assign(state.relay,{enabled:true,model:name,activeModel:name,activeModelId:saved.models[name].model,message:'主对话已开启'}); return emit(); },
    /** 关闭仅改变测试状态，不触碰 Codex。 */
    DisableRelay:async () => { state.relay.enabled=false; return emit(); },
    /** 模拟可取消检查。 */
    CheckUpdate:async () => updateOperation(false),
    /** 模拟可取消下载，关闭弹窗不结束 Promise。 */
    DownloadUpdate:async () => updateOperation(true),
    /** 取消网络令牌，不允许旧回调重新就绪。 */
    CancelUpdate:async () => { token++; state.update.phase='cancelled'; state.update.message='当前操作已取消'; if(pendingUpdate) pendingUpdate(); return emit(); },
    /** 更新偏好只影响下次打开弹窗。 */
    SetUpdateAutoCheck:async value => { state.update.autoCheck=value; return emit(); },
    /** 合成跳过操作不下载。 */
    SkipUpdate:async () => { state.update.phase='skipped'; return emit(); },
    /** 断言草稿保护后才进入合成安装阶段。 */
    InstallUpdate:async () => { if(dirty) throw Error('有草稿'); window.__mock.installed++; state.update.phase='installing'; return emit(); },
    /** 插件检查使用独立状态。 */
    CheckPluginUpdate:async () => { state.pluginUpdate={phase:'latest',message:'插件已是最新版本'}; return emit(); },
    /** 本测试不执行插件安装，仅返回现有状态。 */
    InstallPlugin:async () => emit(),
    /** 本测试不执行插件更新，仅返回现有状态。 */
    UpdatePlugin:async () => emit(),
    /** 插件取消不改变应用更新状态。 */
    CancelPluginUpdate:async () => { state.pluginUpdate.phase='cancelled'; return emit(); },
    /** 页面外链操作只记录，不启动系统浏览器。 */
    OpenReleases:async () => emit(),
    /** 退出保护仅返回合成状态。 */
    ConfirmClose:async () => emit(),
    /** 取消退出不改动草稿。 */
    CancelClose:async () => emit(),
    /** 记录真实页面初始化报告。 */
    FrontendReady:report => { window.__mock.ready=report; },
  }}};
}

/** 在固定资源白名单本地服务器和全新浏览器上下文中验证正式行为、视口边界与截图。 */
async function main() {
  fs.mkdirSync(output,{recursive:true});
  const server=http.createServer((req,res) => {
    const name=new URL(req.url,'http://localhost').pathname.slice(1) || 'index.html';
    if(!assets.includes(name)) { res.writeHead(404); res.end(); return; }
    res.setHeader('Content-Type',name.endsWith('.css')?'text/css':name.endsWith('.js')?'text/javascript':'text/html'); res.end(fs.readFileSync(path.join(__dirname,name)));
  });
  await new Promise(resolve => server.listen(0,'127.0.0.1',resolve));
  const origin='http://127.0.0.1:'+server.address().port;
  const browser=await chromium.launch({executablePath:process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe',headless:true});
  const context=await browser.newContext({viewport:{width:1073,height:884}});
  const errors=[],outside=[]; let layouts=0;
  await context.route('**/*',route => { if(route.request().url().startsWith(origin+'/')) return route.continue(); outside.push(route.request().url()); return route.abort(); });
  await context.addInitScript(mockBindings);
  const page=await context.newPage(); page.on('pageerror',error => errors.push(error.stack));
  const groups=[];
  /** 执行一个具名行为组并记录；失败即退出，避免把后续连锁失败误判为独立问题。 */
  async function check(name,operation) { await operation(); groups.push(name); console.log('PASS '+name); }
  /** 等待异步绑定和保存锁完成后再操作下一控件。 */
  async function settled() { await page.waitForFunction(() => window.__mock.ready && !document.querySelector('#save').disabled); }
  /** 切换配置页签并断言只有一个可见面板，不丢失输入。 */
  async function tab(name) { await page.locator('#tab-'+name).click(); assert.equal(await page.locator('#panel-'+name).isVisible(),true); }
  /** 用侧栏管理指定连接；窄屏先打开抽屉。 */
  async function manage(name) { if(await page.locator('#sidebar-toggle').isVisible() && !await page.locator('#connection-sidebar').evaluate(el => !el.inert)) await page.locator('#sidebar-toggle').click(); await page.locator('.model-select[data-name="'+name+'"]').click(); }
  try {
    await page.goto(origin); await settled();
    await check('正式页面初始化、四页签与移除冗余文案',async () => {
      assert.equal(await page.locator('[role=tab]').count(),4); assert.equal(await page.locator('[data-effort]').count(),9);
      assert.equal(await page.locator('details, #remove-picker-model-dialog, .table-footer, .reasoning-caption, .protocol-badge').count(),0);
      assert.equal(await page.locator('#apiKey').inputValue(),''); assert.equal((await page.locator('body').innerText()).includes('synthetic-test-secret'),false);
      assert.equal(await page.locator('.model-catalog-head').isVisible(),true);
      assert.equal(await page.locator('.model-default-tag:visible').count(),1);
      assert.equal(await page.locator('#select-all-models').evaluate(el => el.getBoundingClientRect().width),13);
      assert.equal(await page.locator('#panel-model').evaluate(el => getComputedStyle(el).paddingLeft),'22px');
    });
    await check('总览选连接不进详情，独立管理无协议或模型下拉',async () => {
      await page.locator('#overview-entry').click(); await page.locator('[data-select-connection=review]').click();
      assert.equal(await page.locator('#overview-page').isVisible(),true); assert.equal(await page.locator('[data-select-connection=review]').getAttribute('aria-pressed'),'true');
      assert.equal(await page.locator('#overview-page select').count(),0); assert.equal(await page.locator('#overview-page').innerText().then(s => s.includes('Responses')),false);
      await page.locator('[data-manage-connection=cpa]').click(); await tab('connection');
    });
    await check('总览搜索保留输入焦点，按全部已选模型匹配且不改写选择',async () => {
      await page.locator('#overview-entry').click();
      await page.locator('#connection-search').fill('deepseek');
      assert.equal(await page.locator('.provider-card').count(),3);
      assert.equal(await page.locator('#connection-search:focus').count(),1);
      await page.locator('#connection-search').fill('no-such-connection');
      assert.equal(await page.locator('.provider-card').count(),0);
      assert.equal(await page.locator('.empty-state strong').textContent(),'还没有匹配的连接');
      await page.locator('#connection-search').fill('review');
      assert.equal(await page.locator('.provider-card').count(),1);
      await page.locator('#connection-search').fill('');
      assert.equal(await page.locator('[data-select-connection=cpa]').getAttribute('aria-pressed'),'true');
      await page.locator('[data-manage-connection=cpa]').click(); await tab('model');
      await page.locator('#workspace').focus(); await page.keyboard.press('/');
      assert.equal(await page.locator('#model-search:focus').count(),1);
      assert.equal(await page.locator('#pull-models svg').evaluate(el => getComputedStyle(el).display),'block');
      await tab('connection');
    });
    await check('连接改名后模型回调、目录与凭据来源仍有效',async () => {
      await page.locator('#name').fill('renamed'); await page.locator('#baseUrl').click();
      await tab('model'); await page.locator('#pull-models').click(); await page.waitForFunction(() => document.querySelectorAll('.model-catalog-row').length===4);
      await page.locator('.model-catalog-row[data-model-id="candidate-model"] input').check(); await page.locator('#save').click(); await settled();
      const saved=await page.evaluate(() => window.__mock.saved()); assert(!saved.models.cpa); assert.equal(saved.models.renamed.apiKey,'synthetic-test-secret'); assert(saved.models.renamed.relayModels.includes('candidate-model'));
    });
    await check('定点保存保留其他连接草稿及未保存连接',async () => {
      await manage('review'); await tab('purpose'); await page.locator('#description').fill('review draft');
      await manage('batch'); await tab('purpose'); await page.locator('#description').fill('batch saved'); await page.locator('#save').click(); await settled();
      const saved=await page.evaluate(() => window.__mock.saved()); assert.equal(saved.models.review.description,'review saved'); assert.equal(saved.models.batch.description,'batch saved');
      await manage('review'); await tab('purpose'); assert.equal(await page.locator('#description').inputValue(),'review draft');
    });
    await check('九档思考键盘、用途标签、计数与单连接放弃',async () => {
      await page.locator('[data-purpose="0"]').click(); assert.match(await page.locator('#purpose-count').innerText(),/\/ 300/);
      await tab('model'); await page.locator('[data-effort=ultra]').click(); await page.locator('[data-effort=ultra]').press('Home'); assert.equal(await page.locator('[data-effort=""]').getAttribute('aria-checked'),'true');
      await page.locator('#discard-current').click(); await page.locator('#confirm-discard-current').click(); await tab('purpose'); assert.equal(await page.locator('#description').inputValue(),'review saved');
    });
    await check('高级覆盖保留输入、范围在上方、恢复默认',async () => {
      await tab('advanced'); assert.equal(await page.locator('#maxTokens').isDisabled(),true);
      await page.locator('#advanced-override').click(); await page.locator('#maxTokens').fill('12000'); await page.locator('#advanced-override').click();
      assert.equal(await page.locator('#maxTokens').inputValue(),'12000'); assert.equal(await page.locator('#maxTokens').isDisabled(),true);
      assert.match(await page.locator('#firstResponseTimeoutSeconds').locator('..').innerText(),/10–600 秒/);
      await page.locator('#reset-advanced').click(); assert.equal(await page.locator('#maxTokens').inputValue(),'4096'); assert.equal(await page.locator('#advanced-override').getAttribute('aria-checked'),'false');
      await page.locator('#save').click(); await settled();
    });
    await check('模型容量、全选、默认切换、即时移除与撤销',async () => {
      await manage('renamed'); await tab('model'); await page.locator('#pull-models').click();
      const row=page.locator('.model-catalog-row[data-model-id="gpt-5-extra"]'); assert.match(await row.innerText(),/256K/);
      await row.locator('[data-remove-model]').click(); await page.locator('[data-notice-key=model-remove] .notice-action').click(); assert.equal(await row.locator('input').isChecked(),true);
      await page.locator('.model-catalog-row[data-model-id="candidate-model"] [data-set-default]').click(); assert.equal(await page.locator('.model-catalog-row[data-model-id="candidate-model"] [data-set-default]').getAttribute('aria-pressed'),'true');
      assert.equal(await page.locator('.model-catalog-row[data-model-id="candidate-model"] .model-default-tag').isVisible(),true); assert.equal(await row.locator('.model-default-tag').isVisible(),false);
      await page.locator('#select-all-models').uncheck(); assert.equal(await page.locator('#picker').getAttribute('data-selected-count'),'0');
      await page.locator('#select-all-models').check(); assert.equal(await page.locator('#picker').getAttribute('data-selected-count'),'4');
      await page.locator('#save').click(); await settled();
    });
    await check('手动模型、编辑校验和搜索不改写选择',async () => {
      await page.locator('#add-manual-model').click(); await page.locator('#manual-model-id').fill('custom-model'); await page.locator('#manual-model-form button[type=submit]').click();
      const row=page.locator('.model-catalog-row[data-model-id="custom-model"]'); assert.equal(await row.locator('input').isChecked(),true);
      await row.locator('[data-rename-model]').click(); await page.locator('#edit-model-id').fill(''); await page.locator('#rename-model-form button[type=submit]').click(); assert.equal(await page.locator('#rename-model-dialog').isVisible(),true);
      await page.locator('#edit-model-id').fill('custom-model'); await page.locator('#model-display-name').fill('手动模型'); await page.locator('#rename-model-form button[type=submit]').click(); assert.match(await row.innerText(),/手动模型/);
      await page.locator('#model-search').fill('手动模型'); assert.equal(await page.locator('.model-catalog-row').count(),1); assert.equal(await row.locator('[data-reorder-model]').getAttribute('aria-disabled'),'true');
      await page.locator('#clear-model-search').click(); assert.equal(await page.locator('#picker').getAttribute('data-selected-count'),'5');
      await page.locator('#save').click(); await settled(); assert.equal(await page.evaluate(() => window.__mock.saved().models.renamed.modelNames['custom-model']),'手动模型');
    });
    await check('新连接默认关闭覆盖、草稿放弃不删除其他保存项',async () => {
      await page.locator('#add').click(); assert.equal(await page.locator('#panel-connection').isVisible(),true); const name=await page.locator('#name').inputValue();
      await tab('advanced'); assert.equal(await page.locator('#advanced-override').getAttribute('aria-checked'),'false'); assert.equal(await page.locator('#firstResponseTimeoutSeconds').inputValue(),'180');
      await page.locator('#discard-current').click(); await page.locator('#confirm-discard-current').click(); assert.equal(await page.locator('.model-select[data-name="'+name+'"]').count(),0); assert.equal(Object.keys(await page.evaluate(() => window.__mock.saved().models)).length,3);
    });
    await check('主对话跟随已保存连接，草稿不切换且不丢失',async () => {
      await page.locator('#relay-switch').click(); await page.waitForFunction(() => document.querySelector('#relay-switch').getAttribute('aria-checked')==='true');
      assert.equal(await page.locator('#relay-label').evaluate(el => el.classList.contains('warm')),true);
      await manage('batch'); await page.waitForFunction(() => window.__mock.calls.some(c => c.route==='EnableRelay' && c.name==='batch'));
      await tab('purpose'); await page.locator('#description').fill('batch unsaved'); await manage('review'); await page.waitForFunction(() => window.__mock.calls.some(c => c.route==='EnableRelay' && c.name==='review'));
      const before=await page.evaluate(() => window.__mock.calls.filter(c => c.route==='EnableRelay').length); await manage('batch');
      assert.equal(await page.evaluate(() => window.__mock.calls.filter(c => c.route==='EnableRelay').length),before); await tab('purpose'); assert.equal(await page.locator('#description').inputValue(),'batch unsaved');
      await page.locator('#save').click(); await settled(); await page.waitForFunction(() => window.__mock.calls.filter(c => c.route==='EnableRelay' && c.name==='batch').length===2);
    });
    await check('图标复制定位到连接页，原 Key 不回显',async () => {
      await page.locator('#copy-current').click(); await settled(); assert.equal(await page.locator('#panel-connection').isVisible(),true); assert.equal(await page.locator('#name').inputValue(),'batch-copy'); assert.equal(await page.locator('#apiKey').inputValue(),'');
    });
    await check('真实测试入口可取消且迟到成功不覆盖',async () => {
      await page.evaluate(() => { window.__mock.holdProbe=true; }); await page.locator('#probe').click(); await page.locator('#cancel-probe').click();
      await page.waitForFunction(() => document.querySelector('#cancel-probe').hidden); await page.evaluate(() => window.__mock.resolveProbe()); assert.match(await page.locator('#probe-result').innerText(),/取消/);
    });
    await check('更新弹窗保持草稿与页签，关闭≠取消，真实偏好与迟到保护',async () => {
      await tab('purpose'); await page.locator('#description').fill('modal preserved'); await page.evaluate(() => { window.__mock.holdUpdate=true; });
      await page.locator('#update-entry').click(); await page.locator('#update-dialog [data-close]').click(); assert.equal(await page.locator('#update-dialog').isVisible(),false); assert.equal(await page.locator('#description').inputValue(),'modal preserved');
      await page.locator('#update-entry').click(); await page.locator('#cancel-update').click(); await page.waitForFunction(() => !document.querySelector('#recheck-update').disabled);
      await page.evaluate(() => window.__mock.resolveUpdate()); assert.match(await page.locator('#update-state').innerText(),/取消/);
      await page.locator('#update-auto-check').uncheck(); await page.waitForFunction(() => !document.querySelector('#update-auto-check').disabled);
      await page.locator('#update-dialog [data-close]').click(); await page.locator('#update-entry').click(); assert.equal(await page.locator('#update-title').innerText(),'更新操作已取消');
      await page.evaluate(() => { window.__mock.holdUpdate=false; }); await page.locator('#recheck-update').click(); await page.waitForFunction(() => !document.querySelector('#check-update').disabled);
      assert.match(await page.locator('#update-notes').innerText(),/<script>untrusted/); assert.equal(await page.locator('#update-notes script').count(),0);
      await page.locator('#skip-update').click(); await page.waitForFunction(() => document.querySelector('#update-title').textContent==='已跳过此版本');
      assert.equal(await page.locator('#check-update').isVisible(),false); await page.locator('#recheck-update').click(); await page.waitForFunction(() => !document.querySelector('#check-update').disabled);
      await page.locator('#check-update').click(); await page.waitForFunction(() => document.querySelector('#update-title').textContent==='更新已准备好');
      assert.equal(await page.locator('#check-update').isDisabled(),true); assert.equal(await page.locator('#update-draft-note').isVisible(),true); assert.equal(await page.evaluate(() => window.__mock.installed),0);
      await page.locator('#update-dialog [data-close]').click(); assert.equal(await page.locator('#description').inputValue(),'modal preserved');
    });
    await check('插件与帮助为独立页面，返回保留连接草稿',async () => {
      await page.locator('#plugin-entry').click(); assert.equal(await page.locator('#plugin-page').isVisible(),true); await page.locator('#check-plugin-update').click();
      assert.equal(await page.locator('.plugin-card-heading').count(),1); assert.equal(await page.locator('.plugin-features span').count(),2);
      await page.locator('#plugin-help-entry').click(); assert.equal(await page.locator('#help-page').isVisible(),true);
      assert.equal(await page.locator('.feature-card').count(),2); assert.equal(await page.locator('.help-block').count(),3);
      await page.locator('#help-request-diagnostics').click(); assert.equal(await page.locator('#relay-diagnostics-dialog').isVisible(),true);
      await page.evaluate(() => window.relayDiagnostics.render({enabled:true,recentRequests:Array.from({length:25},(_,i) => ({id:'diagnostic-'+i,connection:'<img src=x onerror=alert(1)>',model:'mock-'+('long-model-'.repeat(25)),outcome:'upstream_failed',startedAt:'2026-10-10T08:00:00Z',httpStatus:503,upstreamRequestId:'safe-'+i,firstByteMs:20,durationMs:1200,bytesReceived:100,apiKey:'diagnostic-private-key',prompt:'diagnostic-private-prompt'}))}));
      assert.equal(await page.locator('#relay-diagnostics-list li').count(),20); assert.equal(await page.locator('#relay-diagnostics-list img').count(),0);
      assert.equal(await page.locator('#relay-diagnostics-summary').getAttribute('data-kind'),'warning');
      assert.doesNotMatch(await page.locator('#relay-diagnostics-dialog').innerText(),/diagnostic-private/);
      for(const width of [1073,751,390,320]) {
        await page.setViewportSize({width,height:884});
        const box=await page.locator('#relay-diagnostics-dialog').boundingBox(); assert(box.x>=0 && box.x+box.width<=width);
        assert.equal(await page.locator('#relay-diagnostics-dialog').evaluate(el => el.scrollWidth<=el.clientWidth+1),true);
      }
      await page.setViewportSize({width:1073,height:884}); await page.locator('#relay-diagnostics-dialog [data-close]').click();
      await page.evaluate(() => window.relayDiagnostics.render({enabled:false,recentRequests:[]}));
      await page.locator('#help-plugin-entry').click(); assert.equal(await page.locator('#plugin-page').isVisible(),true);
      await page.locator('#help-entry').click(); await page.locator('#help-current-connection').click();
      await tab('purpose'); assert.equal(await page.locator('#description').inputValue(),'modal preserved');
    });
    await page.locator('#discard-current').click(); await page.locator('#confirm-discard-current').click();
    await page.evaluate(() => document.querySelectorAll('[data-notice-key]').forEach(node => window.notices.clear(node.dataset.noticeKey)));
    await check('侧栏菜单优先靠右，操作不改变当前选择',async () => {
      await page.locator('[data-menu=review]').click(); const box=await page.locator('#model-menu').boundingBox(); assert(box.x>=198); assert.equal(await page.locator('#model-menu').getAttribute('data-placement'),'right');
      assert.equal(await page.locator('#model-menu-name').textContent(),'review');
      await page.locator('#model-menu').press('Escape'); assert.equal(await page.locator('#connection-title').innerText(),'batch-copy');
    });
    for(const width of [1366,1132,1073,993,913,751,700,390,320]) {
      await page.setViewportSize({width,height:884});
      for(const name of ['connection','model','purpose','advanced']) {
        await tab(name);
        // 窄屏页签按已确认原型横向滚动；只排除其被裁切的非当前按钮，仍严格验证当前页签与其余控件。
        const overflow=await page.evaluate(() => { const visible=[...document.querySelectorAll('main button, main input, main textarea, main select')].filter(el => el.getClientRects().length && !el.closest('[hidden]') && (!el.closest('#config-tabs') || el.getAttribute('aria-selected') === 'true')); return visible.filter(el => {const r=el.getBoundingClientRect();return r.left < -1 || r.right > innerWidth+1;}).map(el => el.id || el.className); });
        assert.deepEqual(overflow,[],`${width} ${name} controls outside viewport`);
        const tabBounds=await page.locator('#config-tabs').boundingBox(); assert(tabBounds.x>=0 && tabBounds.x+tabBounds.width<=width+1,`${width} tab container outside viewport`);
        await page.screenshot({animations:'disabled',path:path.join(output,`${width}-${name}.png`)});
        layouts++;
      }
      for(const name of ['overview','plugin','help']) {
        if(await page.locator('#sidebar-toggle').isVisible()) await page.locator('#sidebar-toggle').click();
        await page.locator('#'+name+'-entry').click();
        const overflow=await page.locator('#'+name+'-page').evaluate(el => [...el.querySelectorAll('button')].filter(button => {const r=button.getBoundingClientRect(); return r.width && (r.left<0 || r.right>innerWidth+1);}).map(el => el.id || el.className));
        assert.deepEqual(overflow,[],`${width} ${name} overflow`); await page.screenshot({animations:'disabled',path:path.join(output,`${width}-${name}.png`)}); layouts++;
      }
      await page.locator('#update-entry').click();
      const dialog=await page.locator('#update-dialog').boundingBox(); assert(dialog.x>=0 && dialog.y>=0 && dialog.x+dialog.width<=width+1 && dialog.y+dialog.height<=885);
      await page.screenshot({animations:'disabled',path:path.join(output,`${width}-update.png`)}); layouts++;
      await page.locator('#update-dialog [data-close]').click(); await manage('batch-copy');
    }
    groups.push(layouts+' 个响应式布局与截图');
    await check('320px 横向页签与键盘导航符合原型',async () => {
      await page.locator('#tab-model').press('Home'); assert.equal(await page.locator('#tab-connection').getAttribute('aria-selected'),'true');
      await page.locator('#tab-connection').press('End'); assert.equal(await page.locator('#tab-advanced').getAttribute('aria-selected'),'true');
      const box=await page.locator('#tab-advanced').boundingBox(), container=await page.locator('#config-tabs').boundingBox(); assert(box.x>=container.x-1 && box.x+box.width<=container.x+container.width+1);
      assert(await page.locator('#config-tabs').evaluate(el => el.scrollLeft>0));
    });
    await check('320px 抽屉、右侧菜单与移动排序',async () => {
      await page.locator('#sidebar-toggle').click(); await page.locator('[data-menu=review]').click(); const box=await page.locator('#model-menu').boundingBox(); assert(box.x>=8 && box.x+box.width<=312);
      await page.locator('#model-menu').press('Escape'); await page.locator('.model-select[data-name=review]').click(); await tab('model');
      const row=page.locator('.model-catalog-row').first(); await row.locator('[data-move-down]').click(); assert.equal(await page.locator('[data-move-down]:focus').count(),1);
      await page.screenshot({path:path.join(output,'320-mobile-sort.png')});
    });
    assert.deepEqual(errors,[]); assert.deepEqual(outside,[]);
    fs.writeFileSync(path.join(output,'browser-qa.json'),JSON.stringify({groups,layouts,scriptErrors:errors,outsideRequests:outside},null,2));
    console.log(`Verified ${groups.length} groups, ${layouts} layouts; no script errors or external requests.`);
  } finally { await browser.close(); server.close(); }
}
if (require.main === module) main().catch(error => { console.error(error); process.exitCode=1; });
module.exports = {mockBindings,assets};
