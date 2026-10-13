export const USAGE_SOURCES = {main: '主对话接入', proposal: '提案型子代理', codex: '执行型子代理', image: '图片生成', test: '连接测试'};
export const USAGE_RESULTS = {success: '成功', failed: '失败', cancelled: '已取消', unknown: '终态未知'};
// 分类趋势优先使用蓝、绿、黄；Token 拆分仍保留各字段的固定语义色。
const seriesColors = ['#437ef7', '#35b58c', '#d4a017', '#8b5cf6', '#db77a4', '#7896ad'];

/** 连接与上游模型的 JSON 元组与 Go 账本一致，避免名称或斜线造成计价键碰撞。 */
export function usageKey(connection, model) { return JSON.stringify([connection, model]); }

/** 初始用量视图没有模拟记录与默认价格，只保留原型已确认的四项筛选。 */
export function createUsageView() { return {tab: 'overview', filters: {range: '24h', model: 'all', connection: 'all', source: 'all'}, rates: {}, revision: '', priceDraft: null, page: 0, group: 'model', refresh: 0, message: '', detail: null}; }

/** 把独立上游尝试按逻辑 ID 折叠为请求行，保留每次尝试和未知字段，不将失败重试当成免费。 */
export function normalizeUsage(records) {
  const groups = new Map();
  for (const record of records) {if (!groups.has(record.logicalId)) groups.set(record.logicalId, []); groups.get(record.logicalId).push(record);}
  return [...groups.entries()].map(([id, attempts]) => {
    attempts.sort((a,b) => a.attempt - b.attempt);
    const first = attempts[0], last = attempts.at(-1);
    const result = {id, startedAt: first.startedAt, connectionID: first.connectionId, connection: first.connection, model: first.model, modelKey: usageKey(first.connectionId, first.model), source: first.source, result: last.outcome, taskID: first.taskId, attempts: attempts.length, attemptRecords: attempts, firstByteMS: last.firstByteMS, generationMS: attempts.filter(row => row.output != null).reduce((sum,row) => sum + row.durationMS, 0)};
    for (const field of ['input', 'output', 'total', 'read', 'write', 'reasoning']) result[field] = usageSum(attempts, field);
    const costs = attempts.filter(row => row.costMicroUSD != null);
    result.costUSD = costs.length ? costs.reduce((sum,row) => sum + row.costMicroUSD, 0) / 1000000 : null;
    result.quality = attempts.every(row => row.quality === 'complete') ? 'complete' : attempts.some(row => row.total != null) ? 'partial' : last.quality;
    return result;
  }).sort((a,b) => b.startedAt - a.startedAt || b.id.localeCompare(a.id));
}

/** 模型来自保存连接与历史，有已知业务用量的系列优先分配蓝、绿、黄；按完整账本固定颜色，筛选不重新配色。 */
export function usageModels(rows, connections) {
  const models = new Map();
  for (const connection of Object.values(connections)) for (const model of connection.models || []) {
    const id = usageKey(connection.id, model.id);
    models.set(id, {id, connection: connection.id, model: model.id, label: `${connection.name} · ${model.name || model.id}`});
  }
  for (const row of rows) if (!models.has(row.modelKey)) models.set(row.modelKey, {id: row.modelKey, connection: row.connectionID, model: row.model, label: `${row.connection} · ${row.model}`});
  const used = new Set(rows.filter(row => row.source !== 'test' && row.total > 0).map(row => row.modelKey));
  const ordered = [...models.values()].sort((a, b) => Number(used.has(b.id)) - Number(used.has(a.id)));
  return ordered.map((model,index) => ({...model, color: seriesColors[index % seriesColors.length]}));
}

/** 范围使用真实查询时点与 UTC 毫秒，不在正式程序中保留演示日期。 */
export function usageRange(range, now = Date.now()) {const span = (range === '24h' ? 1 : range === '7d' ? 7 : 30) * 86400000; return {from: now - span, to: now + 1, span};}

