/** 建立用量读取身份；只有最新读取可更新记录，保存开始前或保存期间的读取不能覆盖费率。 */
export function createUsageGate() {
  let read = 0, prices = 0, writing = false;
  return {
    /** 分配读取身份，记录发起时的费率代次。 */
    beginRead() {return {read: ++read, prices};},
    /** 拒绝更早的读取及组件离开后的回调。 */
    acceptsRead(ticket) {return ticket.read === read;},
    /** 费率读结果还必须没有跨越写入边界，避免读到保存前的旧修订。 */
    acceptsPrices(ticket) {return ticket.prices === prices && !writing;},
    /** 串行保存费率，跨页签重复点击不会创建第二次写入。 */
    beginWrite() {if (writing) throw new Error('正在保存费率，请稍候。'); writing = true; prices++;},
    /** 完成或失败都使写入期间的费率读取失效，下一次刷新再读取权威修订。 */
    endWrite() {writing = false; prices++;},
    /** 离开页面撤销全部读取身份，不把迟到回调应用到重新进入的页面。 */
    invalidate() {read++; prices++;}
  };
}

/** 从权威费率构造表单，保留当前目录未展示的已保存条目；空行表示尚未定价。 */
export function priceForm(draft, rates, models, revision) {
  const values = structuredClone(draft?.dirty ? draft.rates : rates);
  for (const model of models) if (!Object.hasOwn(values, model.id)) values[model.id] = {input: '', output: '', read: '', write: ''};
  return {rates: values, revision: draft?.dirty ? draft.revision : revision, dirty: Boolean(draft?.dirty)};
}

/** 修改一项费率并保留开始编辑时的修订；刷新或新增模型不能悄悄覆盖草稿或接受外部修改。 */
export function editPrice(draft, rates, models, revision, id, field, value, tier = 'standard') {
  const next = priceForm(draft, rates, models, revision);
  const price = {...next.rates[id], source: '', checkedAt: ''};
  if (tier === 'standard') price[field] = value;
  else if (tier === 'fast') price.fast = {...price.fast, [field]: value};
  else if (tier === 'long') price.longContext = {...price.longContext, rate: {...price.longContext?.rate, [field]: value}};
  else if (tier === 'long-fast') price.longContext = {...price.longContext, fast: {...price.longContext?.fast, [field]: value}};
  next.rates[id] = price; next.dirty = true;
  return next;
}

/** 将表单中的普通、Fast 和长上下文价格分别校验；全空表示删除该覆盖，零和高精度小数保持真实。 */
export function parsePrices(rates) {
  const result = {};
  /** 只转换四个计价字段，不把官网来源或嵌套对象当成数字；部分填写明确报错。 */
  function parseRate(rate, label) {
    const fields = ['input', 'output', 'read', 'write'], values = fields.map(field => rate?.[field] ?? '');
    if (values.every(value => String(value).trim() === '')) return null;
    if (values.some(value => String(value).trim() === '' || !Number.isFinite(Number(value)) || Number(value) < 0 || Number(value) > 10000)) throw new Error(`请完整填写${label}的四项 0–10000 单价，或将整组留空。`);
    return Object.fromEntries(fields.map((field,index) => [field, Number(values[index])]));
  }
  for (const [id, price] of Object.entries(rates)) {
    const standard = parseRate(price, '普通模式'), fast = parseRate(price.fast, 'Fast 模式');
    if (!standard) {if (fast) throw new Error('设置 Fast 价格前请先完整填写普通模式价格。'); continue;}
    const next = {...price, ...standard};
    if (fast) next.fast = fast; else delete next.fast;
    if (price.longContext) {
      const long = parseRate(price.longContext.rate, '长上下文'), longFast = parseRate(price.longContext.fast, '长上下文 Fast');
      if (!long) throw new Error('请完整填写长上下文价格。');
      next.longContext = {...price.longContext, rate: long};
      if (longFast) next.longContext.fast = longFast; else delete next.longContext.fast;
    }
    result[id] = next;
  }
  return result;
}
