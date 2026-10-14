import test from 'node:test';
import assert from 'node:assert/strict';
import {decodeConfig, encodeConnection, mergeSaved, mergeCatalog, sameCatalogSource, emptyStore, displaySnapshot} from './adapter.mjs';
import {clone, choosePlanModel, contextSettings, modelKey, reorderModels, reorderPlanModels, planModels, makePlan, validatePlan, isDirty} from './domain.mjs';
import {fixtureConfig} from '../tests/fixtures.mjs';

test('正式首次启动无示例连接、范围或默认模型', () => {assert.deepEqual(emptyStore().connections, {}); assert.equal(emptyStore().active, null); assert.deepEqual(decodeConfig({models: {}}).plans, []);});
test('模型 Fast 仅按显式设置往返，名称不隐式启用，未选模型不写入连接且旧 Ultra 迁移到 Max', () => {
  const config = fixtureConfig(); config.models.cpa.modelFastModes = {[config.models.cpa.model]: true}; config.models.cpa.reasoningEffort = 'ultra';
  const connection = decodeConfig(config).connections.cpa;
  assert.equal(connection.effort, 'max'); assert.equal(connection.models.find(model => model.id === connection.defaultID).fast, true);
  const other = connection.models.find(model => model.id !== connection.defaultID); other.fast = true; other.selected = false;
  const encoded = encodeConnection(connection); assert.equal(encoded.modelFastModes[connection.defaultID], true); assert.equal(Object.hasOwn(encoded.modelFastModes, other.id), false);
  delete config.models.cpa.modelFastModes;
  assert.ok(decodeConfig(config).connections.cpa.models.every(model => model.fast === false));
});
test('脱敏连接往返保留协议、原生容量、模型顺序、环境 Key 及独立范围', () => {
  const config = fixtureConfig(), store = decodeConfig(config), c = store.connections.cpa;
  assert.equal(c.models.length, 8); assert.equal(c.models.filter(m => m.selected).length, 6); assert.equal(c.models[0].contextOverride, 256000); assert.equal(c.models[2].contextOverride, 128000);
  c.apiKeyEnv = 'SYNTHETIC_TEST_KEY'; c.hasKey = true;
  const p = encodeConnection(c); assert.equal(p.apiKeyEnv, 'SYNTHETIC_TEST_KEY'); assert.equal(p.apiKey, ''); assert.equal(p.protocol, 'responses'); assert.equal(p.modelCatalog.length, 8);
  c.apiKey = 'synthetic-input'; assert.equal(encodeConnection(c).apiKeyEnv, '');
});
test('配置模型选择和默认项只作用于当前配置', () => {
  const store = decodeConfig(fixtureConfig()), original = clone(store.connections), a = store.plans[0];
  const b = choosePlanModel(a, 'cpa', 'gpt-5.1-fast-high', false); assert.equal(b.modelIDs.cpa.length, 5); assert.deepEqual(store.connections, original); assert.equal(a.modelIDs.cpa.length, 6);
  const noDefault = choosePlanModel(a, 'cpa', 'gpt-5-extra', false); assert.equal(noDefault.defaultKey, ''); assert.match(validatePlan(noDefault, store.savedConnections), /星标/);
});
test('配置模型排序独立保存并恢复，不改默认、连接目录或其他配置', () => {
  const config = fixtureConfig(), store = decodeConfig(config), original = JSON.stringify(store), plan = store.plans[0];
  const next = reorderPlanModels(plan, 'cpa', 'gpt-5-extra', 'deepseek-v4-flash');
  assert.deepEqual(next.modelIDs.cpa.slice(0, 3), ['gpt-5.1-fast-high', 'deepseek-v4-flash', 'gpt-5-extra']);
  assert.equal(next.defaultKey, plan.defaultKey); assert.equal(JSON.stringify(store), original); assert.equal(Object.getPrototypeOf(store.savedConnections), null);
  assert.deepEqual(next.modelIDs.worker, plan.modelIDs.worker);
  const restored = decodeConfig({...config, accessConfigs: [next, store.plans[1]]});
  assert.deepEqual(restored.plans[0].modelIDs.cpa, next.modelIDs.cpa);
  assert.deepEqual(planModels(next, store.savedConnections).filter(model => model.connectionID === 'cpa').map(model => model.modelID), next.modelIDs.cpa);
});
test('配置过滤排序保留隐藏项槽位，拒绝未选、重复和跨连接落点', () => {
  const store = decodeConfig(fixtureConfig()), plan = makePlan('test', '独立配置', ['cpa', 'worker'], store.savedConnections, {cpa: ['gpt-5-extra', 'deepseek-v4-flash', 'gpt-5.1-fast-high', 'gpt-4'], worker: ['gpt-5-mini']});
  const visible = ['gpt-5-extra', 'gpt-5.1-fast-high', 'gpt-4'];
  const next = reorderPlanModels(plan, 'cpa', 'gpt-5-extra', 'gpt-4', visible);
  assert.deepEqual(next.modelIDs.cpa, ['gpt-5.1-fast-high', 'deepseek-v4-flash', 'gpt-4', 'gpt-5-extra']);
  assert.equal(reorderPlanModels(plan, 'cpa', 'gpt-5-extra', 'not-selected'), plan);
  assert.equal(reorderPlanModels(plan, 'cpa', 'gpt-5-extra', 'gpt-5-mini'), plan);
  assert.equal(reorderPlanModels(plan, 'missing', 'gpt-5-extra', 'gpt-4'), plan);
  assert.equal(reorderPlanModels(plan, 'cpa', 'gpt-5-extra', 'gpt-4', ['gpt-5-extra', 'gpt-5-extra', 'gpt-4']), plan);
});
test('单连接保存不覆盖其他草稿或运行快照', () => {
  const config = fixtureConfig(), store = decodeConfig(config); store.connections.worker.description = 'unsaved worker'; store.plans[1].name = 'unsaved B'; store.active = displaySnapshot(config.lastAccess, store.savedConnections);
  config.models.cpa.description = 'saved cpa'; const next = mergeSaved(store, config, {connectionID: 'cpa'});
  assert.equal(next.connections.cpa.description, 'saved cpa'); assert.equal(next.connections.worker.description, 'unsaved worker'); assert.equal(next.plans[1].name, 'unsaved B'); assert.deepEqual(next.active, store.active); assert(isDirty(next.connections.worker, next.savedConnections.worker));
});
test('保存连接只提交请求时的内容，保留同一连接在等待期间的新修改并清除已提交 Key', () => {
  const config = fixtureConfig(), store = decodeConfig(config);
  store.connections.cpa.apiKey = 'synthetic-submitted-key';
  const submittedConnection = clone(store.connections.cpa);
  store.connections.cpa.description = '保存期间继续输入';
  const next = mergeSaved(store, config, {connectionID: 'cpa', submittedConnection});
  assert.equal(next.connections.cpa.description, '保存期间继续输入');
  assert.equal(next.connections.cpa.apiKey, '');
  assert.equal(next.savedConnections.cpa.description, config.models.cpa.description || '');
  assert(isDirty(next.connections.cpa, next.savedConnections.cpa));
});
test('等待期间输入的新 Key 不被旧响应清除，改名后下一次保存使用最新凭据引用', () => {
  const config = fixtureConfig(), store = decodeConfig(config), submittedConnection = clone(store.connections.cpa);
  submittedConnection.name = '已提交名称'; submittedConnection.apiKey = 'synthetic-old-key';
  store.connections.cpa = {...submittedConnection, name: '继续修改名称', apiKey: 'synthetic-new-key'};
  config.models['已提交名称'] = {...config.models.cpa, apiKeyEnv: '', hasKey: true}; delete config.models.cpa;
  const next = mergeSaved(store, config, {connectionID: 'cpa', submittedConnection});
  assert.equal(next.connections.cpa.name, '继续修改名称');
  assert.equal(next.connections.cpa.savedName, '已提交名称');
  assert.equal(next.connections.cpa.apiKey, 'synthetic-new-key');
  assert.equal(encodeConnection(next.connections.cpa).savedName, '已提交名称');
  assert.equal(next.savedConnections.cpa.apiKey, '');
});
test('复制连接首次保存后解除来源引用，不复活已被放弃的连接草稿', () => {
  const config = fixtureConfig(), store = decodeConfig(config);
  const submittedConnection = {...clone(store.connections.cpa), copySource: 'source-id'};
  store.connections.cpa = {...submittedConnection, description: '新草稿'};
  const next = mergeSaved(store, config, {connectionID: 'cpa', submittedConnection});
  assert.equal(next.connections.cpa.copySource, undefined);
  delete store.connections.cpa;
  assert.equal(mergeSaved(store, config, {connectionID: 'cpa', submittedConnection}).connections.cpa, undefined);
});
test('配置保存保留等待期间的新范围和默认项，保存副本仍对应已提交的配置', () => {
  const config = fixtureConfig(), store = decodeConfig(config), submittedPlan = clone(store.plans[0]);
  store.plans[0].name = '稍后编辑的名称'; store.plans[0].modelIDs.cpa.reverse();
  const next = mergeSaved(store, config, {planID: submittedPlan.id, submittedPlan});
  assert.equal(next.plans[0].name, '稍后编辑的名称');
  assert.deepEqual(next.plans[0].modelIDs.cpa, store.plans[0].modelIDs.cpa);
  assert.deepEqual(next.savedPlans[submittedPlan.id], submittedPlan);
  assert(isDirty(next.plans[0], next.savedPlans[submittedPlan.id]));
});
test('刷新目录未知容量保持未知且新增模型不会自动选中', () => {
  const c = decodeConfig(fixtureConfig()).connections.cpa, result = mergeCatalog(c, {models: [{id: 'new-unknown'}, {id: 'new-small', contextWindow: 64000}, {id: 'gpt-5-extra', contextWindow: 1000000}]});
  const unknown = result.models.find(m => m.id === 'new-unknown'), small = result.models.find(m => m.id === 'new-small'); assert.equal(unknown.context, null); assert.equal(unknown.contextEnabled, false); assert.equal(small.contextOverride, 64000); assert.equal(small.selected, false); assert.equal(result.defaultID, c.defaultID);
});
test('目录查询过程中本地模型选择可继续编辑，凭据来源改变时拒绝旧结果', () => {
  const submitted = decodeConfig(fixtureConfig()).connections.cpa, current = clone(submitted);
  current.models[0].selected = false; current.description = '新描述';
  assert.equal(sameCatalogSource(current, submitted), true);
  for (const field of ['url', 'protocol', 'apiKey', 'apiKeyEnv', 'savedName', 'copySource']) assert.equal(sameCatalogSource({...current, [field]: 'changed'}, submitted), false);
  assert.equal(sameCatalogSource(undefined, submitted), false);
});
test('上下文关闭不会发送覆盖值且保留原生容量', () => {
  const c = decodeConfig(fixtureConfig()).connections.cpa; c.models[0].contextEnabled = false; const p = encodeConnection(c);
  assert.equal(p.modelContextOverrides['gpt-5-extra'], false); assert.equal(p.modelContextWindows['gpt-5-extra'], undefined); assert.equal(p.modelOfficialContexts['gpt-5-extra'], 1000000); assert.equal(contextSettings(c.models[0]).contextOverride, null);
});
test('过滤排序仅改变可见槽位，隐藏模型不动', () => {
  const models = ['a', 'hidden', 'b', 'c'].map(id => ({id})); assert.deepEqual(reorderModels(models, 'c', 'a', ['a', 'b', 'c']).map(m => m.id), ['c', 'hidden', 'a', 'b']); assert.equal(reorderModels(models, 'a', 'invalid', ['a', 'b']), models);
});
test('删除连接清理所有配置，运行目标消失时不自动切换', () => {
  const config = fixtureConfig(), store = decodeConfig(config); store.active = displaySnapshot(config.lastAccess, store.savedConnections); delete config.models.cpa;
  const next = mergeSaved(store, config, {removeConnectionID: 'cpa'}); assert.equal(next.active, null); assert.equal(next.enabled, false); assert(next.plans.every(p => !p.connectionIDs.includes('cpa'))); assert.equal(next.plans[0].defaultKey, '');
});
test('名称含特殊字符时模型键不碰撞，失效引用不被解释为全部模型', () => {
  assert.notEqual(modelKey('a/b', 'c'), modelKey('a', 'b/c')); const store = decodeConfig(fixtureConfig()); const p = makePlan('id', '测试', ['cpa'], store.savedConnections, {cpa: ['missing']}); assert.match(validatePlan(p, store.savedConnections), /不可用|可用范围/);
});
test('合法特殊模型 ID 不读取原型链或丢失映射值', () => {
  const store = decodeConfig({models: {special: {id: 'special', protocol: 'responses', model: 'constructor', relayModels: ['__proto__'], baseUrl: 'http://127.0.0.1:9/v1'}}});
  const c = store.connections.special; assert.equal(c.models[0].name, ''); assert.equal(c.models[0].context, null); assert.equal(c.models[0].contextEnabled, false);
  c.models[1].name = '特殊模型'; c.models[1].context = 64000; c.models[1].contextEnabled = true; c.models[1].contextOverride = 64000;
  const p = encodeConnection(c); assert.equal(p.modelNames['__proto__'], '特殊模型'); assert.equal(p.modelContextWindows['__proto__'], 64000); assert.equal(JSON.parse(JSON.stringify(p)).modelNames['__proto__'], '特殊模型');
});
