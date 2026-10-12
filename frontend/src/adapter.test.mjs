import test from 'node:test';
import assert from 'node:assert/strict';
import {decodeConfig, encodeConnection, mergeSaved, mergeCatalog, emptyStore, displaySnapshot} from './adapter.mjs';
import {clone, choosePlanModel, contextSettings, modelKey, reorderModels, makePlan, validatePlan, isDirty} from './domain.mjs';
import {fixtureConfig} from '../tests/fixtures.mjs';

test('正式首次启动无示例连接、范围或默认模型', () => {assert.deepEqual(emptyStore().connections, {}); assert.equal(emptyStore().active, null); assert.deepEqual(decodeConfig({models: {}}).plans, []);});
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
test('单连接保存不覆盖其他草稿或运行快照', () => {
  const config = fixtureConfig(), store = decodeConfig(config); store.connections.worker.description = 'unsaved worker'; store.plans[1].name = 'unsaved B'; store.active = displaySnapshot(config.lastAccess, store.savedConnections);
  config.models.cpa.description = 'saved cpa'; const next = mergeSaved(store, config, {connectionID: 'cpa'});
  assert.equal(next.connections.cpa.description, 'saved cpa'); assert.equal(next.connections.worker.description, 'unsaved worker'); assert.equal(next.plans[1].name, 'unsaved B'); assert.deepEqual(next.active, store.active); assert(isDirty(next.connections.worker, next.savedConnections.worker));
});
test('刷新目录未知容量保持未知且新增模型不会自动选中', () => {
  const c = decodeConfig(fixtureConfig()).connections.cpa, result = mergeCatalog(c, {models: [{id: 'new-unknown'}, {id: 'new-small', contextWindow: 64000}, {id: 'gpt-5-extra', contextWindow: 1000000}]});
  const unknown = result.models.find(m => m.id === 'new-unknown'), small = result.models.find(m => m.id === 'new-small'); assert.equal(unknown.context, null); assert.equal(unknown.contextEnabled, false); assert.equal(small.contextOverride, 64000); assert.equal(small.selected, false); assert.equal(result.defaultID, c.defaultID);
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
