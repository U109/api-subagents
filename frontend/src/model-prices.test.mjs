import test from 'node:test';
import assert from 'node:assert/strict';
import {groupModelPrices, editModelPrice, unifyModelPrice, modelPricePayload} from './model-prices.mjs';
import {createUsageGate} from './usage-state.mjs';

const models = [{id: '["a","m"]', model: 'm', connection: 'A', fast: true}, {id: '["b","m"]', model: 'm', connection: 'B'}, {id: '["b","M"]', model: 'M', connection: 'B'}];
const rate = {input: 1, output: 2, read: 0, write: 0};
const tiered = {...rate, fast: {...rate, input: 2}, longContext: {above: 200000, rate: {...rate, output: 4}, fast: {...rate, input: 4}}};

test('精确模型 ID 归并，保留大小写、Fast 和全部分层单价', () => {
  const rates = {[models[0].id]: tiered, [models[1].id]: structuredClone(tiered)};
  const groups = groupModelPrices(models, rates);
  assert.equal(groups.length, 2); assert.equal(groups[0].label, 'm'); assert.equal(groups[0].fast, true);
  assert.equal(groups[0].conflict, false); assert.deepEqual(groups[0].price.longContext.fast, tiered.longContext.fast);
  assert.equal(groups[1].label, 'M');
});
test('普通、Fast、长上下文与缺失价差异须显式解决，不能任意取第一项保存', () => {
  for (const other of [rate, {...tiered, fast: {...rate, input: 9}}, {...tiered, longContext: {...tiered.longContext, above: 300000}}, undefined]) {
    const rates = {[models[0].id]: tiered, [models[1].id]: other};
    assert.equal(groupModelPrices(models, rates)[0].conflict, true);
    assert.throws(() => modelPricePayload(rates, models), /连接费率不同/);
  }
});
test('统一完整费率后展开至连接键，保留隐藏项、官网来源、开始修订和历史价格', () => {
  const rates = {[models[0].id]: {...tiered, source: 'official', checkedAt: '2026-10-14'}, [models[1].id]: rate, hidden: rate};
  const draft = unifyModelPrice(null, rates, models, 'r1', 'm', models[0].id);
  const next = editModelPrice(draft, rates, models, 'r2', 'm', 'input', '3.25', 'fast');
  const payload = modelPricePayload(next.rates, models);
  assert.equal(next.revision, 'r1'); assert.equal(next.dirty, true);
  for (const model of models.slice(0, 2)) {assert.equal(payload[model.id].fast.input, 3.25); assert.deepEqual(payload[model.id].longContext, tiered.longContext);}
  assert.deepEqual(payload.hidden, rate); assert.equal(rates[models[0].id].fast.input, 2);
  assert.equal(draft.rates[models[1].id].source, 'official');
});
test('清除隔离旧读取和跨页重复清除，不影响费率写入代次', () => {
  const gate = createUsageGate(), old = gate.beginRead();
  gate.beginWrite(); gate.beginClear();
  const during = gate.beginRead();
  assert.equal(gate.acceptsRead(old), false); assert.equal(gate.acceptsRead(during), false);
  assert.throws(() => gate.beginClear(), /正在清除/);
  gate.endClear(); assert.equal(gate.acceptsRead(during), false);
  assert.equal(gate.acceptsPrices(during), false); gate.endWrite();
  const current = gate.beginRead(); assert.equal(gate.acceptsRead(current), true); assert.equal(gate.acceptsPrices(current), true);
});
