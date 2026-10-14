import test from 'node:test';
import assert from 'node:assert/strict';
import {createUsageView, normalizeUsage, usageSummary, usageBreakdown, usageVisibleBreakdown, usageCost, usageModels, usageSeriesModels, selectUsageRows, usageKey} from './usage-domain.mjs';

const now = Date.now();
const base = {id: 'attempt-a', logicalId: 'request-a', attempt: 1, startedAt: now-1000, connectionId: 'conn-a', connection: '连接 A', model: 'model-a', source: 'main', outcome: 'success', quality: 'complete', durationMS: 1000, input: 100, output: 20, total: 120, read: 10, write: 5, reasoning: 4, costMicroUSD: 300};

test('实际尝试按逻辑请求合并且保留未知和历史计价', () => {
  const rows = normalizeUsage([{...base, id:'failed-attempt', outcome:'failed', input:null, output:null, total:null, costMicroUSD:null}, {...base, id:'success-attempt', attempt:2}]);
  assert.equal(rows.length,1); assert.equal(rows[0].attempts,2); assert.equal(rows[0].total,120); assert.equal(rows[0].result,'success');
  assert.equal(usageCost(rows[0], {}), .0003);
});

test('没有价格或全部用量缺失不等于零费用', () => {
  const rows = normalizeUsage([{...base, input:null, output:null, total:null, costMicroUSD:null}]);
  assert.equal(usageSummary(rows).cost,null); assert.equal(usageSummary(rows).total,null);
});

test('四项真实筛选和测试来源隔离，不残留隐形条件', () => {
  const rows = normalizeUsage([base,{...base,id:'test',logicalId:'test',source:'test'}]), view = createUsageView();
  assert.equal(selectUsageRows(rows,view,now).length,1);
  view.filters.source='test'; assert.equal(selectUsageRows(rows,view,now)[0].source,'test');
});

test('完整拆分互斥；缺少细分字段显示未知而不是补零', () => {
  const rows = normalizeUsage([base]), parts=usageBreakdown(rows);
  assert.equal(parts.items.reduce((sum,item)=>sum+item.value,0),120);
  assert.equal(usageBreakdown(normalizeUsage([{...base,read:null}])).items[0].value,null);
});

test('模型来自保存连接与历史，而不是模拟目录', () => {
  const rows=normalizeUsage([base]), models=usageModels(rows,{});
  assert.equal(models.length,1); assert.equal(models[0].id,usageKey('conn-a','model-a'));
  assert.equal(usageModels([],{}).length,0);
});

test('零值与未上报拆分只隐藏展示，不改变已知合计或缓存与推理扣减', () => {
  const rows = normalizeUsage([{...base, read: 0, write: 0, reasoning: 0}]);
  const visible = usageVisibleBreakdown(rows), full = usageBreakdown(rows);
  assert.deepEqual(visible.items.map(item => item.id), ['input', 'output']);
  assert.equal(visible.total, full.total);
  assert.equal(visible.known, full.known);
  assert.equal(visible.items.reduce((sum, item) => sum + item.value, 0), 120);
  assert.equal(visible.items.reduce((sum, item) => sum + item.percent, 0), 1);
  assert.deepEqual(usageVisibleBreakdown([]).items, []);
  assert.deepEqual(usageVisibleBreakdown(normalizeUsage([{...base, read: null}])).items, []);
  assert.equal(usageVisibleBreakdown(normalizeUsage([{...base, read: null}])).total, null);
});

test('图例仅保留当前筛选有记录的模型，未知用量仍保留，目录顺序与配色不变', () => {
  const rows = normalizeUsage([base, {...base, id: 'unknown', logicalId: 'unknown', model: 'unknown-model', input: null, output: null, total: null}]);
  const models = usageModels(rows, {'conn-a': {id: 'conn-a', name: '连接 A', models: [{id: 'unused'}]}});
  const original = structuredClone(models);
  assert.deepEqual(usageSeriesModels([], models), []);
  assert.deepEqual(usageSeriesModels(rows.filter(row => row.model === 'unknown-model'), models), models.filter(model => model.id === usageKey('conn-a', 'unknown-model')));
  assert.equal(usageSeriesModels(rows, models).length, 2);
  assert.deepEqual(models, original);
});

test('有用量的趋势系列优先使用蓝绿黄，未使用模型与连接测试不抢占配色', () => {
  const connections = {
    'test-conn': {id: 'test-conn', name: '测试连接', models: [{id: 'test-model'}]},
    'conn-a': {id: 'conn-a', name: '连接 A', models: [{id: 'unused'}, {id: 'model-a'}]},
    'conn-b': {id: 'conn-b', name: '连接 B', models: [{id: 'model-b'}]},
  };
  const original = structuredClone(connections);
  const rows = normalizeUsage([
    base,
    {...base, id: 'second', logicalId: 'second', connectionId: 'conn-b', connection: '连接 B', model: 'model-b'},
    {...base, id: 'third', logicalId: 'third', connectionId: 'history', connection: '历史连接', model: 'model-c'},
    {...base, id: 'test', logicalId: 'test', connectionId: 'test-conn', model: 'test-model', source: 'test'},
  ]);
  const models = usageModels(rows, connections);
  assert.deepEqual(models.slice(0, 3).map(model => model.id), [usageKey('conn-a', 'model-a'), usageKey('conn-b', 'model-b'), usageKey('history', 'model-c')]);
  assert.deepEqual(models.slice(0, 3).map(model => model.color), ['#437ef7', '#35b58c', '#d4a017']);
  assert.equal(models.length, 5);
  assert.deepEqual(connections, original, 'presentation ordering must not reorder saved connection models');
  assert.deepEqual(usageModels([...rows].reverse(), connections), models, 'record order must not change series colors');
});
