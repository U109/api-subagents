const fragment = location.hash.slice(1);
const key = /^[a-f0-9]{64}$/.test(fragment) ? fragment : sessionStorage.getItem('setup-token');
if (key) {
  sessionStorage.setItem('setup-token', key);
  history.replaceState(null, '', '/');
}
/** 按固定 ID 获取当前渲染的表单节点。 */
const byId = (id) => document.getElementById(id);
const types = {
  compatible: 'OpenAI 兼容 / CPA',
  responses: 'OpenAI Responses',
  anthropic: 'Claude 原生',
  gemini: 'Gemini 原生',
};
const urls = {
  compatible: 'http://127.0.0.1:8317/v1',
  responses: 'https://api.openai.com/v1',
  anthropic: 'https://api.anthropic.com/v1',
  gemini: 'https://generativelanguage.googleapis.com/v1beta',
};
/** 将配置文本转义后嵌入 HTML 属性或内容，防止模型名称等输入成为可执行标记。 */
const esc = (value) =>
  String(value ?? '').replace(
    /[&<>"']/g,
    (c) => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'})[c],
  );
let config = {version: 1, maxConcurrent: 3, models: {}};
let selected = null,
  saved = new Set(),
  busy = false,
  activeTab = 'connection';
const catalogs = new Map(),
  manual = new Set(),
  dirty = new Set();
const tabs = {connection: '连接信息', model: '模型选择', purpose: '任务分工', advanced: '高级设置'};
const reasoningLevels = {'': '服务默认', none: '不思考 · none', minimal: '极低 · minimal', low: '低 · low', medium: '中 · medium', high: '高 · high', xhigh: '超高 · xhigh'};
const tabIcons = {connection: 'link', model: 'model', purpose: 'route', advanced: 'sliders'};
const reasoningHints = {'': '由模型服务决定，不额外指定思考参数。', none: '适用于支持关闭推理的模型，优先快速生成。', minimal: '用尽量少的推理处理简单、明确的任务。', low: '适合日常修改与简单排错，优先速度和较低用量。', medium: '在响应速度与分析深度之间取得平衡。', high: '适合复杂分析与代码审查，通常需要更多时间与用量。', xhigh: '更深入地分析复杂问题，仅支持此档位的模型可用。'};
const removeDialog = byId('remove-dialog');
const menu = byId('model-menu');
let pendingRemoval = null;
let menuAnchor = null,
  menuName = null;

/** 将配置操作结果显示为顶部轻提示；错误保留到主动关闭，空消息只清除此通道。 */
function status(message, good = true) {
  window.notices.show('config', message, good ? (message.startsWith('正在') ? 'info' : 'success') : 'error');
}
/** 桌面版调用 Go 绑定，浏览器版使用会话令牌；两者共享配置校验与错误提示。 */
async function api(route, body) {
  if (window.desktopApp) {
    const data = await window.desktopApp.api(route, body);
    if (data.warning) window.notices.show('config-warning', data.warning, 'warning');
    return data;
  }
  const res = await fetch(route, {
    method: body ? 'POST' : 'GET',
    headers: {authorization: 'Bearer ' + key, 'content-type': 'application/json'},
    ...(body ? {body: JSON.stringify(body)} : {}),
  });
  const data = await res.json();
  if (!res.ok) throw Error(data.error || '请求失败');
  return data;
}
/** 根据当前草稿重建连接列表；切换前提交名称，每个连接只提供复制和删除菜单。 */
function sidebar() {
  closeMenu();
  const entries = Object.entries(config.models);
  byId('model-count').textContent = `${entries.length} 个连接`;
  byId('sidebar-count').textContent = entries.length;
  byId('models').innerHTML = entries.length
    ? entries
        .map(
          ([name, p]) => `
    <div class="model connection ${name === selected ? 'active' : ''}">
      <button class="model-select" data-name="${esc(name)}" title="${esc(name)}" aria-label="${esc(name)}" ${name === selected ? 'aria-current="true"' : ''}>
        <span class="connection-avatar" aria-hidden="true">${esc((p.model || name).slice(0, 1).toUpperCase())}</span><span class="model-copy"><strong><span class="model-name">${esc(name)}</span><span class="relay-badge connected-marker" hidden title="挟持中" aria-label="挟持中"></span></strong><small>${esc(p.model || '尚未选择模型')}</small></span>
      </button>
      <button class="model-more" data-menu="${esc(name)}" aria-label="${esc(name)} 的更多操作" title="更多操作" aria-haspopup="menu" aria-controls="model-menu" aria-expanded="false"><span aria-hidden="true">⋯</span></button>
    </div>`,
        )
        .join('')
    : '<p class="sidebar-empty">添加连接后，在这里切换。</p>';
  document.querySelectorAll('[data-name]').forEach((button) => {
    button.onclick = () => {
      if (!busy && commitName()) {
        selected = button.dataset.name;
        status('');
        render();
        window.uiShell.closeSidebar();
      }
    };
  });
  document.querySelectorAll('[data-menu]').forEach((button) => {
    button.onclick = () => openMenu(button);
    button.onkeydown = (event) => {
      if (!['ArrowDown', 'ArrowUp'].includes(event.key)) return;
      event.preventDefault();
      openMenu(button);
      if (menuName) byId(event.key === 'ArrowUp' ? 'delete-model' : 'copy-model').focus();
    };
  });
}

/** 收起顶层菜单并复原展开状态；键盘退出时将焦点还给原入口。 */
function closeMenu(restoreFocus = false) {
  const anchor = menuAnchor;
  if (menu.matches(':popover-open')) menu.hidePopover();
  anchor?.setAttribute('aria-expanded', 'false');
  menuAnchor = null;
  menuName = null;
  if (restoreFocus) anchor?.focus();
}

/** 在侧栏入口旁打开两项菜单；顶层弹出避免列表滚动裁切，并限制位置不超出窗口。 */
function openMenu(button) {
  if (busy || !commitName()) return;
  const wasOpen = menuAnchor === button && menu.matches(':popover-open');
  closeMenu();
  if (wasOpen) return;
  menuAnchor = button;
  menuName = button.dataset.menu;
  button.setAttribute('aria-expanded', 'true');
  menu.showPopover();
  const rect = button.getBoundingClientRect();
  const gap = 8;
  menu.style.left = `${Math.max(gap, Math.min(rect.right - menu.offsetWidth, innerWidth - menu.offsetWidth - gap))}px`;
  const top = rect.bottom + 5;
  menu.style.top = `${Math.max(gap, top + menu.offsetHeight <= innerHeight - gap ? top : rect.top - menu.offsetHeight - 5)}px`;
  byId('copy-model').focus();
}

/** 标记当前连接有待保存编辑，让改名和跨 Tab 编辑始终显示一致的保存状态。 */
function markChanged() {
  dirty.add(selected);
  byId('save-state').textContent = '待保存';
  byId('save-state').classList.add('dirty');
  window.dispatchEvent(new Event('config:changed'));
}

/** 更新当前连接的标题和保存状态，保持静态外壳独立于表单重绘。 */
function connectionHeading() {
  const p = config.models[selected];
  byId('connection-toolbar').hidden = !p && !window.desktopApp;
  byId('connection-heading').hidden = !p;
  byId('config-actions').hidden = !p;
  byId('save-state').textContent = p ? dirty.has(selected) ? '待保存' : p.savedName ? '已保存' : '新连接' : '尚未配置';
  byId('save-state').classList.toggle('dirty', Boolean(p && (dirty.has(selected) || !p.savedName)));
  if (!p) return;
  byId('connection-title').textContent = selected;
  byId('connection-title').title = selected;
  byId('profile-avatar').textContent = (p.model || selected).slice(0, 1).toUpperCase();
  byId('connection-subtitle').textContent = `${types[p.protocol] || p.protocol} · ${p.model || '尚未选择模型'}`;
}

/** 绘制七档直接点选项；只更新思考参数，保留其他 Tab 的输入和筛选结果。 */
function renderReasoning() {
  const effort = config.models[selected].reasoningEffort || '';
  byId('reasoning-options').innerHTML = Object.entries(reasoningLevels).map(([value, label]) => `<button type="button" class="effort-option" role="radio" aria-checked="${effort === value}" tabindex="${effort === value ? 0 : -1}" data-effort="${value}"><strong>${label.split(' · ')[0]}</strong><small>${value || 'default'}</small></button>`).join('');
  byId('reasoning-description').textContent = reasoningHints[effort] || reasoningHints[''];
}

/** 提交已知思考等级并同步草稿保护；键盘操作后将焦点恢复到重新绘制的选项。 */
function chooseReasoning(value, focus = false) {
  if (busy || !Object.hasOwn(reasoningLevels, value)) return;
  config.models[selected].reasoningEffort = value;
  markChanged();
  renderReasoning();
  if (focus) byId('reasoning-options').querySelector('[aria-checked="true"]').focus();
}

/** 按当前 Tab 生成一致的标题、图标和说明，标题文本均来自固定界面文案。 */
function sectionTitle(tab, description) {
  return `<div class="form-intro"><svg aria-hidden="true"><use href="#i-${tabIcons[tab]}"/></svg><div><h2>${tabs[tab]}</h2><p>${description}</p></div></div>`;
}

/** 切换前校验名称，非法草稿保留在输入框中并获得焦点，避免悄悄丢失用户输入。 */
function commitName() {
  const input = byId('name');
  if (!input || rename(config.models[selected], input)) return true;
  activateTab('connection');
  input.focus();
  return false;
}

/** 仅切换面板可见性，不重建表单；模型筛选、密钥及尚未保存的输入都保持原样。 */
function activateTab(name) {
  activeTab = name;
  for (const id of Object.keys(tabs)) {
    const active = id === name;
    byId('tab-' + id).setAttribute('aria-selected', String(active));
    byId('tab-' + id).tabIndex = active ? 0 : -1;
    byId('panel-' + id).hidden = !active;
  }
}
/** 生成带标签与说明的单个输入框，输入值及占位符均先转义。 */
function field(id, label, value, placeholder = '', full = false, type = 'text', hint = '') {
  return `<div class="field ${full ? 'full' : ''}"><label for="${id}">${label}</label>
    <input id="${id}" type="${type}" value="${esc(value)}" placeholder="${esc(placeholder)}" autocomplete="${type === 'password' ? 'new-password' : 'off'}">
    ${hint ? `<span class="hint">${hint}</span>` : ''}</div>`;
}
/** 校验并重命名任意连接草稿，保留 savedName 追溯原密钥；就地更新以免吞掉失焦后的点击。 */
function rename(profile, input) {
  const name = input.value.trim();
  if (
    !/^[a-z][a-z0-9_-]{0,47}$/.test(name) ||
    ['constructor', 'prototype', '__proto__'].includes(name) ||
    (name !== selected && Object.hasOwn(config.models, name)) ||
    (name !== profile.savedName && saved.has(name))
  ) {
    status(
      '调用名称需以小写字母开头，仅含小写字母、数字、下划线或横线，最多 48 个字符，且不能与其他连接重复。',
      false,
    );
    input.setAttribute('aria-invalid', 'true');
    return false;
  }
  input.removeAttribute('aria-invalid');
  input.value = name;
  if (name === selected) return true;
  // 替换键但保留侧栏顺序，原保存名称只在保存成功后更新。
  config.models = Object.fromEntries(
    Object.entries(config.models).map(([id, value]) => [id === selected ? name : id, value]),
  );
  if (catalogs.has(selected)) catalogs.set(name, catalogs.get(selected));
  if (manual.delete(selected)) manual.add(name);
  catalogs.delete(selected);
  dirty.delete(selected);
  selected = name;
  markChanged();
  // 就地更新名称，保留失焦时正在点击的表单和侧栏节点。
  const button = byId('models').querySelector('[aria-current="true"]');
  button.dataset.name = name;
  button.querySelector('.model-name').textContent = name;
  button.title = name;
  button.setAttribute('aria-label', name);
  const moreButton = button.nextElementSibling;
  moreButton.dataset.menu = name;
  moreButton.setAttribute('aria-label', `${name} 的更多操作`);
  connectionHeading();
  return true;
}
/** 更新模型列表区域的状态提示，不重建下拉框或改变当前选择。 */
function pickerHint(text, kind = '') {
  byId('model-hint').textContent = text;
  byId('model-hint').className = 'model-hint ' + kind;
}
/** 筛选可选模型；即使当前模型不在新列表中也保留原值，防止自动切换。 */
function modelOptions(query = '') {
  const p = config.models[selected],
    catalog = catalogs.get(selected);
  const entries = (catalog?.models || []).filter((item) =>
    `${item.id} ${item.name}`.toLowerCase().includes(query.toLowerCase()),
  );
  // 已保存的模型即使不在新列表中，也保留原值，绝不自动切换到别的模型。
  if (p.model && !entries.some((item) => item.id === p.model))
    entries.unshift({id: p.model, name: p.model, current: true});
  byId('model-select').innerHTML =
    '<option value="">请选择模型</option>' +
    entries
      .map(
        (item) =>
          `<option value="${esc(item.id)}">${esc(item.current ? item.id + '（当前选择）' : item.name === item.id ? item.id : item.name + ' · ' + item.id)}</option>`,
      )
      .join('');
  byId('model-select').value = p.model;
  byId('model-select').disabled = !catalog?.models.length && !p.model;
  window.selectUI.refresh();
}
/** 将当前连接的模型列表交给独立编辑器；新增和移除只标记草稿，不触发服务请求。 */
function renderRelayModels() {
  window.relayModelsUI.render(byId('relay-models'), {profile: config.models[selected], catalog: catalogs.get(selected), onChange: markChanged});
}
/** 绘制模型列表/手动输入两种模式，绑定只读拉取操作并维护连接级缓存。 */
function renderPicker() {
  const p = config.models[selected],
    isManual = manual.has(selected),
    catalog = catalogs.get(selected);
  byId('picker').innerHTML = `
    <div class="field-label"><label for="${isManual ? 'model' : 'model-select'}">默认模型</label><div class="model-mode">
      <button id="mode-list" class="text-button" ${!isManual ? 'hidden' : ''}>列表选择</button><button id="mode-manual" class="text-button" ${isManual ? 'hidden' : ''}>手动输入</button>
    </div></div><div class="model-row"><div class="model-input">${isManual ? `<input id="model" value="${esc(p.model)}" placeholder="输入服务支持的模型 ID" autocomplete="off">` : '<select id="model-select"></select>'}</div><button id="pull-models" class="secondary"><svg aria-hidden="true"><use href="#i-refresh"/></svg>刷新列表</button></div>
    ${!isManual && catalog?.models.length ? '<input id="model-filter" class="search" aria-label="筛选模型" placeholder="搜索模型名称或 ID" autocomplete="off">' : ''}
    <div id="model-hint" class="model-hint" role="status" aria-live="polite"></div>`;
  byId('mode-list').onclick = () => {
    manual.delete(selected);
    renderPicker();
  };
  byId('mode-manual').onclick = () => {
    manual.add(selected);
    renderPicker();
  };
  if (isManual)
    byId('model').oninput = (event) => {
      p.model = event.target.value;
      markChanged();
      sidebar();
      connectionHeading();
      renderRelayModels();
    };
  else {
    modelOptions();
    byId('model-select').onchange = (event) => {
      p.model = event.target.value;
      markChanged();
      sidebar();
      connectionHeading();
      renderRelayModels();
    };
    if (byId('model-filter')) byId('model-filter').oninput = (event) => modelOptions(event.target.value);
  }
  pickerHint(
    isManual
      ? '填写服务商提供的模型 ID；不支持列表接口时可直接使用此方式。'
      : catalog
        ? catalog.models.length
          ? `已获取 ${catalog.models.length} 个模型${catalog.truncated ? '（列表已限制数量）' : ''}。请选择支持工具调用的对话模型。`
          : '接口没有返回可选模型，可切换为手动输入。'
        : '填写 API 地址和 Key 后拉取列表；所选模型用于此连接。',
  );
  byId('pull-models').onclick = () =>
    action(async () => {
      pickerHint('正在获取服务提供的模型…');
      try {
        const result = await api('/api/models', {config, name: selected});
        catalogs.set(selected, result);
        manual.delete(selected);
        renderPicker();
        pickerHint(
          result.models.length
            ? `已获取 ${result.models.length} 个模型${result.truncated ? '（最多显示 1000 个）' : ''}，选择后保存配置。`
            : '接口没有返回可选模型，可切换为手动输入。',
          result.models.length ? 'success' : '',
        );
      } catch (error) {
        pickerHint(error.message, 'error');
      }
    });
  window.selectUI.enhance(byId('picker'));
  renderRelayModels();
}

/** 一次绘制四个配置面板并保留当前 Tab；表单输入同步草稿，工具栏始终提供保存和测试。 */
function render() {
  queueMicrotask(() => window.dispatchEvent(new Event('config:rendered')));
  sidebar();
  connectionHeading();
  const editor = byId('editor'),
    p = config.models[selected];
  if (!p) {
    editor.innerHTML =
      '<div class="empty"><div class="empty-icon" aria-hidden="true">＋</div><h2>连接你的第一个模型</h2><p>支持 OpenAI、Claude、Gemini 和 CPA 等兼容服务。<br>填入接口和 Key，直接从列表选择模型。</p><button class="primary" id="first">添加模型连接</button><div class="empty-steps"><span>填写连接</span><i>→</i><span>拉取模型</span><i>→</i><span>交给 Codex</span></div></div>';
    byId('first').onclick = add;
    return;
  }
  editor.innerHTML = `
    <div class="config-tabs tabs" role="tablist" aria-label="配置分类">${Object.entries(tabs)
      .map(
        ([id, label]) =>
          `<button id="tab-${id}" role="tab" aria-selected="${id === activeTab}" aria-controls="panel-${id}" tabindex="${id === activeTab ? 0 : -1}" data-tab="${id}"><svg aria-hidden="true"><use href="#i-${tabIcons[id]}"/></svg>${label}</button>`,
      )
      .join('')}</div>
    <section id="panel-connection" class="card tab-panel" role="tabpanel" aria-labelledby="tab-connection" tabindex="0">${sectionTitle('connection', '设置接口与凭据，支持 OpenAI、Claude、Gemini 与 CPA。')}<div class="grid">
      ${field('name', '连接名称', selected, '例如 reviewer', false, 'text', '用于 Codex 调用，可在这里重命名。')}
      <div class="field"><label for="protocol">接口类型</label><select id="protocol">${Object.entries(
        types,
      )
        .map(
          ([value, label]) =>
            `<option value="${value}" ${p.protocol === value ? 'selected' : ''}>${label}</option>`,
        )
        .join('')}</select></div>
      ${field('baseUrl', 'API 地址', p.baseUrl, urls[p.protocol], true, 'url', '填写 API 根地址；CPA 常用 http://127.0.0.1:8317/v1。')}
      <div class="field full"><div class="field-label"><label for="apiKey">API Key</label><span class="key-saved">${p.hasKey ? '已保存' : '尚未保存'}</span></div><div class="input-wrap key-input"><input id="apiKey" type="password" value="${esc(p.apiKey)}" placeholder="${p.hasKey ? '已保存，留空保持原 Key' : '粘贴此服务的 API Key'}" autocomplete="new-password"><button id="toggle-key" class="icon-button" type="button" aria-label="显示输入的 Key" ${p.apiKey ? '' : 'disabled'}><svg aria-hidden="true"><use href="#i-eye"/></svg></button></div><div class="hint-row"><span class="hint">留空将保留已保存的 Key。</span><button id="key-env-link" class="text-button">使用环境变量<svg aria-hidden="true"><use href="#i-arrow"/></svg></button></div></div>
    </div></section>
    <section id="panel-model" class="card tab-panel" role="tabpanel" aria-labelledby="tab-model" tabindex="0">${sectionTitle('model', '选择默认模型、挟持模型列表与思考深度。')}<div class="model-layout"><div class="model-main"><div id="picker" class="model-field"></div>
      <div class="reasoning-block"><div class="field-label"><label id="reasoning-label">思考等级</label><span class="quiet-meta">更快响应<span class="small-rule"></span>更深入思考</span></div><div id="reasoning-options" class="reasoning-options" role="radiogroup" aria-labelledby="reasoning-label"></div><p id="reasoning-description" class="hint"></p><span class="hint">具体档位需模型支持；Codex 中的手动选择优先，原生预算受最大输出长度约束。</span></div></div><section id="relay-models" class="relay-models-block" aria-labelledby="relay-models-label"></section></div><div class="default-note"><svg aria-hidden="true"><use href="#i-refresh"/></svg><span>保存后，重启 Codex 可刷新模型列表与默认思考等级。</span></div>
    </section>
    <section id="panel-purpose" class="card tab-panel" role="tabpanel" aria-labelledby="tab-purpose" tabindex="0">${sectionTitle('purpose', '告诉 Codex 这个模型擅长什么，用于插件任务委派。')}
      <div class="field"><label for="description">擅长与用途</label><textarea id="description" maxlength="300" placeholder="例如：分析后端逻辑与边界条件，适合排错和代码审查。">${esc(p.description)}</textarea><span class="hint">日常对话只需描述目标，Codex 会参考这里的用途安排任务。</span></div>
    </section>
    <section id="panel-advanced" class="card tab-panel" role="tabpanel" aria-labelledby="tab-advanced" tabindex="0">${sectionTitle('advanced', '调整响应、并发与等待时间，通常保持默认即可。')}<div class="grid">
        ${field('maxTokens', '最大输出长度', p.maxTokens ?? 4096, '4096', false, 'number')}
        ${field('maxConcurrent', '任务并发数', config.maxConcurrent, '3', false, 'number', '全局设置，所有连接共用，范围 1–8。')}
        <div class="field full"><label for="stream">响应方式</label><select id="stream"><option value="true" ${p.stream !== false ? 'selected' : ''}>流式响应（推荐）</option><option value="false" ${p.stream === false ? 'selected' : ''}>普通响应（兼容旧网关）</option></select><span class="hint">流式接收可持续更新任务进度，服务仍有数据时继续等待。</span></div>
        ${field('firstResponseTimeoutSeconds', '首个数据等待时间（秒）', p.firstResponseTimeoutSeconds ?? 180, '180', false, 'number', '10–600 秒，包含连接和等待服务开始返回数据的时间。')}
        ${field('streamIdleTimeoutSeconds', '响应中断等待时间（秒）', p.streamIdleTimeoutSeconds ?? 120, '120', false, 'number', '10–600 秒，每次收到数据后重新计时。')}
        ${field('taskTimeoutMinutes', '任务总时长上限（分钟）', p.taskTimeoutMinutes ?? 15, '15', false, 'number', '1–60 分钟，包含所有模型轮次与文件操作。')}
        ${field('apiKeyEnv', '从环境变量读取 Key（可选）', p.apiKeyEnv, '例如 MY_MODEL_API_KEY', true, 'text', '设置后优先使用环境变量；留空则使用上面保存的 Key。')}
      </div>
    </section>`;
  activateTab(activeTab);
  document.querySelectorAll('[data-tab]').forEach((button) => {
    button.onclick = () => {
      if (!busy && commitName()) activateTab(button.dataset.tab);
    };
    button.onkeydown = (event) => {
      const ids = Object.keys(tabs),
        index = ids.indexOf(button.dataset.tab);
      const next = {
        ArrowRight: ids[(index + 1) % ids.length],
        ArrowLeft: ids[(index + ids.length - 1) % ids.length],
        Home: ids[0],
        End: ids.at(-1),
      }[event.key];
      if (!next) return;
      event.preventDefault();
      if (busy || !commitName()) return;
      activateTab(next);
      byId('tab-' + next).focus();
    };
  });
  renderPicker();
  renderReasoning();
  byId('reasoning-options').onclick = event => { const button = event.target.closest('[data-effort]'); if (button) chooseReasoning(button.dataset.effort, true); };
  byId('reasoning-options').onkeydown = event => {
    const levels = Object.keys(reasoningLevels), current = levels.indexOf(p.reasoningEffort || '');
    const next = {ArrowRight: (current + 1) % levels.length, ArrowLeft: (current + levels.length - 1) % levels.length, Home: 0, End: levels.length - 1}[event.key];
    if (next === undefined) return;
    event.preventDefault();
    chooseReasoning(levels[next], true);
  };
  byId('toggle-key').onclick = () => {
    const hidden = byId('apiKey').type === 'password';
    byId('apiKey').type = hidden ? 'text' : 'password';
    byId('toggle-key').setAttribute('aria-label', hidden ? '隐藏输入的 Key' : '显示输入的 Key');
  };
  byId('key-env-link').onclick = () => { if (!busy && commitName()) { activateTab('advanced'); byId('apiKeyEnv').focus(); } };
  byId('name').onchange = (event) => rename(p, event.target);
  const numeric = [
    'maxTokens',
    'firstResponseTimeoutSeconds',
    'streamIdleTimeoutSeconds',
    'taskTimeoutMinutes',
  ];
  for (const prop of ['baseUrl', 'apiKey', 'description', 'apiKeyEnv', ...numeric]) {
    byId(prop).oninput = (event) => {
      p[prop] = numeric.includes(prop) ? Number(event.target.value) : event.target.value;
      markChanged();
      if (prop === 'apiKey') byId('toggle-key').disabled = !event.target.value;
      if (['baseUrl', 'apiKey', 'apiKeyEnv'].includes(prop)) {
        catalogs.delete(selected);
        renderPicker();
      }
    };
  }
  byId('stream').onchange = (event) => {
    p.stream = event.target.value === 'true';
    markChanged();
  };
  byId('protocol').onchange = (event) => {
    if (!commitName()) {
      byId('protocol').value = p.protocol;
      window.selectUI.refresh();
      return;
    }
    p.protocol = event.target.value;
    p.baseUrl = urls[p.protocol];
    markChanged();
    catalogs.delete(selected);
    render();
  };
  byId('maxConcurrent').oninput = (event) => {
    config.maxConcurrent = Number(event.target.value);
    markChanged();
  };
  byId('save').onclick = save;
  byId('probe').onclick = () =>
    action(async () => {
      status('正在测试连接…');
      const result = await api('/api/probe', {config, name: selected});
      status('连接成功：' + result.reply);
    });
  window.selectUI.enhance(editor);
}
/** 创建名称不重复的空白连接草稿；用户保存前不会影响已配置模型。 */
function add() {
  if (busy || !commitName()) return;
  if (Object.keys(config.models).length >= 50) return status('最多配置 50 个模型。', false);
  let index = 1;
  while (Object.hasOwn(config.models, 'worker-' + index) || saved.has('worker-' + index)) index++;
  selected = 'worker-' + index;
  activeTab = 'connection';
  config.models[selected] = {
    protocol: 'compatible',
    baseUrl: urls.compatible,
    apiKey: '',
    model: '',
    description: '',
    apiKeyEnv: '',
    maxTokens: 4096,
  };
  render();
  status('');
  window.uiShell.closeSidebar();
  byId('name').focus();
}
/** 请求期间禁用控件，结束时恢复原状态，避免迟到响应覆盖期间发生的新编辑。 */
function setBusy(value) {
  busy = value;
  if (value) window.selectUI.close();
  document.querySelectorAll('button, input, select, textarea').forEach((control) => {
    // 桌面按钮由安装和更新状态独立管理，不能被配置请求结束时的旧快照覆盖。
    if (control.closest('[data-desktop-control]') || control.matches('[data-shell]')) return;
    // 保留本来就不可用的下拉框状态；请求结束不应把空列表误启用。
    if (value) {
      control.dataset.wasDisabled = String(control.disabled);
      control.disabled = true;
    } else if ('wasDisabled' in control.dataset) {
      control.disabled = control.dataset.wasDisabled === 'true';
      delete control.dataset.wasDisabled;
    }
  });
  window.dispatchEvent(new Event('config:changed'));
}
/** 统一校验名称、锁定控件和捕获错误；定点删除或复制可跳过其他连接的名称校验。 */
async function action(fn, {checkName = true} = {}) {
  if (busy) return;
  // 保存和拉取前提交名称，不依赖浏览器是否已经发出失焦 change 事件。
  if (checkName && !commitName()) return;
  setBusy(true);
  try {
    await fn();
  } catch (error) {
    status(typeof error === 'string' ? error : error.message || '操作失败，请重试。', false);
  } finally {
    setBusy(false);
    window.dispatchEvent(new Event('config:rendered'));
  }
}
/** 保存全部草稿，并用服务端标准化且已隐藏密钥的响应刷新页面。 */
async function save() {
  await action(async () => {
    const result = await api('/api/config', {config});
    config = result.config;
    saved = new Set(Object.keys(config.models));
    dirty.clear();
    render();
    status('已保存。后续请求使用新配置；模型列表与默认思考等级在重启 Codex 后刷新。');
  });
}
/** 将目标连接的当前草稿复制并单独保存；服务端保留密钥，其他连接的草稿不受影响。 */
async function copyModel(name) {
  if (Object.keys(config.models).length >= 50) return status('最多配置 50 个模型。', false);
  await action(
    async () => {
      const result = await api('/api/config/copy', {config, name});
      config.models[result.name] = result.config.models[result.name];
      saved.add(result.name);
      if (catalogs.has(name)) catalogs.set(result.name, catalogs.get(name));
      if (manual.has(name)) manual.add(result.name);
      selected = result.name;
      activeTab = 'connection';
      render();
      status(`已复制并保存为“${selected}”，可在连接信息中重命名。其他连接的编辑仍保留在草稿中。`);
      byId('name').focus();
      window.uiShell.closeSidebar();
    },
    {checkName: false},
  );
}
/** 只持久化目标连接的删除，保留其他连接未保存的草稿；请求失败时不移除侧栏项。 */
async function removeModel(name) {
  if (!Object.hasOwn(config.models, name)) return;
  await action(
    async () => {
      // 单独持久化删除，保留其他表单草稿；请求失败时不改变本地列表。
      const original = config.models[name].savedName;
      if (original) await api('/api/config/remove', {name: original});
      delete config.models[name];
      saved.delete(original);
      dirty.delete(name);
      catalogs.delete(name);
      manual.delete(name);
      if (selected === name) {
        selected = Object.keys(config.models)[0] || null;
        render();
      } else sidebar();
      status(`已删除模型配置“${name}”。`);
    },
    {checkName: false},
  );
  if (!Object.hasOwn(config.models, name))
    (byId('models').querySelector('[aria-current="true"]') || byId('add')).focus();
}
// 使用手动 popover：保留顶层显示，但避免原生外部点击先收起、入口 click 又把菜单打开。
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
  byId('remove-description').textContent = `确定删除“${pendingRemoval}”的模型配置吗？确认后立即生效。`;
  removeDialog.returnValue = '';
  removeDialog.showModal();
};
removeDialog.onclose = () => {
  const name = pendingRemoval;
  pendingRemoval = null;
  if (removeDialog.returnValue === 'remove' && name) void removeModel(name);
};
byId('add').onclick = add;
// 桌面版关闭或更新前保留未保存草稿；浏览器入口沿用原有行为。
if (window.desktopApp) {
  /** 检查新增连接、改名和字段草稿，避免更新安装丢失用户输入。 */
  const hasDrafts = () =>
    dirty.size > 0 ||
    Object.values(config.models).some((profile) => !profile.savedName) ||
    (byId('name') && byId('name').value.trim() !== selected);
  /** 输入和保存完成后同步原生关闭保护；仅传布尔值，不传配置和 Key。 */
  const syncDrafts = () => {
    void window.desktopApp.setDirty(Boolean(hasDrafts()));
    window.dispatchEvent(new Event('config:draft'));
  };
  window.modelEditor = {
    /** 返回当前选中的调用名称，供桌面模式按钮选择已保存连接。 */
    selectedName: () => selected,
    /** 存在任何草稿或请求时先要求保存，避免开启后实际使用旧参数。 */
    ready: () => Boolean(selected && config.models[selected]?.savedName && !hasDrafts() && !busy),
  };
  document.addEventListener('input', () => queueMicrotask(syncDrafts));
  document.addEventListener('change', () => queueMicrotask(syncDrafts));
  window.addEventListener('config:rendered', syncDrafts);
  window.addEventListener('config:changed', syncDrafts);
  window.addEventListener('desktop:before-update', (event) => {
    if (hasDrafts() || busy) {
      event.preventDefault();
      status('请先保存配置并等待当前操作完成，再重启更新。', false);
    }
  });
  window.addEventListener('beforeunload', (event) => {
    if (hasDrafts()) {
      event.preventDefault();
      event.returnValue = '';
    }
  });
}
render();
void action(async () => {
  const result = await api('/api/config');
  config = result.config;
  saved = new Set(Object.keys(config.models));
  selected = Object.keys(config.models)[0] || null;
  byId('path').textContent = result.path;
  render();
  if (window.go?.desktop?.App) window.go.desktop.App.FrontendReady({ok: true, configLoaded: true, tabs: Object.keys(tabs).length, reasoningOptions: document.querySelectorAll('[data-effort]').length, reasoningValue: byId('reasoning-options')?.querySelector('[aria-checked="true"]')?.dataset.effort ?? null, expanders: document.querySelectorAll('details').length, relayModelCount: document.querySelectorAll('#relay-model-list .relay-model-item').length});
});
document.addEventListener('keydown', event => {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
    event.preventDefault();
    if (selected && !document.querySelector('dialog[open]')) void save();
  }
});
