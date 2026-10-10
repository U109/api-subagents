const test=require('node:test');
const assert=require('node:assert/strict');
const {createSnapshots,createProbe}=require('./config-operations.js');
const {effectiveContext,applyCatalogueContexts}=require('./config-operations.js');

test('正式目录容量低于上限不抬高，大于上限封顶，缺失保持待确认',()=>{
  assert.equal(effectiveContext(128000),128000);assert.equal(effectiveContext(1000000),256000);
  for(const value of [null,undefined,'256000',0,1.5,-1])assert.equal(effectiveContext(value),null);
  const profile={model:'small',relayModels:['large','unknown'],modelOfficialContexts:{removed:64000,unknown:80000}};
  const catalog={models:[{id:'small',contextWindow:128000},{id:'large',contextWindow:1000000},{id:'unknown'}]};
  assert.equal(applyCatalogueContexts(profile,catalog),true);
  assert.deepEqual({...profile.modelOfficialContexts},{small:128000,large:1000000});
  assert.equal(applyCatalogueContexts(profile,catalog),false);
});

test('恢复改名连接和未保存的新连接，不覆盖其他连接草稿或脱敏 Key 标记',()=>{
  const snapshots=createSnapshots();
  const base={version:1,models:{one:{savedName:'one',hasKey:true,apiKey:'',relayModels:['a']},two:{savedName:'two',description:'saved'}}};
  snapshots.remember(base);
  const current={...base,models:{renamed:{...base.models.one,relayModels:['b']},two:{...base.models.two,description:'draft'}}};
  const restored=snapshots.restore(current,'renamed');
  assert.equal(restored.selected,'one');
  assert.deepEqual(restored.config.models.one.relayModels,['a']);
  assert.equal(restored.config.models.one.apiKey,'');
  assert.equal(restored.config.models.one.hasKey,true);
  assert.equal(restored.config.models.two.description,'draft');
  assert.ok(current.models.renamed);
  restored.config.models.new={model:'c'};
  assert.ok(!snapshots.restore(restored.config,'new').config.models.new);
});

test('恢复名称冲突不会部分修改配置，定点复制删除不污染其他快照',()=>{
  const snapshots=createSnapshots();
  const config={models:{original:{savedName:'original',model:'a'}}};
  snapshots.remember(config);
  snapshots.rememberOne('copied',{savedName:'copied',model:'b'});
  const conflicting={models:{renamed:{savedName:'original'},original:{model:'draft'}}};
  assert.throws(()=>snapshots.restore(conflicting,'renamed'),/占用/);
  assert.ok(conflicting.models.renamed);
  snapshots.forget('original');
  assert.throws(()=>snapshots.restore(config,'original'),/没有可恢复/);
  assert.equal(snapshots.restore({models:{copied:{savedName:'copied',model:'changed'}}},'copied').config.models.copied.model,'b');
});

test('真实请求适配器传递固定路由和取消信号，成功后释放活动状态',async()=>{
  const states=[];let seen;
  const operation=createProbe({api:async(...args)=>{seen=args;},onState:(...value)=>states.push(value),idFactory:()=> 'test-request-123'});
  assert.equal(await operation.run({models:{}},'one'),true);
  assert.equal(seen[0],'/api/probe');
  assert.equal(seen[1].requestId,'test-request-123');
  assert.ok(seen[2].signal instanceof AbortSignal);
  assert.equal(states.at(-1)[0],'success');
  assert.equal(operation.isRunning(),false);
});

test('桌面取消按请求号执行，迟到成功不会覆盖取消，重复测试被拒绝',async()=>{
  let finish;const states=[],cancelled=[];
  const operation=createProbe({api:()=>new Promise(resolve=>{finish=resolve;}),cancelDesktop:async id=>cancelled.push(id),onState:(...s)=>states.push(s),idFactory:()=> 'desktop-test-123'});
  const running=operation.run({},'one');
  assert.equal(await operation.run({},'two'),false);
  await operation.cancel();finish();
  assert.equal(await running,false);
  assert.deepEqual(cancelled,['desktop-test-123']);
  assert.equal(states.at(-1)[0],'cancelled');
  assert.equal(operation.isRunning(),false);
});

test('HTTP 取消和鉴权失败可区分，失败后允许重新测试',async()=>{
  const states=[];
  const operation=createProbe({api:(_route,_body,{signal})=>new Promise((resolve,reject)=>signal.addEventListener('abort',()=>reject(new Error('aborted')))),onState:(...s)=>states.push(s)});
  const running=operation.run({},'one');await operation.cancel();await running;
  assert.equal(states.at(-1)[0],'cancelled');
  const failures=[];
  const failing=createProbe({api:async()=>{throw new Error('HTTP 401');},onState:(...s)=>failures.push(s)});
  assert.equal(await failing.run({},'one'),false);
  assert.deepEqual(failures.at(-1),['error','HTTP 401']);
  assert.equal(failing.isRunning(),false);
});
