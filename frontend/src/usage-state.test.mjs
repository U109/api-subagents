import test from 'node:test';
import assert from 'node:assert/strict';
import {createUsageGate, priceForm, editPrice} from './usage-state.mjs';

test('只接受最新用量读取，离开页面后早期成功与失败均不能改变视图', () => {
  const gate = createUsageGate(), old = gate.beginRead(), latest = gate.beginRead();
  assert.equal(gate.acceptsRead(old), false); assert.equal(gate.acceptsRead(latest), true);
  gate.invalidate(); assert.equal(gate.acceptsRead(latest), false);
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
