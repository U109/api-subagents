import React, {useEffect, useMemo, useRef, useState} from 'react';
import * as Tabs from '@radix-ui/react-tabs';
import * as Dialog from '@radix-ui/react-dialog';
import * as AlertDialog from '@radix-ui/react-alert-dialog';
import {BarChart3, Activity, List, CircleDollarSign, RefreshCw, X, ChevronDown} from 'lucide-react';
import {USAGE_SOURCES, USAGE_RESULTS, usageKey, normalizeUsage, usageModels, selectUsageRows, usageSummary, usageBreakdown, usageBuckets, usageGroups, usageCost} from './usage-domain.mjs';
import {api} from './transport.mjs';
import {createUsageGate, priceForm, editPrice} from './usage-state.mjs';

const tabItems = [['overview', BarChart3, '概览'], ['analysis', Activity, '分析'], ['requests', List, '请求明细'], ['prices', CircleDollarSign, '价格统计']];

/** 展示已知整数，未知值使用破折号；大数只在卡片和图表轴上压缩，明细保留原值。 */
function number(value, compact = false) {
  if (value == null) return '—';
  if (compact && value >= 1000000) return (value / 1000000).toFixed(2) + 'M';
  if (compact && value >= 1000) return (value / 1000).toFixed(1) + 'K';
  return new Intl.NumberFormat('zh-CN', {maximumFractionDigits: 0}).format(value);
}

/** 将有效比例转换为百分比；不存在分母时不显示虚假的零成功率。 */
function percent(value) { return value == null ? '—' : (value * 100).toFixed(1) + '%'; }

/** 费用只展示合成数据的美元估算，未知记录不显示零元。 */
function money(value) { return value == null ? '—' : '$' + value.toFixed(3); }

/** 日期按本机时区展示，查询和存储始终使用 UTC 毫秒，不保留原型固定日期。 */
function timeLabel(value, short = false) {
  return new Intl.DateTimeFormat('zh-CN', {month: '2-digit', day: '2-digit', ...(short ? {} : {hour: '2-digit', minute: '2-digit', hour12: false})}).format(value);
}

/** 复用已有应用按钮风格，图标仅装饰，名称由文字或明确的 aria-label 给出。 */
function UsageButton({children, className = '', ...props}) { return <button type="button" className={`button button-secondary ${className}`} {...props}>{children}</button>; }

/** 测量实际图表容器并在宽度变化时重绘，窄屏不通过缩小整张 SVG 压缩字体。 */
function usePlotSize(active) {
  const ref = useRef(null), [width, setWidth] = useState(600);
  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const observer = new ResizeObserver(() => {if (element.clientWidth > 0) setWidth(Math.floor(element.clientWidth));});
    observer.observe(element);
    if (element.clientWidth > 0) setWidth(Math.floor(element.clientWidth));
    return () => observer.disconnect();
  }, [active]);
  return {ref, width};
}

