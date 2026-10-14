import React, {useEffect, useId, useLayoutEffect, useMemo, useRef, useState} from 'react';
import * as Tabs from '@radix-ui/react-tabs';
import * as Dialog from '@radix-ui/react-dialog';
import * as AlertDialog from '@radix-ui/react-alert-dialog';
import * as DropdownMenu from '@radix-ui/react-dropdown-menu';
import {BarChart3, Activity, List, CircleDollarSign, RefreshCw, X, ChevronDown, Check} from 'lucide-react';
import {USAGE_SOURCES, USAGE_RESULTS, usageKey, normalizeUsage, usageModels, configuredPriceModels, usageSeriesModels, selectUsageRows, usageSummary, usageVisibleBreakdown, usageBuckets, usageGroups, usageCost} from './usage-domain.mjs';
import {placeUsageTooltip} from './usage-tooltip.mjs';
import {api} from './transport.mjs';
import {createUsageGate, priceForm, editPrice, parsePrices} from './usage-state.mjs';

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

/** 复用现有 Radix 菜单实现单选筛选；支持方向键、文字检索、Escape 和关闭后焦点恢复，长名称可完整读取。 */
function UsageSelect({label, options, value, onChange}) {
  const valueID = useId(), selected = options.find(([id]) => id === value)?.[1] ?? options[0]?.[1] ?? '请选择';
  return <DropdownMenu.Root><DropdownMenu.Trigger asChild><button type="button" className="usage-select-trigger" aria-label={label} aria-describedby={valueID}><span id={valueID} className="usage-select-value" title={selected}>{selected}</span><ChevronDown size={14} aria-hidden="true" /></button></DropdownMenu.Trigger><DropdownMenu.Portal><DropdownMenu.Content className="dropdown-content usage-select-menu" align="start" sideOffset={6} collisionPadding={12}><DropdownMenu.RadioGroup value={value} onValueChange={onChange}>{options.map(([id, text]) => <DropdownMenu.RadioItem key={id} value={id} textValue={text} className="dropdown-item usage-select-item"><span title={text}>{text}</span><DropdownMenu.ItemIndicator><Check size={15} aria-hidden="true" /></DropdownMenu.ItemIndicator></DropdownMenu.RadioItem>)}</DropdownMenu.RadioGroup></DropdownMenu.Content></DropdownMenu.Portal></DropdownMenu.Root>;
}

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

