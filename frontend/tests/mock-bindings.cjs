/** 浏览器测试专用固定绑定：所有数据在内存，模型、文件、更新与插件请求均不触及用户环境。 */
function mockBindings(fixture) {
  /** 深复制合成数据，避免页面草稿修改测试后台的保存快照。 */
  function copy(value) {return JSON.parse(JSON.stringify(value));}
  let saved = copy(fixture.config), state = copy(fixture.state), listener, dirty = false, pendingUpdate, updateToken = 0;
  const calls = [], pending = new Map();
  for (const p of Object.values(saved.models)) p.apiKey = 'synthetic-fixture-secret';
  /** 仅返回脱敏的保存配置，模拟正式后端的凭据边界。 */
  function editable() {const c = copy(saved); for (const [name, p] of Object.entries(c.models)) {p.hasKey = Boolean(p.apiKey); p.apiKey = ''; p.savedName = name;} return c;}
  /** 发布真实操作形状的状态事件，旧结果只能按任务令牌完成。 */
  function emit() {if (listener) listener(copy(state)); return copy(state);}
  /** 模拟受控检查/下载，用显式测试完成函数代替联网。 */
  function update(download = false, plugin = false) {
    const mine = ++updateToken, field = plugin ? 'pluginUpdate' : 'update';
    state[field] = {...state[field], phase: download ? 'downloading' : 'checking', message: download ? '正在下载更新' : plugin ? '正在检查插件更新' : '正在检查更新'}; emit();
    return new Promise(resolve => {pendingUpdate = {resolve, mine, field, download};});
  }
  /** 取消令牌使迟到结果失效，取消后返回完整状态快照。 */
  function cancelUpdate(plugin = false) {const field = plugin ? 'pluginUpdate' : 'update'; updateToken++; state[field] = {...state[field], phase: 'cancelled', message: '已取消'}; emit(); pendingUpdate?.resolve(copy(state)); return copy(state);}
  const app = {
    /** 读取合成桌面状态，不读取真实版本或配置文件。 */
    GetState: async () => copy(state),
    /** 记录正式固定 API 路由并执行对应的隔离保存动作。 */
    API: async (route, body) => {
      calls.push({route, body}); if (window.__mock.failure === route) {window.__mock.failure = ''; throw Error('synthetic operation failed');}
      const input = body ? JSON.parse(body) : {};
      if (route === '/api/usage') return {records: copy(fixture.usageRecords || []), prices: copy(fixture.usagePrices || {revision:'', rates:{}}), now:Date.now(), skipped:0, lost:0};
      if (route === '/api/usage/prices') {fixture.usagePrices = {revision:'test-price-version',rates:copy(input.rates)}; return {prices:copy(fixture.usagePrices)};}
      if (route === '/api/probe' || route === '/api/models') return new Promise((resolve, reject) => pending.set(input.requestId, {resolve, reject, route}));
      if (route === '/api/config/save') {
        const p = copy(input.config.models[input.name]), source = p.savedName || (p.copySource ? Object.entries(saved.models).find(([, value]) => value.id === p.copySource)?.[0] : input.name), previous = saved.models[source];
        if (p.savedName !== input.name && saved.models[input.name]) throw Error('名称冲突');
        p.apiKey = p.apiKey || (p.hasKey ? previous?.apiKey || '' : '');
        if (p.savedName && p.savedName !== input.name) delete saved.models[p.savedName]; delete p.savedName; delete p.hasKey; delete p.copySource;
        saved.models[input.name] = p;
      } else if (route === '/api/config/remove') {
        const id = saved.models[input.name].id; delete saved.models[input.name];
        for (const p of saved.accessConfigs) {p.connectionIDs = p.connectionIDs.filter(value => value !== id); delete p.modelIDs[id]; if (p.defaultKey && JSON.parse(p.defaultKey)[0] === id) p.defaultKey = '';}
        if (state.relay.access?.connectionIDs.includes(id)) {state.relay = {enabled: false}; saved.lastAccess = null; emit();}
      } else if (route === '/api/access/save') {
        const index = saved.accessConfigs.findIndex(p => p.id === input.plan.id); if (index < 0) saved.accessConfigs.push(copy(input.plan)); else saved.accessConfigs[index] = copy(input.plan);
      } else if (route === '/api/access/remove') {
        saved.accessConfigs = saved.accessConfigs.filter(p => p.id !== input.id); if (state.relay.access?.id === input.id) {state.relay = {enabled: false}; saved.lastAccess = null; emit();}
      } else if (route !== '/api/config') throw Error('unexpected API route '+route);
      return {config: editable(), path: 'isolated-test-config', lastAccessCurrent: fixture.lastAccessCurrent !== false};
    },
    /** 开启仅使用已保存子集，空 ID 恢复最后启用范围。 */
    EnableAccess: async id => {calls.push({enableAccess: id}); const p = id ? saved.accessConfigs.find(p => p.id === id) : saved.lastAccess; if (!p) throw Error('没有可恢复的配置'); saved.lastAccess = copy(p); state.relay = {enabled: true, access: copy(p)}; return emit();},
    /** 停用保留最后范围，不修改正在查看或保存的配置。 */
    DisableRelay: async () => {calls.push({disableRelay: true}); state.relay.enabled = false; return emit();},
    /** 保留关闭保护测试所需的草稿状态。 */
    SetDirty: async value => {dirty = value;},
    /** 取消指定连接任务；测试完成函数不能再成功完成该请求。 */
    CancelProbe: async id => {pending.get(id)?.reject(Error('已取消')); pending.delete(id);},
    /** 模拟应用检查等待，不访问远程发布源。 */
    CheckUpdate: async () => update(),
    /** 模拟应用下载等待，不生成安装文件。 */
    DownloadUpdate: async () => update(true),
    /** 取消应用检查和下载。 */
    CancelUpdate: async () => cancelUpdate(),
    /** 保存合成自动检查偏好。 */
    SetUpdateAutoCheck: async enabled => {state.update.autoCheck = enabled; return emit();},
    /** 测试跳过当前版本。 */
    SkipUpdate: async () => {state.update.phase = 'skipped'; return emit();},
    /** 只有无草稿时测试安装动作才成功。 */
    InstallUpdate: async () => {if (dirty) throw Error('请先保存草稿'); state.update.phase = 'installing'; return emit();},
    /** 发布源动作仅记录绑定调用。 */
    OpenReleases: async () => {calls.push({openReleases: true}); return copy(state);},
    /** 插件检查也使用可取消的独立结果。 */
    CheckPluginUpdate: async () => update(false, true),
    /** 插件取消不会改动应用更新状态。 */
    CancelPluginUpdate: async () => cancelUpdate(true),
    /** 插件安装只修改合成版本状态。 */
    InstallPlugin: async () => {state.plugin.phase = 'installed'; state.plugin.installedVersion = state.plugin.bundledVersion; return emit();},
    /** 插件更新只修改合成版本状态。 */
    UpdatePlugin: async () => {state.plugin.phase = 'installed'; state.plugin.installedVersion = state.plugin.bundledVersion; state.pluginUpdate.phase = 'idle'; return emit();},
    /** 请求继续编辑，取消退出确认而不影响接入。 */
    CancelClose: async () => {state.close = {}; return emit();},
    /** 退出测试仅记录动作。 */
    ConfirmClose: async () => {calls.push({confirmClose: true});},
    /** 标记正式前端已挂载，供测试等待初始化。 */
    FrontendReady: report => {window.__mock.ready = copy(report);}
  };
  window.go = {desktop: {App: app}};
  window.runtime = {/** 注册唯一的后台事件订阅，返回取消函数。 */ EventsOn: (_event, callback) => {listener = callback; return () => {listener = undefined;};}};
  window.__mock = {
    /** 返回隔离副本以验证保存及运行作用域。 */
    snapshot: () => ({saved: copy(saved), state: copy(state), dirty, calls: copy(calls)}),
    /** 完成连接测试或模型目录请求，不调用真实 API。 */
    completeProbe: (result = {ok: true, reply: 'OK'}) => {for (const [id, task] of pending) {task.resolve(task.route === '/api/models' ? {models: [{id: 'new-local-model', contextWindow: 64000}]} : result); pending.delete(id);}},
    /** 完成当前模拟更新，取消令牌不符时不广播旧结果。 */
    completeUpdate: () => {const p = pendingUpdate; if (!p || p.mine !== updateToken) return; state[p.field] = {...state[p.field], phase: p.download ? 'downloaded' : 'available', availableVersion: '0.0.1-test', message: p.download ? '更新已下载' : '发现新版本'}; emit(); p.resolve(copy(state)); pendingUpdate = undefined;},
    /** 模拟后台关闭请求以验证 Radix 草稿确认。 */
    requestClose: () => {state.close = {phase: dirty ? 'confirm' : 'closing', message: '关闭将放弃未保存修改，并恢复原配置。'}; emit();},
    /** 仅发布合成诊断事件，验证弹窗实时刷新，不触及真实请求或配置。 */
    publishDiagnostics: items => {state.relay.recentRequests = copy(items); emit();}
  };
}
module.exports = {mockBindings};