/** 堆叠柱状趋势仅保留图例与图表；鼠标、触屏及隐藏的原生滑块共享时段读数，键盘聚焦时显示提示。 */
function TokenTrend({rows, range, models, now}) {
  const buckets = useMemo(() => usageBuckets(rows, range, models, now), [rows, range, models, now]);
  const [visible, setVisible] = useState({});
  const [selection, setSelection] = useState(null), [hover, setHover] = useState(null);
  const {ref, width} = usePlotSize(rows.length > 0);
  const shownModels = models.filter(model => visible[model.id] !== false);
  const height = width < 480 ? 288 : 376, left = width < 400 ? 45 : 55, right = 12, top = 26, bottom = 43;
  const plotW = Math.max(1, width - left - right), plotH = height - top - bottom, band = plotW / buckets.length;
  const maximum = Math.max(1000, ...buckets.map(bucket => shownModels.reduce((sum, model) => sum + bucket.values[model.id], 0)));
  const ceiling = Math.ceil(maximum / 10000) * 10000;
  const chosen = Math.min(selection ?? buckets.length - 1, buckets.length - 1), current = buckets[chosen];
  const inspected = hover == null ? null : Math.min(hover, buckets.length - 1);
  const readout = `${timeLabel(current.start)}，${number(shownModels.reduce((sum, model) => sum + current.values[model.id], 0))} Token，${current.requests} 请求${current.unknown ? `，${current.unknown} 条未上报` : ''}`;
  const tickCount = width < 400 ? 3 : width < 600 ? 4 : 5;
  const ticks = [...new Set(Array.from({length: Math.min(tickCount, buckets.length)}, (_, index) => Math.round(index * (buckets.length - 1) / (Math.min(tickCount, buckets.length) - 1))))];
  /** 根据真实 SVG 位置选择最近时间桶，限制边缘范围，不让 tooltip 把面板撑出屏幕。 */
  function inspect(event) {
    const box = event.currentTarget.getBoundingClientRect();
    const index = Math.min(buckets.length - 1, Math.max(0, Math.floor((event.clientX - box.left - left) / band)));
    setHover(index); setSelection(index);
  }
  return <section className="u-panel u-trend"><div className="u-panel-heading"><h2>Token 趋势</h2><span>{range === '24h' ? '按小时汇总' : '按日汇总'}</span></div>
    <div className="u-legend">{models.map(model => <button key={model.id} type="button" aria-pressed={visible[model.id] !== false} onClick={() => setVisible(previous => ({...previous, [model.id]: previous[model.id] === false}))}><span style={{background: model.color, opacity: visible[model.id] !== false ? 1 : .25}} aria-hidden="true" />{model.label}</button>)}</div>
    {!rows.length ? <div className="u-chart-empty">当前筛选下暂无请求</div> : <div ref={ref} className="u-plot">
      <input type="range" className="sr-only u-trend-reader" aria-label="选择趋势时段" aria-valuetext={readout} min={0} max={buckets.length - 1} step={1} value={chosen} onFocus={() => setHover(chosen)} onBlur={() => setHover(null)} onChange={event => {const index = Number(event.target.value); setSelection(index); setHover(index);}} />
      <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label="按模型堆叠的已知 Token 趋势" onPointerMove={inspect} onPointerLeave={() => setHover(null)} onClick={inspect}>
        <title>已知 Token 随时间变化，颜色区分模型</title><desc>缺失用量没有计入柱状高度。聚焦图表时可用方向键切换时段，查看提示和精确读数。</desc>
        {[0, 1, 2, 3, 4].map(index => <g key={index}><line x1={left} x2={width - right} y1={top + plotH * index / 4} y2={top + plotH * index / 4} className="u-chart-grid" /><text x={left - 8} y={top + plotH * index / 4 + 4} textAnchor="end" className="u-axis">{number(ceiling * (1 - index / 4), width < 600)}</text></g>)}
        {buckets.map((bucket, index) => {let sum = 0; return <g key={bucket.start}>{shownModels.map(model => {const value = bucket.values[model.id], y = top + plotH * (1 - (sum + value) / ceiling), h = value / ceiling * plotH; sum += value; return <rect key={model.id} x={left + index * band + 2} y={y} width={Math.max(1, band - 4)} height={h} fill={model.color} opacity={inspected === index ? 1 : .83} />;})}</g>;})}
        {ticks.map(index => <text key={index} x={left + (index + .5) * band} y={height - 15} className="u-axis" textAnchor={index === 0 ? 'start' : index === buckets.length - 1 ? 'end' : 'middle'}>{range === '24h' ? timeLabel(buckets[index].start).split(' ')[1] : timeLabel(buckets[index].start, true)}</text>)}
      </svg>
      {inspected != null && <div className="u-chart-tooltip" role="tooltip" style={{left: Math.max(0, Math.min(width - 230, left + inspected * band))}}><strong>{timeLabel(buckets[inspected].start)}</strong>{shownModels.map(model => <div key={model.id}><span>{model.label}</span><b>{number(buckets[inspected].values[model.id])}</b></div>)}</div>}
    </div>}
  </section>;
}

