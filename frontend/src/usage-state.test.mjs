import test from 'node:test';
import assert from 'node:assert/strict';
import {createUsageGate, priceForm, editPrice, parsePrices} from './usage-state.mjs';
import {configuredPriceModels} from './usage-domain.mjs';

test('只接受最新用量读取，离开页面后早期成功与失败均不能改变视图', () => {
  const gate = createUsageGate(), old = gate.beginRead(), latest = gate.beginRead();
  assert.equal(gate.acceptsRead(old), false); assert.equal(gate.acceptsRead(latest), true);
  gate.invalidate(); assert.equal(gate.acceptsRead(latest), false);
});

test('价格表只列保存连接的已选模型，不展示目录未选和历史项', () => {
  const models = configuredPriceModels({c: {id: 'c', name: '连接', models: [{id: 'selected', selected: true, fast: true}, {id: 'catalog-only', selected: false}]}});
  assert.equal(models.length, 1); assert.equal(models[0].id, '["c","selected"]'); assert.equal(models[0].fast, true);
});
test('Fast 价格单独编辑并保留普通、长上下文和隐藏项，高精度小数不舍入', () => {
  const rates = {a: {input: 2.5, output: 15, read: .25, write: 2.5, source: 'official', fast: {input: 5, output: 30, read: .5, write: 5}, longContext: {above: 272000, rate: {input: 5, output: 22.5, read: .5, write: 5}}}, hidden: {input: 1, output: 2, read: 0, write: 0}};
  const draft = editPrice(null, rates, [{id: 'a'}], 'revision', 'a', 'read', '.000125', 'fast');
  const parsed = parsePrices(draft.rates);
  assert.equal(parsed.a.read, .25); assert.equal(parsed.a.fast.read, .000125); assert.equal(parsed.a.source, '');
  assert.deepEqual(parsed.a.longContext, rates.a.longContext); assert.deepEqual(parsed.hidden, rates.hidden); assert.equal(rates.a.fast.read, .5);
});
test('缺失 Fast 字段或非有限单价报错，全空 Fast 和全空普通覆盖按组删除', () => {
  const normal = {input: 1, output: 2, read: 0, write: 0};
  assert.throws(() => parsePrices({a: {...normal, fast: {input: 3}}}), /Fast/);
  assert.throws(() => parsePrices({a: {...normal, output: Infinity}}), /完整填写/);
  assert.deepEqual(parsePrices({a: {...normal, fast: {input: '', output: '', read: '', write: ''}}, b: {input: '', output: '', read: '', write: ''}}), {a: normal});
});
test('费率保存使保存前和保存期间的读取失效，成功失败后都可重新刷新', () => {
  const gate = createUsageGate(), before = gate.beginRead(); gate.beginWrite();
  const during = gate.beginRead(); assert.equal(gate.acceptsPrices(before), false); assert.equal(gate.acceptsPrices(during), false);
  assert.throws(() => gate.beginWrite(), /正在保存/); gate.endWrite();
  assert.equal(gate.acceptsPrices(during), false); assert.equal(gate.acceptsPrices(gate.beginRead()), true);
});
test('刷新修订和新增模型保留未保存单价，提交仍检查原修订避免覆盖其他窗口', () => {
  const draft = editPrice(null, {a: {input: 1, output: 2, read: 0, write: 0}}, [{id: 'a'}], 'revision-1', 'a', 'input', '3.5');
  const next = priceForm(draft, {a: {input: 9, output: 2, read: 0, write: 0}}, [{id: 'a'}, {id: 'b'}], 'revision-2');
  assert.equal(next.rates.a.input, '3.5'); assert.equal(next.revision, 'revision-1'); assert.equal(next.rates.b.input, '');
  assert.equal(priceForm(null, {a: {input: 9}}, [{id: 'a'}], 'revision-2').rates.a.input, 9);
});
test('保存当前可见模型的费率不会删除已保存但不在目录中的其他条目', () => {
  const rates = {hidden: {input: 7, output: 8, read: 1, write: 2}};
  const next = editPrice(null, rates, [{id: 'visible'}], 'revision', 'visible', 'input', '2');
  assert.deepEqual(next.rates.hidden, rates.hidden); assert.equal(next.rates.visible.input, '2');
  assert.deepEqual(rates, {hidden: {input: 7, output: 8, read: 1, write: 2}});
});
