import {useState, useRef, useEffect} from 'react';
import {clone, makePlan, validatePlan, isDirty, makeSnapshot, reorderModels, deleteConnection} from './domain.mjs';
import {emptyStore, decodeConfig, encodeConnection, mergeSaved, displaySnapshot, mergeCatalog, sameCatalogSource} from './adapter.mjs';
import {api, desktopAction} from './transport.mjs';

/** 将真实更新状态映射为原型的等待/成功/失败变体；所有版本与结果均来自 Go。 */
export function updateJob(state) {
  const phase = state?.phase || 'idle';
  return {phase: ['checking', 'downloading', 'installing'].includes(phase) ? 'loading' : phase === 'latest' ? 'current' : phase, label: state?.message || '', operation: phase};
}

/** 管理工作区和草稿边界；extraDirty 将费率等外部草稿纳入退出保护，组件不接触文件或任意命令。 */
export function useWorkbench({extraDirty = false} = {}) {
  const [store, setStore] = useState(emptyStore), [backend, setBackend] = useState({}), [loading, setLoading] = useState(true), [loadError, setLoadError] = useState(''), [busy, setBusy] = useState(false);
  const [page, setPage] = useState('plans'), [connectionID, setConnectionID] = useState(''), [tab, setTab] = useState('info'), [modal, setModal] = useState(null), [confirm, setConfirm] = useState(null), [toast, setToast] = useState(''), [mobileNav, setMobileNav] = useState(false), [jobs, setJobs] = useState({});
  const [pending, setPending] = useState(null);
  const opener = useRef(null), toastTimer = useRef(null), current = useRef(store), desktop = useRef(backend), locked = useRef(false), requests = useRef({}), epochs = useRef({}), sources = useRef(Object.create(null));
  // 草稿引用只由 changeStore 更新，渲染中的旧快照不能回退事件刚写入的最新输入。
  desktop.current = backend;

  /** 同步更新草稿；来源变化只递增数字代次，使旧测试失效而不把 API Key 复制进任务状态。 */
  function changeStore(update) {
    const previous = current.current, next = typeof update === 'function' ? update(previous) : update;
    for (const [id, connection] of Object.entries(next.connections)) {
      if (!sameCatalogSource(previous.connections[id], connection)) sources.current[id] = (sources.current[id] || 0) + 1;
    }
    current.current = next; setStore(next);
  }
  /** 简短反馈仅用于成功；操作失败另外展示在当前表单旁，不只依赖 toast。 */
  function notify(message) {setToast(message); clearTimeout(toastTimer.current); toastTimer.current = setTimeout(() => setToast(''), 4500);}
  /** 状态事件只更新运行开关和后台操作；不会把正在查看的配置当作运行对象。 */
  function acceptState(value) {
    desktop.current = value; setBackend(value);
    if (value.relay) changeStore(prev => ({...prev, enabled: Boolean(value.relay.enabled)}));
  }
  /** 重试初始化只读配置，桌面恢复后的开关状态由后台提供，绝不自动启用任何模型。 */
  async function load() {
    setLoading(true); setLoadError('');
    try {
      const [result, state] = await Promise.all([api('/api/config'), window.desktopApp ? desktopAction('getState') : Promise.resolve({})]);
      const next = decodeConfig(result.config);
      next.active = displaySnapshot(state.relay?.access || result.config.lastAccess, next.savedConnections);
	  next.activeCurrent = result.lastAccessCurrent !== false;
      next.enabled = Boolean(state.relay?.enabled);
      changeStore(next); acceptState(state);
    } catch (error) {setLoadError(error.message);} finally {setLoading(false);}
  }
  useEffect(() => {
    let live = true;
    const unsubscribe = window.desktopApp?.onState(value => {if (live) acceptState(value);});
    void load();
    return () => {live = false; if (typeof unsubscribe === 'function') unsubscribe(); clearTimeout(toastTimer.current); Object.values(requests.current).forEach(r => r.abort());};
  }, []);
  const dirty = extraDirty || Object.values(store.connections).some(c => isDirty(c, store.savedConnections[c.id])) || store.plans.some(p => isDirty(p, store.savedPlans[p.id]));
  useEffect(() => {window.desktopApp?.setDirty(dirty);}, [dirty]);
  useEffect(() => {
    /** 浏览器关页同样保护草稿，桌面原生关闭由 Go 的确认状态接管。 */
    function beforeUnload(event) {if (dirty && !window.desktopApp) {event.preventDefault(); event.returnValue = '';}}
    window.addEventListener('beforeunload', beforeUnload); return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [dirty]);
  useEffect(() => {
    const state = backend.close;
    if (state?.phase === 'confirm') ask({kind: 'close', title: '放弃未保存修改并退出？', description: state.message, action: '放弃并退出', run: () => desktopAction('confirmClose')});
  }, [backend.close?.phase]);

  /** 写入与启用串行执行；kind 和 target 提供准确等待反馈，重入无效，失败保留草稿及目标错误。 */
  async function operation(action, target, kind = 'operation') {
    if (locked.current) return false;
    locked.current = true; setBusy(true); setPending({target, kind}); changeStore(prev => ({...prev, actionError: null}));
    try {await action(); return true;} catch (error) {changeStore(prev => ({...prev, actionError: {target, message: error.message}})); return false;} finally {locked.current = false; setBusy(false); setPending(null);}
  }
  /** 打开模态时记录触发点，由 Radix 在关闭后恢复焦点。 */
  function openModal(value) {opener.current = document.activeElement; setModal(value); if (value.type === 'update' && desktop.current.update?.autoCheck !== false && !['checking', 'downloading', 'installing'].includes(desktop.current.update?.phase)) void updateAction('checkUpdate');}
  /** 所有破坏性或放弃草稿操作必须先确认。 */
  function ask(data) {opener.current = document.activeElement; setConfirm(data);}
  /** 取消退出确认保留网关；其他弹窗只关闭本地视图。 */
  function closeConfirm() {if (confirm?.kind === 'close') void desktopAction('cancelClose'); setConfirm(null);}
  /** 页面导航保留所有草稿，不改变当前接入范围。 */
  function navigate(value) {setPage(value); setMobileNav(false);}
  /** 进入连接详情只改变查看对象，不修改配置成员和模型选择。 */
  function openConnection(id) {setConnectionID(id); setTab('info'); navigate('connections');}
  /** 编辑只修改当前配置独立草稿，清除已解决的动作错误。 */
  function editPlan(update) {changeStore(prev => ({...prev, actionError: null, plans: prev.plans.map(p => p.id === prev.selected ? update(clone(p)) : p)}));}
  /** 连接草稿与保存快照隔离，Key 仅留在未提交的内存输入中。 */
  function editConnection(update) {changeStore(prev => ({...prev, actionError: null, connections: {...prev.connections, [connectionID]: update(clone(prev.connections[connectionID]))}}));}
  /** 先保存提交副本再明确启用；等待期间的新编辑保留为草稿，启用只使用后台已保存范围。 */
  async function commitPlan(enable) {
    const id = current.current.selected;
    const draft = current.current.plans.find(plan => plan.id === id);
    if (!draft) return false;
    const validation = validatePlan(draft, current.current.savedConnections, current.current.connections);
    if (validation) {ask({kind: 'notice', title: enable ? '暂时无法启用配置' : '暂时无法保存配置', description: validation}); return false;}
    return operation(async () => {
      const plan = clone(current.current.plans.find(p => p.id === id)), error = validatePlan(plan, current.current.savedConnections, current.current.connections);
      if (error) throw new Error(error);
      if (isDirty(plan, current.current.savedPlans[id])) {
        const result = await api('/api/access/save', {plan}); changeStore(prev => mergeSaved(prev, result.config, {planID: id, submittedPlan: plan}));
      }
      if (enable) {
        const state = await desktopAction('enableAccess', id);
        changeStore(prev => ({...prev, active: makeSnapshot(prev.savedPlans[id], prev.savedConnections), activeCurrent: true, enabled: Boolean(state.relay.enabled)})); acceptState(state);
      }
      notify(enable ? '接入配置已启用' : '配置已保存');
    }, id, enable ? 'plan-enable' : 'plan-save');
  }
  /** 总开关恢复最后生效范围，不依据选中配置或其新的保存内容。 */
  async function toggleAccess(enabled) {return operation(async () => {
    if (enabled && !current.current.active) throw new Error('先选择配置，点击「启用此配置」。');
    const state = await desktopAction(enabled ? 'enableAccess' : 'disableRelay', ...(enabled ? [''] : [])); acceptState(state);
  }, current.current.selected);}
  /** 还原只影响当前配置，新建草稿被移除时仍保留其他对象。 */
  function restorePlan() {ask({title: '还原当前配置？', description: '仅放弃此配置的未保存修改，其他配置和当前接入不变。', action: '还原配置', run: () => changeStore(prev => {const next = clone(prev); if (next.savedPlans[next.selected]) next.plans = next.plans.map(p => p.id === next.selected ? clone(next.savedPlans[p.id]) : p); else {next.plans = next.plans.filter(p => p.id !== next.selected); next.selected = next.plans[0]?.id || '';} next.actionError = null; return next;})});}
  /** 复制配置仅生成独立草稿，不自动保存或运行。 */
  function copyPlan() {changeStore(prev => {const p = clone(prev.plans.find(p => p.id === prev.selected)); p.id = crypto.randomUUID(); p.name = `${p.name} 副本`.slice(0, 60); return {...prev, selected: p.id, plans: [p, ...prev.plans]};});}
  /** 删除运行配置由后端先安全恢复，失败时保留对象和运行状态。 */
  function removePlan() {const p = current.current.plans.find(p => p.id === current.current.selected); ask({title: `删除「${p.name}」？`, description: current.current.active?.id === p.id ? '删除将关闭主对话接入，不会自动切换到其他配置。' : '仅删除此配置，不会删除连接或其他配置。', action: '删除配置', run: () => operation(async () => {
    let config;
    if (current.current.savedPlans[p.id]) config = (await api('/api/access/remove', {id: p.id})).config;
    if (config) changeStore(prev => mergeSaved(prev, config, {removePlanID: p.id})); else changeStore(prev => ({...prev, plans: prev.plans.filter(item => item.id !== p.id), selected: prev.plans.find(item => item.id !== p.id)?.id || ''}));
    if (window.desktopApp) acceptState(await desktopAction('getState'));
  }, p.id)});}
  /** 新建配置为空草稿，必须由用户明确选择连接及模型。 */
  function createPlan(name) {const p = makePlan(crypto.randomUUID(), name, [], current.current.savedConnections); changeStore(prev => ({...prev, plans: [p, ...prev.plans], selected: p.id})); setModal(null); navigate('plans');}
  /** 新建连接不注入示例模型或密钥，接口协议来自新建表单。 */
  function createConnection(name, url, protocol = 'responses') {const c = {id: crypto.randomUUID(), name, url, protocol, apiKey: '', apiKeyEnv: '', hasKey: false, description: '', models: [], defaultID: '', effort: 'default', override: false, maxTokens: 4096, firstTimeout: 180, idleTimeout: 120, taskTimeout: 15, response: 'stream'}; changeStore(prev => ({...prev, connections: {...prev.connections, [c.id]: c}})); setModal(null); openConnection(c.id);}
  /** 复制只创建未保存草稿；脱敏凭据以来源引用追溯，保存时复制，不重命名或覆盖原连接。 */
  function copyConnection(id) {
    const source = current.current.connections[id], copy = clone(source);
    copy.id = crypto.randomUUID(); copy.name = `${source.name} 副本`; copy.copySource = source.savedName ? source.id : source.copySource; delete copy.savedName;
    changeStore(prev => ({...prev, connections: {...prev.connections, [copy.id]: copy}})); openConnection(copy.id);
  }
  /** 删除连接先确认配置引用影响，再由后端恢复运行入口并清理所有引用。 */
  function removeConnection(id) {const c = current.current.connections[id], count = current.current.plans.filter(p => p.connectionIDs.includes(id)).length; ask({title: `删除连接「${c.name}」？`, description: `将移除此连接及 ${count} 个配置中的引用。${current.current.active?.connectionIDs.includes(id) ? '确认后关闭主对话接入，不会自动切换范围。' : '当前接入不变。'}`, action: '删除连接', run: () => operation(async () => {
    cancel(`test-${id}`); cancel(`catalog-${id}`);
    if (c.savedName) {const result = await api('/api/config/remove', {name: c.savedName}); changeStore(prev => mergeSaved(prev, result.config, {removeConnectionID: id}));} else changeStore(prev => deleteConnection(prev, id));
    if (window.desktopApp) acceptState(await desktopAction('getState'));
    if (connectionID === id) navigate('connections');
  }, id)});}
  /** 定点保存提交时的连接；保留等待期间的新输入，清除已提交 Key，不扩大配置或运行范围。 */
  async function commitConnection() {return operation(async () => {
    const c = clone(current.current.connections[connectionID]);
    const result = await api('/api/config/save', {name: c.name, config: {version: 1, models: {[c.name]: encodeConnection(c)}}});
    changeStore(prev => mergeSaved(prev, result.config, {connectionID, submittedConnection: c})); notify(result.warning || '连接已保存');
  }, connectionID, 'connection-save');}
  /** 还原只丢弃当前连接草稿，新建对象无保存副本时返回连接目录。 */
  function restoreConnection() {ask({title: '还原连接修改？', description: '恢复此连接上次保存的内容，其他连接与配置保持不变。', action: '还原连接', run: () => {changeStore(prev => {const next = clone(prev); if (next.savedConnections[connectionID]) next.connections[connectionID] = clone(next.savedConnections[connectionID]); else delete next.connections[connectionID]; next.actionError = null; return next;}); if (!current.current.connections[connectionID]) navigate('connections');}});}
  /** 拖动和键盘排序只重排可见槽位，过滤隐藏项及已保存目录不动。 */
  function moveModel(id, targetID, visibleIDs) {editConnection(c => ({...c, models: reorderModels(c.models, id, targetID, visibleIDs)}));}
  /** 高级重置保持凭据和模型设置，并等待用户明确保存。 */
  function resetAdvanced() {ask({title: '恢复高级设置默认值？', description: '仅还原运行参数，修改仍需保存连接。', action: '恢复默认', run: () => editConnection(c => ({...c, override: false, maxTokens: 4096, firstTimeout: 180, idleTimeout: 120, taskTimeout: 15, response: 'stream'}))});}
  /** 执行可取消的真实请求并记录耗时；任务仅保存来源代次，身份或凭据改变后不合入旧目录。 */
  async function start(key, label) {
    if (requests.current[key]) return;
    const c = clone(current.current.connections[connectionID]), requestId = crypto.randomUUID(), abort = new AbortController();
    const source = sources.current[c.id] || 0, started = performance.now();
    requests.current[key] = abort; epochs.current[key] = requestId; setJobs(prev => ({...prev, [key]: {phase: 'loading', label, source}}));
    try {
      const catalog = key.startsWith('catalog-'), result = await api(catalog ? '/api/models' : '/api/probe', {name: c.name, requestId, config: {version: 1, models: {[c.name]: encodeConnection(c)}}}, abort.signal);
      if (epochs.current[key] !== requestId) return;
      if (!sameCatalogSource(current.current.connections[c.id], c)) throw new Error(catalog ? '连接已修改，请重新刷新模型。' : '连接已修改，请重新测试。');
      if (catalog) changeStore(prev => ({...prev, connections: {...prev.connections, [c.id]: mergeCatalog(prev.connections[c.id], result)}}));
      setJobs(prev => ({...prev, [key]: {phase: 'success', source, elapsedMS: Math.round(performance.now() - started), label: catalog ? `已读取 ${result.models.length} 个模型${result.truncated ? ' · 目录已截断' : ''}` : `连接测试成功${result.reply ? ' · ' + result.reply : ''}`}}));
    } catch (error) {if (epochs.current[key] === requestId) setJobs(prev => ({...prev, [key]: {phase: 'error', source, label: error.message}}));} finally {if (epochs.current[key] === requestId) delete requests.current[key];}
  }
  /** 取消同步撤销请求身份，迟到的成功响应不会覆盖取消提示；桌面绑定同时中止上游请求。 */
  function cancel(key) {const id = epochs.current[key]; requests.current[key]?.abort(); delete requests.current[key]; epochs.current[key] = ''; if (id && window.desktopApp) void desktopAction('cancelProbe', id).catch(() => {}); setJobs(prev => ({...prev, [key]: {...prev[key], phase: 'cancelled', label: '已取消'}}));}
  /** 更新与插件操作完全使用真实 Go 状态，前端仅提前展示等待，不用定时器模拟结果。 */
  async function updateAction(name, plugin = false, arg) {
    const field = plugin ? 'pluginUpdate' : 'update';
    try {acceptState(await desktopAction(name, ...(arg === undefined ? [] : [arg])));} catch (error) {setBackend(prev => ({...prev, [field]: {...prev[field], phase: 'error', message: error.message}}));}
  }
  const allJobs = {...jobs, update: updateJob(backend.update)};
  // 保留进行中的取消入口；来源改变后不展示旧测试结论，目录错误仍留在查询操作旁。
  for (const [key, job] of Object.entries(jobs)) {
    if (!key.startsWith('test-') || job.phase === 'loading') continue;
    const id = key.slice(5);
    if (!store.connections[id] || job.source !== (sources.current[id] || 0)) delete allJobs[key];
  }
  return {store, backend, loading, loadError, load, busy, pending, page, connectionID, tab, setTab, modal, setModal, confirm, closeConfirm, toast, setToast, mobileNav, setMobileNav, opener, jobs: allJobs, dirty, openModal, navigate, openConnection, editPlan, editConnection, commitPlan, toggleAccess, restorePlan, copyPlan, removePlan, createPlan, createConnection, copyConnection, removeConnection, commitConnection, restoreConnection, moveModel, resetAdvanced, start, cancel, updateAction, select: id => changeStore(prev => ({...prev, selected: id, actionError: null}))};
}