/** 四项筛选统一作用于概览、分析与请求列表；未暴露的 Key 或结果条件不会隐藏记录。 */
export function selectUsageRows(rows, view, now) {
  const bounds = usageRange(view.filters.range, now), filter = view.filters;
  return rows.filter(row => row.startedAt >= bounds.from && row.startedAt < bounds.to && (filter.model === 'all' || row.modelKey === filter.model) && (filter.connection === 'all' || row.connectionID === filter.connection) && (filter.source === 'all' ? row.source !== 'test' : row.source === filter.source));
}

/** 聚合只相加已经上报的数值；空集合为零，存在记录但所有字段缺失则为未知。 */
export function usageSum(rows, field) {if (!rows.length) return 0; const known = rows.filter(row => Number.isFinite(row[field])); return known.length ? known.reduce((sum,row) => sum + row[field], 0) : null;}

/** 历史费用使用请求开始时结算的微美元，不因用户修改单价而静默重算。 */
export function usageCost(row) {return row.costUSD;}

/** 汇总真实观测的输出吞吐与覆盖率；TPS 为已知输出除以对应请求耗时，不冒充服务端生成性能。 */
export function usageSummary(rows) {
  const timed = rows.filter(row => row.output != null && row.generationMS > 0), input = usageSum(rows, 'input'), read = usageSum(rows, 'read');
  return {requests: rows.length, input, output: usageSum(rows,'output'), total: usageSum(rows,'total'), known: rows.filter(row => row.total != null).length,
    successRate: rows.length ? rows.filter(row => row.result === 'success').length / rows.length : null,
    cacheRate: input > 0 && read != null ? read / input : null,
    tps: timed.length ? timed.reduce((sum,row) => sum + row.output,0) / (timed.reduce((sum,row) => sum + row.generationMS,0) / 1000) : null,
    cost: usageSum(rows,'costUSD'), priced: rows.filter(row => row.costUSD != null).length};
}

/** 拆分仅使用缓存与推理明细完整的记录；缺失分类保留未知，不显示为零占比。 */
export function usageBreakdown(rows) {
  const known = rows.filter(row => [row.input,row.output,row.read,row.write,row.reasoning].every(value => value != null) && row.input >= row.read + row.write && row.output >= row.reasoning);
  const total = known.reduce((sum,row) => sum + row.input + row.output,0), missing = rows.length && !known.length;
  const items = [
    {id:'input',label:'普通输入',color:'#437ef7',value:known.reduce((s,r)=>s+r.input-r.read-r.write,0)},
    {id:'output',label:'生成输出',color:'#35b58c',value:known.reduce((s,r)=>s+r.output-r.reasoning,0)},
    {id:'reasoning',label:'推理',color:'#8b5cf6',value:known.reduce((s,r)=>s+r.reasoning,0)},
    {id:'read',label:'缓存读取',color:'#f59e0b',value:known.reduce((s,r)=>s+r.read,0)},
    {id:'write',label:'缓存创建',color:'#ec4899',value:known.reduce((s,r)=>s+r.write,0)}
  ];
  return {total: missing ? null : total, known: known.length, items: items.map(item => ({...item, value: missing ? null : item.value, percent: missing ? null : total ? item.value / total : 0}))};
}

/** 连续时间桶只汇总实际已知用量；没有用量的请求仍保留数量，模型数超过六项时颜色循环。 */
export function usageBuckets(rows, range, models, now) {
  const bounds = usageRange(range,now), count = range === '24h' ? 24 : range === '7d' ? 7 : 30, interval = bounds.span / count;
  return Array.from({length:count},(_,index) => {const start = bounds.from + index * interval, items = rows.filter(row=>row.startedAt>=start && row.startedAt<start+interval); return {start,requests:items.length,unknown:items.filter(row=>row.total==null).length,values:Object.fromEntries(models.map(model=>[model.id,items.filter(row=>row.modelKey===model.id && row.total!=null).reduce((sum,row)=>sum+row.total,0)]))};});
}

/** 分组键使用稳定身份；同名模型跨连接不会被误合并，费用仍是历史快照。 */
export function usageGroups(rows, group) {const groups = new Map(); for (const row of rows) {const key = group === 'model' ? row.modelKey : group === 'source' ? row.source : row.connection; if (!groups.has(key)) groups.set(key,[]); groups.get(key).push(row);} return [...groups].map(([id,items])=>({id,rows:items,...usageSummary(items)})).sort((a,b)=>(b.total??-1)-(a.total??-1));}