/** 提示使用实际内容尺寸跟随悬停点或选中柱，首帧和内容变化时测量，不在每次鼠标移动时反复测量。 */
function TrendTooltip({id, bucket, models, anchor, width, height}) {
  const ref = useRef(null), [size, setSize] = useState({width: 230, height: 100});
  useLayoutEffect(() => {
    const node = ref.current;
    /** 内容或容器改变时更新实际提示尺寸，只有尺寸变化才触发重绘，避免移动本身造成测量循环。 */
    function measure() {
      const next = {width: node.offsetWidth, height: node.offsetHeight};
      setSize(previous => previous.width === next.width && previous.height === next.height ? previous : next);
    }
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, []);
  return <div ref={ref} id={id} className="u-chart-tooltip" role="tooltip" style={placeUsageTooltip(anchor, {width, height}, size)}><strong>{timeLabel(bucket.start)}</strong>{models.map(model => <div key={model.id}><span>{model.label}</span><b>{number(bucket.values[model.id])}</b></div>)}</div>;
}

/** 趋势只显示有记录模型；鼠标提示跟随悬停位置，键盘提示锚定选中柱，保持原有颜色和精确读数。 */
function TokenTrend({rows, range, models, now}) {
  const recordedModels = useMemo(() => usageSeriesModels(rows, models), [rows, models]);
  const buckets = useMemo(() => usageBuckets(rows, range, recordedModels, now), [rows, range, recordedModels, now]);
  const [visible, setVisible] = useState({});
  const [selection, setSelection] = useState(null), [hover, setHover] = useState(null);
  const [pointer, setPointer] = useState(null), tooltipID = useId();
  const {ref, width} = usePlotSize(rows.length > 0);
  const shownModels = recordedModels.filter(model => visible[model.id] !== false);
  const height = width < 480 ? 288 : 376, left = width < 400 ? 45 : 55, right = 12, top = 26, bottom = 43;
  const plotW = Math.max(1, width - left - right), plotH = height - top - bottom, band = plotW / buckets.length;
  const maximum = Math.max(1000, ...buckets.map(bucket => shownModels.reduce((sum, model) => sum + bucket.values[model.id], 0)));
  const ceiling = Math.ceil(maximum / 10000) * 10000;
  const chosen = Math.min(selection ?? buckets.length - 1, buckets.length - 1), current = buckets[chosen];
  const inspected = hover == null ? null : Math.min(hover, buckets.length - 1);
  const readout = `${timeLabel(current.start)}，${number(shownModels.reduce((sum, model) => sum + current.values[model.id], 0))} Token，${current.requests} 请求${current.unknown ? `，${current.unknown} 条未上报` : ''}`;
  const tickCount = width < 400 ? 3 : width < 600 ? 4 : 5;
  const ticks = [...new Set(Array.from({length: Math.min(tickCount, buckets.length)}, (_, index) => Math.round(index * (buckets.length - 1) / (Math.min(tickCount, buckets.length) - 1))))];
  const inspectedTotal = inspected == null ? 0 : shownModels.reduce((sum, model) => sum + buckets[inspected].values[model.id], 0);
  const anchor = pointer ?? {x: left + ((inspected ?? chosen) + .5) * band, y: top + plotH * (1 - inspectedTotal / ceiling)};
  /** 根据实际 SVG 坐标选择时间桶并记录悬停点，提示跟随鼠标；图表边缘由定位函数自动避让。 */
  function inspect(event) {
    const box = event.currentTarget.getBoundingClientRect();
    const index = Math.min(buckets.length - 1, Math.max(0, Math.floor((event.clientX - box.left - left) / band)));
    setPointer({x: event.clientX - box.left, y: event.clientY - box.top});
    setHover(index); setSelection(index);
  }
  return <section className="u-panel u-trend"><div className="u-panel-heading"><h2>Token 趋势</h2><span>{range === '24h' ? '按小时汇总' : '按日汇总'}</span></div>
    {recordedModels.length > 0 && <div className="u-legend">{recordedModels.map(model => <button key={model.id} type="button" aria-pressed={visible[model.id] !== false} onClick={() => setVisible(previous => ({...previous, [model.id]: previous[model.id] === false}))}><span style={{background: model.color, opacity: visible[model.id] !== false ? 1 : .25}} aria-hidden="true" />{model.label}</button>)}</div>}
    {!rows.length ? <div className="u-chart-empty">当前筛选下暂无请求</div> : <div ref={ref} className="u-plot">
      <input type="range" className="sr-only u-trend-reader" aria-label="选择趋势时段" aria-valuetext={readout} aria-describedby={inspected != null ? tooltipID : undefined} min={0} max={buckets.length - 1} step={1} value={chosen} onFocus={() => {setPointer(null); setHover(chosen);}} onBlur={() => {setPointer(null); setHover(null);}} onKeyDown={event => {if (event.key === 'Escape') {setPointer(null); setHover(null);}}} onChange={event => {const index = Number(event.target.value); setPointer(null); setSelection(index); setHover(index);}} />
      <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label="按模型堆叠的已知 Token 趋势" onPointerMove={inspect} onPointerLeave={() => {setPointer(null); setHover(null);}} onClick={inspect}>
        <title>已知 Token 随时间变化，颜色区分模型</title><desc>缺失用量没有计入柱状高度。聚焦图表时可用方向键切换时段，查看提示和精确读数。</desc>
        {[0, 1, 2, 3, 4].map(index => <g key={index}><line x1={left} x2={width - right} y1={top + plotH * index / 4} y2={top + plotH * index / 4} className="u-chart-grid" /><text x={left - 8} y={top + plotH * index / 4 + 4} textAnchor="end" className="u-axis">{number(ceiling * (1 - index / 4), width < 600)}</text></g>)}
        {buckets.map((bucket, index) => {let sum = 0; return <g key={bucket.start}>{shownModels.map(model => {const value = bucket.values[model.id], y = top + plotH * (1 - (sum + value) / ceiling), h = value / ceiling * plotH; sum += value; return <rect key={model.id} x={left + index * band + 2} y={y} width={Math.max(1, band - 4)} height={h} fill={model.color} opacity={inspected === index ? 1 : .83} />;})}</g>;})}
        {ticks.map(index => <text key={index} x={left + (index + .5) * band} y={height - 15} className="u-axis" textAnchor={index === 0 ? 'start' : index === buckets.length - 1 ? 'end' : 'middle'}>{range === '24h' ? timeLabel(buckets[index].start).split(' ')[1] : timeLabel(buckets[index].start, true)}</text>)}
      </svg>
      {inspected != null && <TrendTooltip id={tooltipID} bucket={buckets[inspected]} models={shownModels} anchor={anchor} width={width} height={height} />}
    </div>}
  </section>;
}

/** 拆分只显示有值的分类和已知合计；没有分类时不显示面板，缓存和推理仍从父项扣除。 */
function TokenBreakdown({breakdown}) {
  if (!breakdown.items.length) return null;
  return <section className="u-panel u-breakdown"><h2>Token 拆分</h2><div className="u-breakdown-items">{breakdown.items.map(item => <div className="u-breakdown-item" key={item.id}><div><span>{item.label}</span><strong>{number(item.value, true)}</strong><small>{percent(item.percent)}</small></div><div className="u-track" role="meter" aria-label={item.label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={(item.percent ?? 0) * 100}><span style={{width: `${(item.percent ?? 0) * 100}%`, background: item.color}} /></div></div>)}</div><div className="u-breakdown-total"><span>已知合计</span><strong>{number(breakdown.total, true)}</strong></div></section>;
}

/** 概览仅展示六项指标的名称和数值；有拆分时使用双栏，否则趋势图填满，统计与未知值语义不变。 */
function Overview({rows, view, models, now}) {
  const summary = usageSummary(rows, view.rates);
  const breakdown = usageVisibleBreakdown(rows);
  const metrics = [['总请求数', number(summary.requests)], ['平均输出 TPS', summary.tps == null ? '—' : summary.tps.toFixed(1)], ['总 Token', number(summary.total, true)], ['成功率', percent(summary.successRate)], ['缓存命中率', percent(summary.cacheRate)], ['估算费用', money(summary.cost)]];
  return <><div className="u-kpis">{metrics.map(([label, value]) => <article className="u-kpi" key={label}><span>{label}</span><strong>{value}</strong></article>)}</div><div className={`u-overview-grid${breakdown.items.length ? '' : ' without-breakdown'}`}><TokenTrend rows={rows} range={view.filters.range} models={models} now={now} /><TokenBreakdown breakdown={breakdown} /></div></>;
}

/** 分析页按连接、模型或来源比较已知用量；分组菜单保留可访问名称，不再重复展示上方标签文字。 */
function Analysis({rows, view, update}) {
  const groups = usageGroups(rows, view.group, view.rates);
  return <section className="u-panel"><div className="u-panel-heading"><h2>用量分析</h2><div className="u-analysis-group"><UsageSelect label="分析分组" value={view.group} options={[["model", "按模型"], ["connection", "按连接"], ["source", "按来源"]]} onChange={group => update({group})} /></div></div><div className="u-table-wrap"><table className="u-table"><thead><tr><th>分组</th><th>请求数</th><th>输入 Token</th><th>输出 Token</th><th>总 Token</th><th>成功率</th><th>估算费用</th></tr></thead><tbody>{groups.map(group => <tr key={group.id}><td>{view.group === 'source' ? USAGE_SOURCES[group.id] : group.id}</td><td>{number(group.requests)}</td><td>{number(group.input)}</td><td>{number(group.output)}</td><td>{number(group.total)}</td><td>{percent(group.successRate)}</td><td>{money(group.cost)}</td></tr>)}</tbody></table></div>{!groups.length && <div className="u-empty">当前筛选下暂无请求</div>}</section>;
}

/** 请求列表分页并保留未知字段；查看详情只展开脱敏合成元数据，不展示提示词和响应正文。 */
function Requests({rows, view, update, openDetail}) {
  const page = Math.min(view.page, Math.max(0, Math.ceil(rows.length / 8) - 1)), slice = rows.slice(page * 8, page * 8 + 8);
  return <section className="u-panel"><div className="u-panel-heading"><h2>请求明细</h2><span>{number(rows.length)} 条请求</span></div><div className="u-table-wrap"><table className="u-table u-request-table"><thead><tr><th>时间</th><th>连接 / 模型</th><th>来源</th><th>请求结果</th><th>总 Token</th><th>估算费用</th><th>操作</th></tr></thead><tbody>{slice.map(row => <tr key={row.id}><td>{timeLabel(row.startedAt)}</td><td><strong>{row.connection}</strong><small className="u-mono">{row.model}</small></td><td>{USAGE_SOURCES[row.source]}</td><td><span className={`u-result u-result-${row.result}`}>{USAGE_RESULTS[row.result]}</span></td><td>{number(row.total)}{row.total == null && <small>未上报</small>}</td><td>{money(usageCost(row, view.rates))}</td><td><UsageButton onClick={() => openDetail(row.id)} aria-label={`查看请求 ${row.id}`}>详情</UsageButton></td></tr>)}</tbody></table></div>{!rows.length && <div className="u-empty">当前筛选下暂无请求</div>}<div className="u-pagination"><span>{rows.length ? `${page * 8 + 1}–${Math.min(rows.length, page * 8 + 8)}` : '0'} / {number(rows.length)}</span><div><UsageButton disabled={page === 0} onClick={() => update({page: page - 1})}>上一页</UsageButton><UsageButton disabled={(page + 1) * 8 >= rows.length} onClick={() => update({page: page + 1})}>下一页</UsageButton></div></div></section>;
}

/** 费率表不重复展示说明，受控草稿跨页面保留；外部修订冲突须核对，重新载入前确认放弃输入。 */
function Prices({rates, models, revision, draft, changeDraft, save}) {
  const [error, setError] = useState(''), [saving, setSaving] = useState(false);
  const form = priceForm(draft, rates, models, revision), stale = form.dirty && form.revision !== revision;
  const fields = [['input', '普通输入'], ['output', '输出'], ['read', '缓存读取'], ['write', '缓存创建']];
  /** 保存普通和 Fast 的完整费率，保留隐藏项与历史快照；失败、冲突和跨窗口编辑不清除草稿。 */
  async function submit(event) {
    event.preventDefault();
    if (stale) {setError('费率已被其他窗口修改。你的草稿已保留，请载入最新费率后重新核对。'); return;}
    let next; try {next = parsePrices(form.rates);} catch (failure) {setError(failure.message); return;}
    setSaving(true); setError('');
    try {await save(next, form.revision); changeDraft(previous => previous === draft ? null : previous);} catch (failure) {setError(failure.message);} finally {setSaving(false);}
  }
  return <section className="u-panel"><div className="u-panel-heading"><h2>模型价格配置</h2><span>USD / 百万 Token</span></div><form onSubmit={submit} aria-busy={saving}><div className="u-prices">{models.map(model => {
    const price = form.rates[model.id] || {}, tiers = [{key: 'standard', title: '普通模式', value: price}, {key: 'fast', title: 'Fast 模式', value: price.fast}];
    if (price.longContext) {tiers.push({key: 'long', title: '长上下文 · 输入 > ' + price.longContext.above.toLocaleString(), value: price.longContext.rate}); if (price.longContext.fast) tiers.push({key: 'long-fast', title: '长上下文 Fast', value: price.longContext.fast});}
    return <fieldset key={model.id} disabled={saving}><legend>{model.label}</legend><p className="u-price-origin">{price.source ? '官网默认 · ' + price.checkedAt : fields.some(([field]) => price[field] !== '' && price[field] != null) ? '自定义费率' : '未定价'}{model.fast && <span className="u-fast-status">Fast 已开启</span>}</p>{price.note && <p className="u-price-note">{price.note}</p>}{tiers.map(tier => <section className="u-rate-group" key={tier.key}><h3>{tier.title}</h3><div>{fields.map(([field, label]) => <label key={field}>{label}<input type="number" min="0" max="10000" step="any" aria-label={model.model + ' ' + (tier.key === 'standard' ? '' : tier.title + ' ') + label + '单价'} aria-invalid={Boolean(error || stale)} aria-describedby={error || stale ? 'usage-price-error' : undefined} value={tier.value?.[field] ?? ''} onChange={event => changeDraft(previous => editPrice(previous, rates, models, revision, model.id, field, event.target.value, tier.key))} /></label>)}</div></section>)}</fieldset>;
  })}</div>{!models.length && <div className="u-empty">暂无已配置模型，请先在我的连接中选择并保存模型。</div>}{(error || stale) && <p id="usage-price-error" className="u-error" role="alert">{error || '费率已被其他窗口修改。你的草稿已保留，请载入最新费率后重新核对。'}</p>}<div className="u-price-actions"><button type="submit" className="button button-primary" disabled={saving || !models.length}>{saving ? '正在保存费率' : '保存费率'}</button>{stale && <AlertDialog.Root><AlertDialog.Trigger asChild><button type="button" className="button button-secondary" disabled={saving}>载入最新费率</button></AlertDialog.Trigger><AlertDialog.Portal><AlertDialog.Overlay className="dialog-overlay" /><AlertDialog.Content className="dialog-content"><AlertDialog.Title className="dialog-title">放弃未保存费率？</AlertDialog.Title><AlertDialog.Description className="dialog-description">将放弃当前费率草稿并载入其他窗口保存的最新费率，历史请求费用不会改变。</AlertDialog.Description><div className="dialog-actions"><AlertDialog.Cancel asChild><button type="button" className="button button-secondary">保留草稿</button></AlertDialog.Cancel><AlertDialog.Action asChild><button type="button" className="button button-primary" onClick={() => {changeDraft(() => null); setError('');}}>放弃草稿并载入</button></AlertDialog.Action></div></AlertDialog.Content></AlertDialog.Portal></AlertDialog.Root>}</div></form></section>;
}

/** 详情对话框由 Radix 管理焦点和 Escape；成功状态与用量完整性独立，未知仍显示为破折号。 */
function RequestDetail({row, close, rates, opener}) {
  return <Dialog.Root open={Boolean(row)} onOpenChange={open => {if (!open) close();}}><Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content u-detail-dialog" onCloseAutoFocus={event => {event.preventDefault(); if (opener.current?.isConnected) opener.current.focus();}}><Dialog.Title className="dialog-title">请求详情</Dialog.Title><Dialog.Description className="dialog-description">仅展示脱敏状态和用量，不含提示词、回答或 Key。</Dialog.Description><Dialog.Close asChild><button type="button" className="button button-ghost icon-button u-detail-close" aria-label="关闭请求详情"><X size={18} aria-hidden="true" /></button></Dialog.Close>{row && <dl className="u-detail-grid">{[['请求 ID', row.id], ['连接 / 模型', `${row.connection} / ${row.model}`], ['请求结果', USAGE_RESULTS[row.result]], ['调用来源', USAGE_SOURCES[row.source]], ['输入 Token', number(row.input)], ['输出 Token', number(row.output)], ['缓存读取', number(row.read)], ['缓存创建', number(row.write)], ['推理 Token', number(row.reasoning)], ['上游尝试', row.attempts], ['用量质量', row.total == null ? '未上报，不按零处理' : row.attempts > 1 ? '部分尝试未知，只合计已知值' : '已上报；仅合计已知数据'], ['估算费用', `${money(usageCost(row, rates))} · 已知费率`]].map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>}</Dialog.Content></Dialog.Portal></Dialog.Root>;
}

/** 用量页保留四个页签与受控菜单筛选；移除冗余来源文案，切换和刷新仍保留父级草稿与真实结果边界。 */
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
      if (gate.current.acceptsPrices(ticket)) {pricesRevision.current = value.prices.revision; onChange(previous => ({...previous, rates: value.prices.rates || {}, revision: value.prices.revision}));}
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
    <div className="u-status-line"><div className="u-collection"><span className={view.collecting ? 'u-collect-dot' : 'u-collect-dot paused'} aria-hidden="true" /><strong>本地用量记录</strong></div><UsageButton className="u-refresh" aria-label="刷新用量记录" onClick={() => void load()}><RefreshCw size={15} aria-hidden="true" /></UsageButton></div>
    {(readError || degraded) && <div className="field-error" role="alert">{readError || degraded}</div>}{loading && <p role="status">正在读取本地用量记录…</p>}<section className="u-filters" aria-label="全局用量筛选">{filters.map(([field, label, options]) => <div className="u-filter-field" key={field}><span>{label}</span><UsageSelect label={`用量${label}`} value={view.filters[field]} options={options} onChange={value => changeFilter(field, value)} /></div>)}</section>
    <Tabs.Content value="overview">{loading || readError && !records.length ? <section className="u-panel" role="status">{loading ? '正在读取本地用量…' : '无法读取记录，请刷新重试'}</section> : <Overview rows={rows} view={view} models={models} now={now} />}</Tabs.Content>
    <Tabs.Content value="analysis"><Analysis rows={rows} view={view} update={update} /></Tabs.Content>
    <Tabs.Content value="requests"><Requests rows={rows} view={view} update={update} openDetail={openDetail} /></Tabs.Content>
    <Tabs.Content value="prices"><Prices rates={view.rates} models={configuredPriceModels(connections)} revision={view.revision} draft={view.priceDraft} changeDraft={changePriceDraft} save={savePrices} /></Tabs.Content>
    <div className="u-message" role="status" aria-live="polite">{view.message}</div>
  </Tabs.Root><RequestDetail row={detail} close={() => update({detail: null})} rates={view.rates} opener={detailOpener} /></div>;
}
