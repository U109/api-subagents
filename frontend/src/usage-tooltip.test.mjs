import test from 'node:test';
import assert from 'node:assert/strict';
import {placeUsageTooltip} from './usage-tooltip.mjs';

test('趋势提示默认位于鼠标右下方，同一时段内移动仍跟随指针', () => {
  const bounds = {width: 600, height: 376}, size = {width: 230, height: 100};
  assert.deepEqual(placeUsageTooltip({x: 100, y: 80}, bounds, size), {left: 114, top: 94});
  assert.deepEqual(placeUsageTooltip({x: 107, y: 86}, bounds, size), {left: 121, top: 100});
});

test('趋势提示接近右下边缘时翻到左上方，实际内容变大后重新避让', () => {
  const anchor = {x: 580, y: 350}, bounds = {width: 600, height: 376};
  assert.deepEqual(placeUsageTooltip(anchor, bounds, {width: 230, height: 100}), {left: 336, top: 236});
  assert.deepEqual(placeUsageTooltip(anchor, bounds, {width: 280, height: 160}), {left: 286, top: 176});
});

test('窄屏和键盘锚点的趋势提示保留内边距，不产生负坐标或越过图表边界', () => {
  for (const bounds of [{width: 600, height: 376}, {width: 320, height: 288}]) {
    const size = {width: 230, height: 120};
    for (const anchor of [{x: 0, y: 0}, {x: bounds.width / 2, y: bounds.height / 2}, {x: bounds.width, y: bounds.height}]) {
      const result = placeUsageTooltip(anchor, bounds, size);
      assert.ok(result.left >= 12 && result.top >= 12);
      assert.ok(result.left + size.width <= bounds.width - 12);
      assert.ok(result.top + size.height <= bounds.height - 12);
    }
  }
});

test('极小容器仍返回有限非负定位，超大内容由组件样式约束而非负坐标掩盖', () => {
  assert.deepEqual(placeUsageTooltip({x: 5, y: 5}, {width: 20, height: 20}, {width: 230, height: 120}), {left: 12, top: 12});
});
