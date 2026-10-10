const fragment = location.hash.slice(1);
const key = /^[a-f0-9]{64}$/.test(fragment) ? fragment : sessionStorage.getItem('setup-token');
if (key) {
  sessionStorage.setItem('setup-token', key);
  history.replaceState(null, '', '/');
}
/** 按固定 ID 获取当前渲染的表单节点 */
const byId = (id) => document.getElementById(id);
// 新连接默认使用 Responses；现有地址与协议不因切换控件而自动改写。
const defaultEndpoint = 'https://api.openai.com/v1';
/** 将配置文本转义后嵌入 HTML 属性或内容，防止模型名称等输入成为可执行标记 */
const esc = (value) =>
  String(value ?? '').replace(
    /[&<>"']/g,
    (c) => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'})[c],
  );
// 连接名是自由文本，无原型字典让 __proto__、constructor 等名称也能作为普通键使用
let config = {version: 1, models: Object.create(null)};
let modelOutputDefaults = Object.create(null);
let selected = null,
  saved = new Set(),
  busy = false;
const catalogs = new Map(),
  dirty = new Set();
const savedSnapshots = window.configOperations.createSnapshots();
const probeOperation = window.configOperations.createProbe({
  api,
  /** 桌面取消走固定 Go 绑定；浏览器模式仅使用请求自身的 AbortSignal。 */
  cancelDesktop: window.desktopApp ? id => window.desktopApp.cancelProbe(id) : null,
  /** 将真实测试状态同步到结果区和取消按钮，错误与取消不伪装为成功。 */
  onState(phase, message) {
    const running = ['running','cancelling'].includes(phase);
    const current = probeOwner === selected;
    byId('probe').disabled = running || busy;
    byId('cancel-probe').hidden = !running || !current;
    byId('cancel-probe').disabled = phase === 'cancelling';
    if (!current) return;
    byId('probe-result').textContent = message;
    byId('probe-result').dataset.tone = phase === 'success' ? 'success' : 'notice';
    window.notices.show('connection-test', message, phase === 'success' ? 'success' : phase === 'error' ? 'error' : 'info');
  }
});
const tabs = {connection:'连接信息',model:'模型选择',purpose:'任务分工',advanced:'高级设置'};
let page = 'detail', activeTab = 'model', overviewQuery = '', connectionsCollapsed = false, catalogToken = 0, catalogRequests = 0, probeOwner = null;
const catalogStatus = new Map();
const credentialSources = new WeakMap();
const purposeExamples = ['审查代码结构、逻辑边界与潜在问题，提出可验证的修改建议。','整理技术文档、使用说明与变更记录，保持术语一致和内容准确。','按明确规则执行批量修改，并检查结果与兼容性。'];
const reasoningLevels = {'': '服务默认', none: '不思考 · none', minimal: '最少 · minimal', low: '低 · low', medium: '中 · medium', high: '高 · high', xhigh: '超高 · xhigh', max: '最高 · max', ultra: '极高 · ultra'};
const removeDialog = byId('remove-dialog');
const menu = byId('model-menu');
let pendingRemoval = null;
let menuAnchor = null,
  menuName = null;

/** 将配置结果显示为三秒轻提示，空消息只清除此通道 */
function status(message, good = true) {
  window.notices.show('config', message, good ? (message.startsWith('正在') ? 'info' : 'success') : 'error');
}
/** 桌面版调用 Go 绑定，浏览器版使用会话令牌；两者共享配置校验与错误提示 */
async function api(route, body, options = {}) {
  if (window.desktopApp) {
    const data = await window.desktopApp.api(route, body);
    if (data.warning) window.notices.show('config-warning', data.warning, 'warning');
    return data;
  }
  const res = await fetch(route, {
    signal: options.signal,
    method: body ? 'POST' : 'GET',
    headers: {authorization: 'Bearer ' + key, 'content-type': 'application/json'},
    ...(body ? {body: JSON.stringify(body)} : {}),
  });
  const data = await res.json();
  if (!res.ok) throw Error(data.error || '请求失败');
  return data;
}
/** 根据真实连接草稿重建侧栏，选择与详情导航分开，折叠不丢失当前连接。 */
function sidebar() {
  closeMenu();
  const entries = Object.entries(config.models);
  byId('model-count').textContent = entries.length;
  byId('sidebar-count').textContent = entries.length;
  byId('models').hidden = connectionsCollapsed;
  byId('connections-toggle').setAttribute('aria-expanded',String(!connectionsCollapsed));
  byId('models').innerHTML = entries.map(([name,p]) => `<div class="model"><button type="button" class="model-select" data-name="${esc(name)}" aria-current="${name === selected}" aria-label="${esc(name)}"><span class="connection-avatar tone-${window.workbenchViews.tone(name)}">${esc([...name][0]?.toUpperCase())}</span><span class="model-copy"><span class="model-name">${esc(name)}</span><small>${esc(window.modelPickerUI.nameFor(p,p.model) || '尚未选择模型')}</small></span></button><button type="button" class="model-menu-button" data-menu="${esc(name)}" aria-label="${esc(name)} 的更多操作" aria-haspopup="menu" aria-expanded="false">${window.workbenchViews.icon('more')}</button></div>`).join('');
  byId('models').querySelectorAll('[data-name]').forEach(button => { button.onclick = () => selectConnection(button.dataset.name,true); });
  byId('models').querySelectorAll('[data-menu]').forEach(button => {
    button.onclick = () => openMenu(button);
    button.onkeydown = event => { if (!['ArrowDown','ArrowUp'].includes(event.key)) return; event.preventDefault(); openMenu(button); if (menuName) byId(event.key === 'ArrowUp' ? 'delete-model' : 'copy-model').focus(); };
  });
}
/** 收起顶层菜单并复原展开状态；键盘退出时将焦点还给原入口 */
function closeMenu(restoreFocus = false) {
  const anchor = menuAnchor;
  if (menu.matches(':popover-open')) menu.hidePopover();
  anchor?.setAttribute('aria-expanded', 'false');
  menuAnchor = null;
  menuName = null;
  if (restoreFocus) anchor?.focus();
}

/** 菜单优先贴侧栏右边缘，窄屏上下回退并夹紧视口；不改变连接选择。 */
function openMenu(button) {
  if (busy || !commitName()) return;
  const wasOpen = menuAnchor === button && menu.matches(':popover-open');
  closeMenu(); if (wasOpen) return;
  menuAnchor = button; menuName = button.dataset.menu;
  byId('model-menu-name').textContent = menuName;
  button.setAttribute('aria-expanded','true'); menu.setAttribute('aria-label',menuName + ' 的连接操作'); menu.showPopover();
  const rect = button.getBoundingClientRect(), row = button.closest('.model').getBoundingClientRect();
  const edge = byId('connection-sidebar').getBoundingClientRect().right, gap = 8;
  const width = document.documentElement.clientWidth, height = document.documentElement.clientHeight;
  const right = edge + gap + menu.offsetWidth <= width - gap;
  const below = rect.bottom + gap + menu.offsetHeight <= height - gap;
  const x = right ? edge + gap : rect.right - menu.offsetWidth;
  const y = right ? row.top : below ? rect.bottom + gap : rect.top - menu.offsetHeight - gap;
  menu.style.left = Math.max(gap,Math.min(x,width - menu.offsetWidth - gap)) + 'px';
  menu.style.top = Math.max(gap,Math.min(y,height - menu.offsetHeight - gap)) + 'px';
  menu.dataset.placement = right ? 'right' : below ? 'bottom' : 'top'; byId('copy-model').focus();
}
/** 标记当前连接有待保存编辑，让改名和跨区块编辑始终显示一致的保存状态。 */
function markChanged() {
  dirty.add(selected);
  byId('save-state').textContent = '待保存';
  byId('save-state').classList.add('dirty');
  byId('save-status-icon').setAttribute('href','#i-info');
  byId('save-status-icon').closest('.save-guidance').classList.add('dirty');
  window.dispatchEvent(new Event('config:changed'));
}

/** 只隐藏或展示正式页面，不重绘当前草稿；工具页不显示连接测试与保存底栏。 */
function syncPages() {
  byId('detail-page').hidden = page !== 'detail';
  for (const name of ['overview','plugin','help']) byId(name + '-page').hidden = page !== name;
  connectionHeading();
  if (page === 'overview') renderOverview();
}

/** 用真实配置生成总览，选择只改变当前连接，管理和保存使用独立的按钮。 */
function renderOverview() {
  byId('overview-page').innerHTML = window.workbenchViews.overview(config.models,selected,dirty,overviewQuery);
}

/** 搜索只替换卡片列表，输入框及组合输入、光标和当前连接均保持不变。 */
function filterOverview(query) {
  overviewQuery = query;
  const template = document.createElement('template');
  template.innerHTML = window.workbenchViews.overview(config.models,selected,dirty,overviewQuery);
  const grid = byId('overview-page').querySelector('.provider-grid'), next = template.content.querySelector('.provider-grid');
  if (grid && next) grid.replaceWith(next);
}

/** 导航前提交当前名称，保留所有面板输入和不同连接的草稿；无连接时仍可访问工具页。 */
function navigate(next) {
  if (!['overview','detail','plugin','help'].includes(next) || busy || !commitName()) return;
  page = next; syncPages(); window.uiShell.closeSidebar();
  byId('workspace').scrollTop = 0;
  window.dispatchEvent(new Event('config:rendered'));
}

/** 卡片选择留在总览，侧栏与管理入口进入详情；切换时使旧目录和测试反馈失效。 */
function selectConnection(name, manage = false) {
  if (!Object.hasOwn(config.models,name) || busy || !commitName()) return;
  if (selected !== name) {
    catalogToken++;
    if (catalogStatus.get(selected)?.phase === 'loading') catalogStatus.delete(selected);
    void probeOperation.cancel();
    window.notices.clear('model-remove');
    byId('probe-result').textContent = '';
    selected = name;
  } else if (!manage && page === 'overview') return;
  if (manage) page = 'detail';
  render(); window.uiShell.closeSidebar();
}

/** 按当前用途同步字数及标签匹配，不重建输入以免打断中文输入法和光标。 */
function updatePurpose() {
  const text = config.models[selected]?.description || '';
  byId('purpose-count').textContent = `${text.length} / 300`;
  byId('panel-purpose').querySelectorAll('[data-purpose]').forEach(button => {
    const applied = purposeExamples[Number(button.dataset.purpose)] === text;
    button.setAttribute('aria-pressed',String(applied));
    button.querySelector('svg').outerHTML = window.workbenchViews.icon(applied ? 'check' : 'plus');
  });
}

/** 显式测试选中的真实服务，仅用现有可取消操作；切换连接后不回显旧请求的结果。 */
async function runProbe() {
  if (busy || !commitName() || selected === null) return;
  probeOwner = selected;
  try { await probeOperation.run(config,selected); }
  catch (error) { if (probeOwner === selected) status(error.message || '连接测试失败',false); }
  finally { window.dispatchEvent(new Event('config:rendered')); }
}

/** 同步页面标题、当前连接和保存状态；总览与工具页不展示连接详情操作。 */
function connectionHeading() {
  const p = selected === null ? null : config.models[selected], detail = page === 'detail';
  byId('config-actions').hidden = !detail || !p;
  byId('connection-actions').hidden = !detail || !p;
  byId('overview-add').hidden = page !== 'overview';
  byId('profile-avatar').hidden = !detail || !p;
  byId('connection-subtitle').hidden = !(detail && p) && page !== 'help';
  byId('tool-badge').hidden = page !== 'help';
  byId('help-current-connection').disabled = !p;
  byId('connection-title').textContent = detail && p ? selected : ({overview:'模型连接',plugin:'插件管理',help:'使用帮助'})[page] || '模型连接';
  byId('connection-title').title = byId('connection-title').textContent;
  byId('breadcrumb-current').textContent = byId('connection-title').textContent;
  if (p) {
    byId('profile-avatar').textContent = [...selected][0]?.toUpperCase();
    byId('profile-avatar').className = 'profile-avatar tone-' + window.workbenchViews.tone(selected);
    byId('connection-subtitle').textContent = page === 'help' ? '同一批模型，两种使用方式。' : '管理连接凭据、模型与运行偏好';
  }
  if (page === 'help' && !p) byId('connection-subtitle').textContent = '同一批模型，两种使用方式。';
  byId('save-state').textContent = p ? dirty.has(selected) ? '当前连接有未保存的修改' : p.savedName ? '当前连接已保存' : '新连接尚未保存' : '尚未配置';
  byId('save-state').classList.toggle('dirty',Boolean(p && (dirty.has(selected) || !p.savedName)));
  byId('save-status-icon').setAttribute('href',p && (dirty.has(selected) || !p.savedName) ? '#i-info' : '#i-check');
  byId('save-status-icon').closest('.save-guidance').classList.toggle('dirty',Boolean(p && (dirty.has(selected) || !p.savedName)));
  byId('tab-model-count').textContent = p ? window.modelPickerUI.selectedModels(p).length : 0;
  for (const name of ['overview','plugin','help']) byId(name + '-entry').setAttribute('aria-current',page === name || name === 'overview' && detail ? 'page' : 'false');
}
/** 绘制九档思考等级和右侧当前值；服务默认只由第一档恢复，不重复底部说明。 */
function renderReasoning() {
  const effort = config.models[selected].reasoningEffort || '';
  byId('reasoning-options').innerHTML = Object.entries(reasoningLevels).map(([value,label]) => `<button type="button" class="effort-option" role="radio" aria-checked="${effort === value}" tabindex="${effort === value ? 0 : -1}" data-effort="${value}"><strong>${label.split(' · ')[0]}</strong><small>${value || 'default'}</small></button>`).join('');
  byId('reasoning-summary').textContent = (reasoningLevels[effort] || reasoningLevels['']).split(' · ')[0] + ' · ' + (effort || 'default');
}
/** 提交已知思考等级并同步草稿保护；键盘操作后将焦点恢复到重新绘制的选项 */
function chooseReasoning(value, focus = false) {
  if (busy || !Object.hasOwn(reasoningLevels, value)) return;
  config.models[selected].reasoningEffort = value;
  markChanged();
  renderReasoning();
  if (focus) byId('reasoning-options').querySelector('[aria-checked="true"]').focus();
}

/** 切换前提交改名；空名称或重名时保留输入并聚焦，避免覆盖其他连接 */
function commitName() {
  const input = byId('name');
  if (!input || rename(config.models[selected], input)) return true;
  revealSection('connection');
  input.focus();
  return false;
}

/** 切换配置页签而不重建表单，校验失败时让对应输入和标签可见。 */
function revealSection(name, focus = false) {
  if (!Object.hasOwn(tabs,name)) return;
  activeTab = name;
  for (const id of Object.keys(tabs)) {
    const button = byId('tab-' + id); button.setAttribute('aria-selected',String(id === name)); button.tabIndex = id === name ? 0 : -1;
    if (byId('panel-' + id)) byId('panel-' + id).hidden = id !== name;
  }
  if (focus) byId('tab-' + name).focus();
}
/** 按自由文本重命名连接，仅阻止空名称与重名；保留原密钥来源，就地更新以免吞掉点击 */
function rename(profile, input) {
  const name = input.value.trim();
  const duplicate = (name !== selected && Object.hasOwn(config.models, name)) ||
    (name !== profile.savedName && saved.has(name));
  if (!name || duplicate) {
    status(name ? '连接名称已被使用，请换一个名称' : '请填写连接名称', false);
    input.setAttribute('aria-invalid', 'true');
    return false;
  }
  input.removeAttribute('aria-invalid');
  input.value = name;
  if (name === selected) return true;
  // 替换键但保留侧栏顺序，原保存名称只在保存成功后更新
  config.models = Object.assign(Object.create(null), Object.fromEntries(
    Object.entries(config.models).map(([id, value]) => [id === selected ? name : id, value]),
  ));
  catalogToken++;
  void probeOperation.cancel();
  if (catalogs.has(selected)) catalogs.set(name, catalogs.get(selected));
  catalogs.delete(selected);
  const catalogueState = catalogStatus.get(selected);
  if (catalogueState && catalogueState.phase !== 'loading') catalogStatus.set(name,catalogueState);
  catalogStatus.delete(selected);
  dirty.delete(selected);
  selected = name;
  markChanged();
  // 就地更新名称，保留失焦时正在点击的表单和侧栏节点
  const button = byId('models').querySelector('[aria-current="true"]');
  if (button) {
    button.dataset.name = name;
    button.querySelector('.model-name').textContent = name;
    button.title = name;
    button.setAttribute('aria-label', name);
    const moreButton = button.nextElementSibling;
    moreButton.dataset.menu = name;
    moreButton.setAttribute('aria-label', `${name} 的更多操作`);
  }
  renderPicker();
  connectionHeading();
  return true;
}
/** 对接真实模型目录，按连接对象和请求令牌拒绝迟到结果，目录刷新不测试模型。 */
function renderPicker() {
  const owner = config.models[selected], name = selected;
  window.modelPickerUI.render(byId('picker'), {
    profile:owner,outputDefaults:modelOutputDefaults,catalog:catalogs.get(name),catalogStatus:catalogStatus.get(name),
    /** 保存期间禁止模型编辑；目录读取不锁住连接导航。 */
    isBusy:() => busy,
    /** 撤销只允许仍选中的原连接对象，保存或切换后旧操作不再生效。 */
    isCurrent:() => selected === name && config.models[name] === owner,
    /** 更新真实草稿，保留模型编辑器的搜索、焦点与顺序。 */
    onChange:() => { window.configOperations.applyCatalogueContexts(owner,catalogs.get(name)); dirty.add(name); markChanged(); sidebar(); connectionHeading(); },
    /** 刷新只使用固定鉴权 API；切换连接、修改凭据或删除连接使旧响应失效。 */
    onRefresh:async () => {
      if (busy || catalogStatus.get(name)?.phase === 'loading' || !commitName()) return;
      const token = ++catalogToken, requestName = selected;
      catalogRequests++;
      catalogStatus.set(requestName,{phase:'loading',message:'正在获取模型目录…'}); renderPicker();
      try {
        const result = await api('/api/models',{config,name:requestName});
        if (token !== catalogToken || selected !== requestName || config.models[requestName] !== owner) return;
        catalogs.set(requestName,result); catalogStatus.set(requestName,{phase:'ready',message:result.models.length ? '已获取 ' + result.models.length + ' 个模型' : '服务未返回模型，可手动添加'});
        if (window.configOperations.applyCatalogueContexts(owner,result)) markChanged();
      } catch (error) {
        if (token !== catalogToken || selected !== requestName || config.models[requestName] !== owner) return;
        catalogStatus.set(requestName,{phase:'error',message:'目录读取失败，已保留候选与选择'}); status(error.message || '无法读取模型目录',false);
      } finally {
        catalogRequests--;
        if (token === catalogToken && selected === requestName && config.models[requestName] === owner) renderPicker();
        window.dispatchEvent(new Event('config:rendered'));
      }
    }
  });
  if (busy) setBusy(true);
}
/** 绘制四个独立页签并绑定真实草稿；页签切换只隐藏面板，保存、凭据和目录仍走固定服务。 */
function render() {
  sidebar(); connectionHeading();
  const editor = byId('editor'), p = selected === null ? null : config.models[selected];
  byId('config-tabs').hidden = !p;
  if (!p) { editor.innerHTML = '<div class="empty"><h2>还没有连接</h2><p>添加服务连接，选择模型后保存即可使用。</p><button class="primary" id="first">添加连接</button></div>'; byId('first').onclick = add; syncPages(); window.dispatchEvent(new Event('config:rendered')); return; }
  editor.dataset.profile = selected;
  if (!credentialSources.has(p)) credentialSources.set(p,p.apiKeyEnv || '');
  editor.innerHTML = window.workbenchViews.settings(selected,p);
  renderPicker(); renderReasoning(); revealSection(activeTab);
  byId('reasoning-options').onclick = event => { const button = event.target.closest('[data-effort]'); if (button) chooseReasoning(button.dataset.effort,true); };
  byId('reasoning-options').onkeydown = event => {
    const levels = Object.keys(reasoningLevels), current = levels.indexOf(p.reasoningEffort || '');
    const next = {ArrowRight:(current + 1) % levels.length,ArrowLeft:(current + levels.length - 1) % levels.length,Home:0,End:levels.length - 1}[event.key];
    if (next !== undefined) { event.preventDefault(); chooseReasoning(levels[next],true); }
  };
  byId('toggle-key').onclick = () => { const hidden = byId('apiKey').type === 'password'; byId('apiKey').type = hidden ? 'text' : 'password'; byId('toggle-key').setAttribute('aria-label',hidden ? '隐藏输入的 Key' : '显示输入的 Key'); };
  byId('name').onchange = event => rename(p,event.target);
  byId('name').oninput = markChanged;
  byId('protocol').onchange = event => {
    p.protocol = event.target.value;
    catalogToken++; catalogs.delete(selected); catalogStatus.delete(selected);
    markChanged(); renderPicker();
  };
  const numeric = ['maxTokens','firstResponseTimeoutSeconds','streamIdleTimeoutSeconds','taskTimeoutMinutes'];
  for (const prop of ['baseUrl','apiKey','description',...numeric]) byId(prop).oninput = event => {
    p[prop] = numeric.includes(prop) ? Number(event.target.value) : event.target.value; markChanged();
    if (prop === 'apiKey') { byId('toggle-key').disabled = !event.target.value; p.apiKeyEnv = event.target.value ? '' : credentialSources.get(p); }
    if (['baseUrl','apiKey'].includes(prop)) { catalogToken++; delete p.modelOfficialContexts; catalogs.delete(selected); catalogStatus.delete(selected); renderPicker(); }
    if (prop === 'description') updatePurpose();
  };
  byId('advanced-override').onclick = () => { p.advancedOverride = !window.workbenchViews.advancedEnabled(p); byId('advanced-fields').disabled = !p.advancedOverride; byId('advanced-override').setAttribute('aria-checked',String(p.advancedOverride)); byId('override-label').textContent = p.advancedOverride ? '已开启' : '未开启'; byId('override-label').classList.toggle('enabled',p.advancedOverride); markChanged(); };
  byId('reset-advanced').onclick = () => { Object.assign(p,{advancedOverride:false,maxTokens:4096,stream:true,firstResponseTimeoutSeconds:180,streamIdleTimeoutSeconds:120,taskTimeoutMinutes:15}); markChanged(); render(); };
  editor.querySelectorAll('[name="response-mode"]').forEach(input => { input.onchange = () => { p.stream = input.value === 'stream'; markChanged(); }; });
  editor.querySelectorAll('[data-purpose]').forEach(button => { button.onclick = () => { p.description = purposeExamples[Number(button.dataset.purpose)]; byId('description').value = p.description; markChanged(); updatePurpose(); }; });
  updatePurpose(); syncPages(); window.selectUI.enhance(editor);
  byId('probe').disabled = busy || probeOperation.isRunning();
  byId('cancel-probe').hidden = !probeOperation.isRunning() || probeOwner !== selected;
  window.dispatchEvent(new Event('config:rendered'));
}
/** 创建名称不重复的空白连接草稿；用户保存前不会影响已配置模型 */
function add() {
  if (busy || !commitName()) return;
  if (Object.keys(config.models).length >= 50) return status('最多配置 50 个连接', false);
  let index = 1;
  while (Object.hasOwn(config.models, 'worker-' + index) || saved.has('worker-' + index)) index++;
  catalogToken++; void probeOperation.cancel(); window.notices.clear('model-remove');
  page = 'detail'; activeTab = 'connection'; selected = 'worker-' + index;
  config.models[selected] = {
    protocol: 'responses',
    baseUrl: defaultEndpoint,
    apiKey: '',
    model: '',
    description: '',
    stream: true,
    maxTokens: 4096,
    advancedOverride: false,
    firstResponseTimeoutSeconds: 180,
    streamIdleTimeoutSeconds: 120,
    taskTimeoutMinutes: 15,
  };
  render();
  status('');
  window.uiShell.closeSidebar();
  byId('name').focus();
}
/** 请求期间禁用控件，结束时恢复原状态，避免迟到响应覆盖期间发生的新编辑 */
function setBusy(value) {
  busy = value;
  if (value) window.selectUI.close();
  document.querySelectorAll('button, input, select, textarea').forEach((control) => {
    // 桌面按钮由安装和更新状态独立管理，不能被配置请求结束时的旧快照覆盖
    if (control.closest('[data-desktop-control]') || control.matches('[data-shell]')) return;
    // 保留本来就不可用的下拉框状态；请求结束不应把空列表误启用
    if (value) {
      if (!('wasDisabled' in control.dataset)) control.dataset.wasDisabled = String(control.disabled);
      control.disabled = true;
    } else if ('wasDisabled' in control.dataset) {
      control.disabled = control.dataset.wasDisabled === 'true';
      delete control.dataset.wasDisabled;
    }
  });
  byId('probe').disabled = busy || probeOperation.isRunning();
  window.dispatchEvent(new Event('config:changed'));
}
/** 提交当前改名、锁定控件并捕获错误；定点删除或复制不提交其他连接的名称草稿 */
async function action(fn, {checkName = true} = {}) {
  if (busy) return;
  // 保存和拉取前提交名称，不依赖浏览器是否已经发出失焦 change 事件
  if (checkName && !commitName()) return;
  setBusy(true);
  try {
    await fn();
  } catch (error) {
    status(typeof error === 'string' ? error : error.message || '操作失败，请重试', false);
  } finally {
    setBusy(false);
    window.dispatchEvent(new Event('config:rendered'));
  }
}
/** 只保存指定连接并合并服务端脱敏结果，其他连接草稿和 Key 来源不会被覆盖。 */
async function save(name = selected) {
  const previousSelection = selected;
  if (name === selected && !commitName()) return;
  if (name === previousSelection) name = selected;
  if (!Object.hasOwn(config.models,name)) return;
  const previous = config.models[name];
  await action(async () => {
    const result = await api('/api/config/save',{config,name});
    const normalized = result.config.models[name];
    if (!normalized) throw Error('保存结果缺少目标连接，请刷新后重试');
    window.modelPickerUI.transferState(previous,normalized);
    config.models[name] = normalized;
    window.notices.clear('model-remove');
    if (previous.savedName && previous.savedName !== name) { saved.delete(previous.savedName); savedSnapshots.forget(previous.savedName); }
    saved.add(name); savedSnapshots.rememberOne(name,normalized); dirty.delete(name);
    if (selected === name) render(); else { sidebar(); connectionHeading(); if (page === 'overview') renderOverview(); }
    status('当前连接已保存');
  },{checkName:false});
}
/** 将目标连接的当前草稿复制并单独保存；服务端保留密钥，其他连接的草稿不受影响 */
async function copyModel(name) {
  if (Object.keys(config.models).length >= 50) return status('最多配置 50 个连接', false);
  await action(
    async () => {
      const result = await api('/api/config/copy', {config, name});
      config.models[result.name] = result.config.models[result.name];
      saved.add(result.name);
      savedSnapshots.rememberOne(result.name, result.config.models[result.name]);
      if (catalogs.has(name)) catalogs.set(result.name, catalogs.get(name));
      catalogToken++; void probeOperation.cancel(); window.notices.clear('model-remove');
      selected = result.name;
      page = 'detail'; activeTab = 'connection';
      render();
      status('连接已复制，可在连接信息中重命名');
      byId('name').focus();
      window.uiShell.closeSidebar();
    },
    {checkName: false},
  );
}
/** 只持久化目标连接的删除，保留其他连接未保存的草稿；请求失败时不移除侧栏项 */
async function removeModel(name) {
  if (!Object.hasOwn(config.models, name)) return;
  await action(
    async () => {
      // 单独持久化删除，保留其他表单草稿；请求失败时不改变本地列表
      const original = config.models[name].savedName;
      if (original) await api('/api/config/remove', {name: original});
      delete config.models[name];
      saved.delete(original);
      savedSnapshots.forget(original);
      dirty.delete(name);
      catalogs.delete(name);
      if (selected === name) {
        catalogToken++; void probeOperation.cancel(); window.notices.clear('model-remove');
        selected = Object.keys(config.models)[0] || null;
        render();
      } else { sidebar(); if (page === 'overview') renderOverview(); }
      status('连接已删除');
    },
    {checkName: false},
  );
  if (!Object.hasOwn(config.models, name))
    (byId('models').querySelector('[aria-current="true"]') || byId('add')).focus();
}
// 使用手动 popover：保留顶层显示，但避免原生外部点击先收起、入口 click 又把菜单打开
document.addEventListener('pointerdown', (event) => {
  if (!menu.contains(event.target) && !menuAnchor?.contains(event.target)) closeMenu();
});
menu.addEventListener('toggle', (event) => {
  if (event.newState === 'closed' && !menu.matches(':popover-open')) closeMenu();
});
menu.onkeydown = (event) => {
  if (event.key === 'Escape' || event.key === 'Tab') {
    if (event.key === 'Escape') event.preventDefault();
    closeMenu(true);
  } else if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
    event.preventDefault();
    const first = byId('copy-model');
    (event.key === 'Home' || (event.key !== 'End' && document.activeElement !== first)
      ? first
      : byId('delete-model')
    ).focus();
  }
};
window.addEventListener('resize', () => closeMenu());
document.addEventListener(
  'scroll',
  (event) => {
    if (!menu.contains(event.target)) closeMenu();
  },
  true,
);
byId('copy-model').onclick = () => {
  const name = menuName;
  closeMenu();
  if (name) void copyModel(name);
};
byId('delete-model').onclick = () => {
  pendingRemoval = menuName;
  closeMenu(true);
  if (!pendingRemoval) return;
  byId('remove-description').textContent = `确定删除“${pendingRemoval}”的连接配置吗？确认后立即生效`;
  removeDialog.returnValue = '';
  removeDialog.showModal();
};
removeDialog.onclose = () => {
  const name = pendingRemoval;
  pendingRemoval = null;
  if (removeDialog.returnValue === 'remove' && name) void removeModel(name);
};
byId('add').onclick = add;
byId('overview-add').onclick = add;
byId('save').onclick = () => save();
byId('probe').onclick = runProbe;
byId('overview-entry').onclick = () => navigate('overview');
byId('home-breadcrumb').onclick = () => navigate('overview');
byId('plugin-entry').onclick = () => navigate('plugin');
byId('help-entry').onclick = () => navigate('help');
byId('connections-toggle').onclick = () => { connectionsCollapsed = !connectionsCollapsed; sidebar(); };
byId('copy-current').onclick = () => { if (selected !== null) void copyModel(selected); };
byId('delete-current').onclick = () => {
  if (busy || selected === null || !commitName()) return;
  pendingRemoval = selected;
  byId('remove-description').textContent = `确定删除“${pendingRemoval}”的连接配置吗？确认后立即生效`;
  removeDialog.returnValue = ''; removeDialog.showModal();
};
byId('overview-page').onclick = event => {
  const button = event.target.closest('button'); if (!button) return;
  if (button.hasAttribute('data-select-connection')) selectConnection(button.dataset.selectConnection);
  else if (button.hasAttribute('data-manage-connection')) selectConnection(button.dataset.manageConnection,true);
  else if (button.hasAttribute('data-save-connection')) void save(button.dataset.saveConnection);
  else if (button.hasAttribute('data-add-connection')) add();
};
byId('overview-page').oninput = event => { if (event.target.id === 'connection-search') filterOverview(event.target.value); };
byId('help-current-connection').onclick = () => navigate('detail');
byId('help-plugin-entry').onclick = () => navigate('plugin');
byId('plugin-help-entry').onclick = () => navigate('help');
byId('config-tabs').onclick = event => {
  const button = event.target.closest('[data-config-tab]'); if (button && !busy) revealSection(button.dataset.configTab,true);
};
byId('config-tabs').onkeydown = event => {
  if (busy || !['ArrowLeft','ArrowRight','Home','End'].includes(event.key)) return;
  const ids = Object.keys(tabs), index = ids.indexOf(activeTab);
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? ids.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + ids.length) % ids.length;
  event.preventDefault(); revealSection(ids[next],true);
};
byId('cancel-probe').onclick = () => { void probeOperation.cancel(); };
byId('discard-current').onclick = () => { if (!busy && selected) byId('discard-dialog').showModal(); };
byId('confirm-discard-current').onclick = () => {
  if (busy || !selected) return;
  try {
    const previous = selected;
    const restored = savedSnapshots.restore(config, selected);
    config = restored.config; selected = restored.selected;
    dirty.delete(previous); catalogs.delete(previous);
    byId('discard-dialog').close(); render(); status('已放弃当前连接的修改，其他连接草稿保留');
  } catch (error) { status(error.message, false); }
};
/** 浏览器关闭和桌面安装共用草稿判断，包含尚未失焦提交的连接名。 */
function hasDrafts() {
  return dirty.size > 0 || Object.values(config.models).some(profile => !profile.savedName) || Boolean(byId('name') && byId('name').value.trim() !== selected);
}
// 桌面版关闭或更新前保留未保存草稿；浏览器入口沿用原有行为
if (window.desktopApp) {
  /** 输入和保存完成后同步原生关闭保护；仅传布尔值，不传配置和 Key */
  const syncDrafts = () => {
    void window.desktopApp.setDirty(Boolean(hasDrafts()));
    window.dispatchEvent(new Event('config:draft'));
  };
  window.modelEditor = {
    /** 返回当前选中的连接名称，供桌面模式按钮选择已保存连接 */
    selectedName: () => selected,
    /** 仅当前连接保存完成且没有请求时可接入；其他连接草稿仍由退出和安装保护。 */
    ready: () => Boolean(selected && config.models[selected]?.savedName === selected && !dirty.has(selected) && !busy && !probeOperation.isRunning() && catalogRequests === 0 && (!byId('name') || byId('name').value.trim() === selected)),
    /** 暴露草稿布尔状态给更新弹窗，不传配置、地址或密钥。 */
    hasDrafts,
  };
  document.addEventListener('input', () => queueMicrotask(syncDrafts));
  document.addEventListener('change', () => queueMicrotask(syncDrafts));
  window.addEventListener('config:rendered', syncDrafts);
  window.addEventListener('config:changed', syncDrafts);
  window.addEventListener('desktop:before-update', (event) => {
    if (hasDrafts() || busy || probeOperation.isRunning() || catalogRequests > 0) {
      event.preventDefault();
      status('请先保存配置，等待操作完成', false);
    }
  });
  // 窗口退出统一由 Go 生命周期与页面草稿弹窗处理，避免放弃草稿后又弹出 WebView 原生确认
} else {
  window.addEventListener('beforeunload', event => { if (hasDrafts() || busy) { event.preventDefault(); event.returnValue = ''; } });
}
render();
void action(async () => {
  const result = await api('/api/config');
  modelOutputDefaults = result.modelOutputDefaults || Object.create(null);
  config = {...result.config, models: Object.assign(Object.create(null), result.config.models)};
  saved = new Set(Object.keys(config.models));
  savedSnapshots.remember(config);
  selected = Object.keys(config.models)[0] || null;
  byId('path').textContent = result.path;
  render();
  if (window.go?.desktop?.App) window.go.desktop.App.FrontendReady({ok: true, configLoaded: true, tabs: document.querySelectorAll('[role="tab"]').length, reasoningOptions: document.querySelectorAll('[data-effort]').length, reasoningValue: byId('reasoning-options')?.querySelector('[aria-checked="true"]')?.dataset.effort ?? null, expanders: document.querySelectorAll('details').length, relayModelCount: Number(byId('picker')?.dataset.selectedCount || 0), updateBindings: ['CancelUpdate','SkipUpdate','SetUpdateAutoCheck','CancelPluginUpdate'].every(name => typeof window.go.desktop.App[name] === 'function')});
});
document.addEventListener('keydown', event => {
  if (event.key === '/' && !event.ctrlKey && !event.metaKey && !event.altKey && !event.isComposing && page === 'detail' && activeTab === 'model' && !document.querySelector('dialog[open]') && !event.target.closest('input,textarea,select,[contenteditable="true"]')) {
    event.preventDefault(); byId('model-search')?.focus(); return;
  }
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
    event.preventDefault();
    if (selected && !document.querySelector('dialog[open]')) void save();
  }
});
