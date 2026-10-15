import {priceForm, editPrice, parsePrices} from './usage-state.mjs';

const fields = ['input', 'output', 'read', 'write'];

/** 将空白和数值字符串规范为可比较费率，不以缺失的 Fast 价冒充普通价或免费。 */
function rateIdentity(rate) {
  return fields.map(field => rate?.[field] == null || String(rate[field]).trim() === '' ? null : Number(rate[field]));
}

/** 比较完整计价档位及阈值，来源说明不属于单价冲突；未知和显式零保持不同。 */
function priceIdentity(price) {
  const long = price?.longContext;
  return JSON.stringify([rateIdentity(price), rateIdentity(price?.fast), long ? [long.above, rateIdentity(long.rate), rateIdentity(long.fast)] : null]);
}

/** 按准确模型 ID 展示一行，保留连接保存键及完整 Fast/长上下文价格；有差异必须显式统一。 */
export function groupModelPrices(models, rates = {}) {
  const groups = new Map();
  for (const model of models) {
    if (!groups.has(model.model)) groups.set(model.model, {id: model.model, model: model.model, label: model.model, ids: [], members: [], fast: false});
    const group = groups.get(model.model);
    if (!group.ids.includes(model.id)) {group.ids.push(model.id); group.members.push(model);}
    group.fast ||= model.fast === true;
  }
  return [...groups.values()].map(group => ({...group, price: rates[group.ids[0]] || {}, conflict: new Set(group.ids.map(id => priceIdentity(rates[id]))).size > 1}));
}

/** 模型级编辑同步该字段至所有已配置连接，其他档位、历史费率和最初修订保持不变。 */
export function editModelPrice(draft, rates, models, revision, modelID, field, value, tier = 'standard') {
  if (!fields.includes(field) || !['standard', 'fast', 'long', 'long-fast'].includes(tier)) throw new Error('不支持的费率字段');
  let next = priceForm(draft, rates, models, revision);
  for (const model of models) if (model.model === modelID) next = editPrice(next, rates, models, revision, model.id, field, value, tier);
  return next;
}

/** 用户明确选择一套完整费率后统一所有连接，连同 Fast、上下文阈值及来源一起复制，不动隐藏模型。 */
export function unifyModelPrice(draft, rates, models, revision, modelID, sourceID) {
  const next = priceForm(draft, rates, models, revision);
  const members = models.filter(model => model.model === modelID);
  if (!members.some(model => model.id === sourceID)) throw new Error('所选价格不属于当前模型');
  for (const model of members) next.rates[model.id] = structuredClone(next.rates[sourceID]);
  return {...next, dirty: true};
}

/** 继续使用原有连接元组 API 和完整分层校验；拒绝未解决的冲突，保留目录外费率及历史快照。 */
export function modelPricePayload(rates, models) {
  const conflict = groupModelPrices(models, rates).find(group => group.conflict);
  if (conflict) throw new Error(`模型 ${conflict.model} 的连接费率不同，请先选择统一费率。`);
  return parsePrices(rates);
}