/** 右侧仅展示互斥分类和合计；缓存、推理仍从父项扣除，去除解释文字不改变统计口径。 */
function TokenBreakdown({rows}) {
  const breakdown = usageBreakdown(rows);
  return <section className="u-panel u-breakdown"><h2>Token 拆分</h2><div className="u-breakdown-items">{breakdown.items.map(item => <div className="u-breakdown-item" key={item.id}><div><span>{item.label}</span><strong>{number(item.value, true)}</strong><small>{percent(item.percent)}</small></div><div className="u-track" role="meter" aria-label={item.label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={(item.percent ?? 0) * 100}><span style={{width: `${(item.percent ?? 0) * 100}%`, background: item.color}} /></div></div>)}</div><div className="u-breakdown-total"><span>已知合计</span><strong>{number(breakdown.total, true)}</strong></div></section>;
}

/** 概览保留六项指标和双栏图表，移除底部冗余说明；未知值和已知费率标记仍由指标与明细呈现。 */
function Overview({rows, view, models, now}) {
  const summary = usageSummary(rows, view.rates);
  const metrics = [['总请求数', number(summary.requests), '逻辑请求'], ['平均输出 TPS', summary.tps == null ? '—' : summary.tps.toFixed(1), 'Token / 秒 · 请求耗时'], ['总 Token', number(summary.total, true), '已上报用量'], ['成功率', percent(summary.successRate), '成功 / 所有请求'], ['缓存命中率', percent(summary.cacheRate), '缓存读取 / 输入'], ['估算费用', money(summary.cost), 'USD · 已知估算']];
  return <><div className="u-kpis">{metrics.map(([label, value, hint]) => <article className="u-kpi" key={label}><span>{label}</span><strong>{value}</strong><small>{hint}</small></article>)}</div><div className="u-overview-grid"><TokenTrend rows={rows} range={view.filters.range} models={models} now={now} /><TokenBreakdown rows={rows} /></div></>;
}

/** 分析页按连接、模型或调用来源比较已知用量，统一继承顶部筛选，不混入工具次数或等待轮询。 */
function Analysis({rows, view, update}) {
  const groups = usageGroups(rows, view.group, view.rates);
  return <section className="u-panel"><div className="u-panel-heading"><h2>用量分析</h2><label>分组<select aria-label="分析分组" value={view.group} onChange={event => update({group: event.target.value})}><option value="model">按模型</option><option value="connection">按连接</option><option value="source">按来源</option></select></label></div><div className="u-table-wrap"><table className="u-table"><thead><tr><th>分组</th><th>请求数</th><th>输入 Token</th><th>输出 Token</th><th>总 Token</th><th>成功率</th><th>估算费用</th></tr></thead><tbody>{groups.map(group => <tr key={group.id}><td>{view.group === 'source' ? USAGE_SOURCES[group.id] : group.id}</td><td>{number(group.requests)}</td><td>{number(group.input)}</td><td>{number(group.output)}</td><td>{number(group.total)}</td><td>{percent(group.successRate)}</td><td>{money(group.cost)}</td></tr>)}</tbody></table></div>{!groups.length && <div className="u-empty">当前筛选下暂无请求</div>}</section>;
}

/** 请求列表分页并保留未知字段；查看详情只展开脱敏合成元数据，不展示提示词和响应正文。 */
function Requests({rows, view, update, openDetail}) {
  const page = Math.min(view.page, Math.max(0, Math.ceil(rows.length / 8) - 1)), slice = rows.slice(page * 8, page * 8 + 8);
  return <section className="u-panel"><div className="u-panel-heading"><h2>请求明细</h2><span>{number(rows.length)} 条请求</span></div><div className="u-table-wrap"><table className="u-table u-request-table"><thead><tr><th>时间</th><th>连接 / 模型</th><th>来源</th><th>请求结果</th><th>总 Token</th><th>估算费用</th><th>操作</th></tr></thead><tbody>{slice.map(row => <tr key={row.id}><td>{timeLabel(row.startedAt)}</td><td><strong>{row.connection}</strong><small className="u-mono">{row.model}</small></td><td>{USAGE_SOURCES[row.source]}</td><td><span className={`u-result u-result-${row.result}`}>{USAGE_RESULTS[row.result]}</span></td><td>{number(row.total)}{row.total == null && <small>未上报</small>}</td><td>{money(usageCost(row, view.rates))}</td><td><UsageButton onClick={() => openDetail(row.id)} aria-label={`查看请求 ${row.id}`}>详情</UsageButton></td></tr>)}</tbody></table></div>{!rows.length && <div className="u-empty">当前筛选下暂无请求</div>}<div className="u-pagination"><span>{rows.length ? `${page * 8 + 1}–${Math.min(rows.length, page * 8 + 8)}` : '0'} / {number(rows.length)}</span><div><UsageButton disabled={page === 0} onClick={() => update({page: page - 1})}>上一页</UsageButton><UsageButton disabled={(page + 1) * 8 >= rows.length} onClick={() => update({page: page + 1})}>下一页</UsageButton></div></div></section>;
}

/** 受控保存费率，草稿跨页面保留；外部修订冲突须明确核对，重新载入前确认放弃输入。 */
function Prices({rates, models, revision, draft, changeDraft, save}) {
  const [error, setError] = useState(''), [saving, setSaving] = useState(false);
  const form = priceForm(draft, rates, models, revision), stale = form.dirty && form.revision !== revision;
  const fields = [['input', '普通输入'], ['output', '输出'], ['read', '缓存读取'], ['write', '缓存创建']];
  /** 保存完整合法的费率和开始编辑时的修订；保留不可见模型，失败或并发修改不清除草稿。 */
  async function submit(event) {
    event.preventDefault(); const next = {};
    if (stale) {setError('费率已被其他窗口修改。你的草稿已保留，请载入最新费率后重新核对。'); return;}
    for (const [id, price] of Object.entries(form.rates)) {
      const values = Object.values(price); if (values.every(value => String(value).trim() === '')) continue;
      if (values.some(value => String(value).trim() === '' || !Number.isFinite(Number(value)) || Number(value)<0 || Number(value)>10000)) {setError('请完整填写 0–10000 范围内的单价，或将整行留空。'); return;}
      next[id] = Object.fromEntries(Object.entries(price).map(([field,value]) => [field,Number(value)]));
    }
    setSaving(true); setError(''); try {await save(next, form.revision); changeDraft(previous => previous === draft ? null : previous);} catch (failure) {setError(failure.message);} finally {setSaving(false);}
  }
  return <section className="u-panel"><div className="u-panel-heading"><h2>模型价格配置</h2><span>USD / 百万 Token</span></div><p className="u-secondary">价格由你配置，留空表示未定价。新价格只影响之后开始的请求，历史费用按原费率快照保留。</p><form onSubmit={submit} aria-busy={saving}><div className="u-prices">{models.map(model => <fieldset key={model.id} disabled={saving}><legend>{model.connection} · {model.model}</legend><div>{fields.map(([field, label]) => <label key={field}>{label}<input type="number" min="0" max="10000" step="0.01" aria-label={`${model.model} ${label}单价`} aria-invalid={Boolean(error || stale)} aria-describedby={error || stale ? 'usage-price-error' : undefined} value={form.rates[model.id]?.[field] ?? ''} onChange={event => changeDraft(previous => editPrice(previous, rates, models, revision, model.id, field, event.target.value))} /></label>)}</div></fieldset>)}</div>{(error || stale) && <p id="usage-price-error" className="u-error" role="alert">{error || '费率已被其他窗口修改。你的草稿已保留，请载入最新费率后重新核对。'}</p>}<div className="u-price-actions"><button type="submit" className="button button-primary" disabled={saving}>保存费率</button>{stale && <AlertDialog.Root><AlertDialog.Trigger asChild><button type="button" className="button button-secondary" disabled={saving}>载入最新费率</button></AlertDialog.Trigger><AlertDialog.Portal><AlertDialog.Overlay className="dialog-overlay" /><AlertDialog.Content className="dialog-content"><AlertDialog.Title className="dialog-title">放弃未保存费率？</AlertDialog.Title><AlertDialog.Description className="dialog-description">将放弃当前费率草稿并载入其他窗口保存的最新费率，历史请求费用不会改变。</AlertDialog.Description><div className="dialog-actions"><AlertDialog.Cancel asChild><button type="button" className="button button-secondary">保留草稿</button></AlertDialog.Cancel><AlertDialog.Action asChild><button type="button" className="button button-primary" onClick={() => {changeDraft(() => null); setError('');}}>放弃草稿并载入</button></AlertDialog.Action></div></AlertDialog.Content></AlertDialog.Portal></AlertDialog.Root>}</div></form></section>;
}

/** 详情对话框由 Radix 管理焦点和 Escape；成功状态与用量完整性独立，未知仍显示为破折号。 */
function RequestDetail({row, close, rates, opener}) {
  return <Dialog.Root open={Boolean(row)} onOpenChange={open => {if (!open) close();}}><Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content u-detail-dialog" onCloseAutoFocus={event => {event.preventDefault(); if (opener.current?.isConnected) opener.current.focus();}}><Dialog.Title className="dialog-title">请求详情</Dialog.Title><Dialog.Description className="dialog-description">仅展示脱敏状态和用量，不含提示词、回答或 Key。</Dialog.Description><Dialog.Close asChild><button type="button" className="button button-ghost icon-button u-detail-close" aria-label="关闭请求详情"><X size={18} aria-hidden="true" /></button></Dialog.Close>{row && <dl className="u-detail-grid">{[['请求 ID', row.id], ['连接 / 模型', `${row.connection} / ${row.model}`], ['请求结果', USAGE_RESULTS[row.result]], ['调用来源', USAGE_SOURCES[row.source]], ['输入 Token', number(row.input)], ['输出 Token', number(row.output)], ['缓存读取', number(row.read)], ['缓存创建', number(row.write)], ['推理 Token', number(row.reasoning)], ['上游尝试', row.attempts], ['用量质量', row.total == null ? '未上报，不按零处理' : row.attempts > 1 ? '部分尝试未知，只合计已知值' : '已上报；仅合计已知数据'], ['估算费用', `${money(usageCost(row, rates))} · 已知费率`]].map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>}</Dialog.Content></Dialog.Portal></Dialog.Root>;
}

/** 精简用量页保留四个页签和四项筛选；删除管理入口与底栏后仍保留父级状态和请求结果明细。 */
export function UsagePage({view, onChange, connections, requestGate}) {
  const detailOpener = useRef(null), alive = useRef(true), pricesRevision = useRef(null), gate = useRef(requestGate || createUsageGate());
  const [records, setRecords] = useState([]), [now, setNow] = useState(Date.now()), [loading, setLoading] = useState(true), [readError, setReadError] = useState(''), [degraded, setDegraded] = useState('');
  const allRows = useMemo(() => normalizeUsage(records), [records]);
  const models = useMemo(() => usageModels(allRows, connections), [allRows, connections]);
  const rows = useMemo(() => selectUsageRows(allRows, view, now), [allRows, view, now]);
  const detail = allRows.find(row => row.id === view.detail);
  /** 读取最近三十天真实记录；只接受最新读取，保存费率期间的旧修订不能回写，失败保留旧数据。 */
  async function load() {
    const ticket = gate.current.beginRead();
    try {
      const to = Date.now(), value = await api('/api/usage', {from: to-30*86400000, to});
      if (!alive.current || !gate.current.acceptsRead(ticket)) return;
      setRecords(value.records || []); setNow(value.now || to); setReadError(''); setDegraded(value.skipped || value.lost ? '部分记录读取或写入失败，统计可能不完整' : '');
      if (gate.current.acceptsPrices(ticket) && pricesRevision.current !== value.prices.revision) {pricesRevision.current = value.prices.revision; onChange(previous => ({...previous, rates: value.prices.rates || {}, revision: value.prices.revision}));}
    } catch (failure) {if (alive.current && gate.current.acceptsRead(ticket)) setReadError(failure.message);} finally {if (alive.current && gate.current.acceptsRead(ticket)) setLoading(false);}
  }
  useEffect(() => {alive.current=true; void load(); const timer=setInterval(load,10000); return () => {alive.current=false; gate.current.invalidate(); clearInterval(timer);};}, []);
  /** 将用户单价保存至独立费率文件；后端修订检查失败时不覆盖其他窗口的新价格。 */
  async function savePrices(rates, revision) {
    gate.current.beginWrite();
    try {const value=await api('/api/usage/prices', {revision,rates}); pricesRevision.current=value.prices.revision; onChange(previous=>({...previous,rates:value.prices.rates,revision:value.prices.revision,message:'费率已保存，将用于后续请求'}));}
    finally {gate.current.endWrite();}
  }
  /** 费率草稿由工作区持有，切换页签或页面不丢失，后台刷新只更新权威费率而不清除输入。 */
  function changePriceDraft(updateDraft) {onChange(previous => ({...previous, priceDraft: updateDraft(previous.priceDraft)}));}
  /** 局部更新用量视图状态，不触碰连接草稿、接入配置或任务正文。 */
  function update(patch) {onChange(previous => ({...previous, ...patch}));}
  /** 记录当前详情按钮，交给 Radix 的关闭生命周期恢复焦点，不重建焦点陷阱。 */
  function openDetail(id) {detailOpener.current = document.activeElement; update({detail: id});}
  /** 顶部筛选变动后重置分页；切换连接时不保留不属于该连接的模型条件。 */
  function changeFilter(field, value) {
    const filters = {...view.filters, [field]: value};
    if (field === 'connection' && value !== 'all' && !models.some(model => model.connection === value && model.id === filters.model)) filters.model = 'all';
    update({filters, page: 0, message: ''});
  }
  const filters = [
    ['range', '时间范围', [['24h', '近 24 小时'], ['7d', '近 7 天'], ['30d', '近 30 天']]],
    ['model', '模型', [['all', '全部模型'], ...models.filter(model => view.filters.connection === 'all' || model.connection === view.filters.connection).map(model => [model.id, model.model])]],
    ['connection', '连接', [['all', '全部连接'], ...[...new Set([...Object.keys(connections), ...models.map(model => model.connection)])].map(id => [id, connections[id]?.name ?? `${id}（历史连接）`])]],
    ['source', '来源', [['all', '全部业务调用'], ...Object.entries(USAGE_SOURCES)]]
  ];
  return <div className="usage-reference" data-testid="usage-panel"><Tabs.Root value={tabItems.some(([id]) => id === view.tab) ? view.tab : 'overview'} onValueChange={tab => update({tab})}><header className="u-heading"><h1>用量统计</h1><Tabs.List className="u-tabs" aria-label="用量统计页面">{tabItems.map(([id, Icon, label]) => <Tabs.Trigger value={id} key={id}><Icon size={15} aria-hidden="true" />{label}</Tabs.Trigger>)}</Tabs.List></header>
    <div className="u-status-line"><div className="u-collection"><span className={view.collecting ? 'u-collect-dot' : 'u-collect-dot paused'} aria-hidden="true" /><strong>{'本地用量记录'}</strong><span>{number(records.length)} 条上游尝试</span></div><UsageButton className="u-refresh" aria-label="刷新用量记录" onClick={() => void load()}><RefreshCw size={15} aria-hidden="true" /></UsageButton><span className="u-demo-label">用量来源：上游上报</span></div>
    {(readError || degraded) && <div className="field-error" role="alert">{readError || degraded}</div>}{loading && <p role="status">正在读取本地用量记录…</p>}<section className="u-filters" aria-label="全局用量筛选">{filters.map(([field, label, options]) => <label key={field}>{label}<span className="u-filter-control"><select aria-label={`用量${label}`} value={view.filters[field]} onChange={event => changeFilter(field, event.target.value)}>{options.map(([id, text]) => <option value={id} key={id}>{text}</option>)}</select><ChevronDown size={14} aria-hidden="true" /></span></label>)}</section>
    <Tabs.Content value="overview">{loading || readError && !records.length ? <section className="u-panel" role="status">{loading ? '正在读取本地用量…' : '无法读取记录，请刷新重试'}</section> : <Overview rows={rows} view={view} models={models} now={now} />}</Tabs.Content>
    <Tabs.Content value="analysis"><Analysis rows={rows} view={view} update={update} /></Tabs.Content>
    <Tabs.Content value="requests"><Requests rows={rows} view={view} update={update} openDetail={openDetail} /></Tabs.Content>
    <Tabs.Content value="prices"><Prices rates={view.rates} models={models} revision={view.revision} draft={view.priceDraft} changeDraft={changePriceDraft} save={savePrices} /></Tabs.Content>
    <div className="u-message" role="status" aria-live="polite">{view.message}</div>
  </Tabs.Root><RequestDetail row={detail} close={() => update({detail: null})} rates={view.rates} opener={detailOpener} /></div>;
}
