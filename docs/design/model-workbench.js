// 本设计稿只在内存中模拟交互，不读取真实配置、不调用接口，也不保存示例 Key。
const profiles = [
  {id: 1, name: 'daily-coder', model: 'deepseek-v4-pro', vendor: 'DeepSeek', subtitle: '编码与日常任务', avatar: 'D', color: '', protocol: 'compatible', effort: 'low', endpoint: 'https://api.example.com/v1'},
  {id: 2, name: 'code-review', model: 'claude-sonnet-4-6', vendor: 'Claude', subtitle: '代码审查与复杂排错', avatar: 'C', color: 'claude', protocol: 'anthropic', effort: 'high', endpoint: 'https://api.example.com/v1'},
  {id: 3, name: 'quick-task', model: 'gemini-3-flash', vendor: 'Gemini', subtitle: '批量注释与文档整理', avatar: 'G', color: 'gemini', protocol: 'gemini', effort: '', endpoint: 'https://api.example.com/v1beta'},
];
const levels = [
  ['', '服务默认', 'default', '由模型服务决定，不额外指定思考参数。'],
  ['none', '不思考', 'none', '适用于支持关闭推理的模型，优先快速生成。'],
  ['minimal', '极低', 'minimal', '用尽量少的推理处理简单、明确的任务。'],
  ['low', '低', 'low', '适合日常修改与简单排错，优先速度和较低用量。'],
  ['medium', '中', 'medium', '在响应速度与分析深度之间取得平衡。'],
  ['high', '高', 'high', '适合复杂分析与代码审查，通常需要更多时间与用量。'],
  ['xhigh', '超高', 'xhigh', '更深入地分析复杂问题，仅支持此档位的模型可用。'],
];
let selected = profiles[0], activeTab = 'connection', relay = null, menuTarget = null, toastTimer;
const dirty = new Set();
const narrowSidebar = matchMedia('(max-width: 700px)');

/** 获取设计稿中的固定节点，所有操作均局限于当前页面。 */
function el(id) { return document.getElementById(id); }
/** 转义用户在原型中编辑的内容，防止名称成为可执行 HTML。 */
function escapeHTML(value) { return String(value).replace(/[&<>"']/g, char => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[char])); }
/** 同步桌面收起与窄屏抽屉状态；隐藏侧栏不可聚焦，抽屉打开时禁用背后的配置区。 */
function syncSidebarState() {
  const compact = narrowSidebar.matches;
  const expanded = compact ? el('workbench').classList.contains('sidebar-open') : !el('workbench').classList.contains('sidebar-collapsed');
  const label = expanded ? (compact ? '关闭侧栏' : '收起侧栏') : '展开侧栏';
  el('sidebar-toggle').setAttribute('aria-expanded', String(expanded));
  el('sidebar-toggle').setAttribute('aria-label', label);
  el('sidebar-toggle').title = label;
  el('connection-sidebar').inert = compact && !expanded;
  document.querySelector('main').inert = compact && expanded;
  el('sidebar-backdrop').hidden = !compact || !expanded;
}
/** 按当前窗口宽度收起侧栏或打开抽屉，只调整布局，不重置当前连接或表单。 */
function toggleSidebar() {
  el('workbench').classList.toggle(narrowSidebar.matches ? 'sidebar-open' : 'sidebar-collapsed');
  syncSidebarState();
}
/** 选定连接或按 Escape 后关闭窄屏抽屉，将焦点交回入口；桌面导航保持原状态。 */
function closeMobileSidebar() {
  if (!narrowSidebar.matches || !el('workbench').classList.contains('sidebar-open')) return;
  el('workbench').classList.remove('sidebar-open');
  syncSidebarState();
  el('sidebar-toggle').focus();
}
/** 更新连接数量与列表；绿色圆点标记模拟挟持连接，收起后仍保留名称提示与状态。 */
function renderList() {
  el('connection-count').textContent = profiles.length;
  el('summary-count').textContent = profiles.length;
  el('connections').innerHTML = profiles.map(p => `<div class="connection ${p.id === selected.id ? 'active' : ''} ${relay === p.id ? 'routed' : ''}"><button class="connection-button" data-connection="${p.id}" title="${escapeHTML(p.name)}" aria-label="${escapeHTML(p.name)}${relay === p.id ? '，挟持中' : ''}" ${p.id === selected.id ? 'aria-current="true"' : ''}><span class="connection-avatar ${p.color}" aria-hidden="true">${p.avatar}</span><span class="connection-text"><strong>${escapeHTML(p.name)}${relay === p.id ? '<span class="connected-marker" title="挟持中"></span>' : ''}</strong><small>${escapeHTML(p.model)}</small></span></button><button class="connection-more" data-menu="${p.id}" aria-label="${escapeHTML(p.name)} 的更多操作" aria-haspopup="menu"><svg aria-hidden="true"><use href="#i-more"/></svg></button></div>`).join('');
}
/** 切换配置 Tab；键盘导航与环境变量入口都复用同一显示状态。 */
function setTab(tab) {
  activeTab = tab;
  document.querySelectorAll('[data-tab]').forEach(button => {
    const current = button.dataset.tab === tab;
    button.setAttribute('aria-selected', current);
    button.tabIndex = current ? 0 : -1;
    el(`panel-${button.dataset.tab}`).hidden = !current;
  });
}
/** 将当前连接的模拟数据写入表单，跨连接选择不会访问服务商。 */
function renderProfile() {
  renderList();
  el('profile-title').textContent = selected.name;
  el('profile-avatar').textContent = selected.avatar;
  el('profile-subtitle').textContent = `${selected.vendor} · ${selected.subtitle}`;
  el('connection-name').value = selected.name;
  el('protocol').value = selected.protocol;
  el('endpoint').value = selected.endpoint;
  el('model-id').innerHTML = `<option>${escapeHTML(selected.model)}</option>`;
  el('model-manual').value = selected.model;
  updateSavedState();
  renderEffort();
  renderRoute();
}
/** 展示七档思考选项，并同步选中项的中文行为说明。 */
function renderEffort() {
  el('reasoning-options').innerHTML = levels.map(([value, label, caption]) => `<button class="effort-option" role="radio" aria-checked="${selected.effort === value}" tabindex="${selected.effort === value ? 0 : -1}" data-effort="${value}"><strong>${label}</strong><small>${caption}</small></button>`).join('');
  el('effort-description').textContent = levels.find(level => level[0] === selected.effort)[3];
}
/** 将开启状态、模型路线和侧栏提示同步，避免视觉上出现两个不同的当前模型。 */
function renderRoute() {
  const active = profiles.find(profile => profile.id === relay);
  el('route-bar').classList.toggle('enabled', Boolean(active));
  el('mode-switch').setAttribute('aria-checked', Boolean(active));
  el('mode-label').textContent = active ? '已开启' : '未开启';
  el('route-target').textContent = active ? active.name : '自带模型';
  el('route-description').textContent = active ? '使用期间保持 App 运行' : '开启后，让此连接承担 Codex 主对话';
  el('sidebar-mode').textContent = active ? `挟持中 · ${active.name}` : 'Codex 使用自带模型';
  el('sidebar-dot').classList.toggle('on', Boolean(active));
}
/** 只修改当前连接的保存标识，不在切换 Tab 时丢失待保存状态。 */
function updateSavedState() {
  const changed = dirty.has(selected.id);
  el('save-status').classList.toggle('dirty', changed);
  el('save-status').innerHTML = changed ? '待保存' : '<svg><use href="#i-check"/></svg>已保存';
  el('save-guidance').textContent = changed ? '有尚未保存的修改' : '修改将在保存后生效';
}
/** 表单编辑仅标记内存草稿，原型不会写入模型配置文件。 */
function markDirty() { dirty.add(selected.id); updateSavedState(); }
/** 模拟保存并明确告知这是设计稿；名称校验沿用现有产品的调用名称约束。 */
function saveConfiguration() {
  const name = el('connection-name').value.trim();
  if (!/^[a-z][a-z0-9_-]{0,47}$/.test(name) || profiles.some(p => p.id !== selected.id && p.name === name)) {
    setTab('connection');
    el('connection-name').focus();
    showToast('名称需以小写字母开头，且不能与其他连接重复。');
    return;
  }
  selected.name = name;
  selected.endpoint = el('endpoint').value;
  selected.protocol = el('protocol').value;
  selected.model = el('model-manual').hidden ? el('model-id').value : el('model-manual').value;
  dirty.delete(selected.id);
  renderProfile();
  showToast('设计稿演示：配置已保存，未改动真实连接。');
}
/** 使用一个可被后续操作替换的提示，不堆叠弹窗或打断表单。 */
function showToast(text) {
  clearTimeout(toastTimer);
  el('toast').textContent = text;
  el('toast').hidden = false;
  toastTimer = setTimeout(() => { el('toast').hidden = true; }, 3200);
}
/** 在连接旁打开仅含复制、删除的菜单，顶层定位不受侧栏裁切。 */
function openMenu(button) {
  menuTarget = profiles.find(profile => profile.id === Number(button.dataset.menu));
  const menu = el('model-menu');
  if (menu.matches(':popover-open')) menu.hidePopover();
  menu.showPopover();
  const rect = button.getBoundingClientRect();
  menu.style.left = `${Math.min(rect.right + 8, innerWidth - menu.offsetWidth - 12)}px`;
  menu.style.top = `${Math.min(rect.top, innerHeight - menu.offsetHeight - 12)}px`;
  el('copy-connection').focus();
}
/** 复制示例配置并选择副本；仅在设计稿中演示现有的复制流程。 */
function copyConnection() {
  let name = `${menuTarget.name}-copy`, index = 2;
  while (profiles.some(profile => profile.name === name)) name = `${menuTarget.name}-copy-${index++}`;
  selected = {...menuTarget, id: Date.now(), name};
  profiles.push(selected);
  el('model-menu').hidePopover();
  renderProfile();
  setTab('connection');
  showToast('已复制示例连接，可修改名称。');
}
/** 删除当前菜单指向的示例项；保留最后一个示例，避免原型进入尚未设计的空白态。 */
function deleteConnection() {
  el('model-menu').hidePopover();
  if (profiles.length === 1) { showToast('设计稿保留一个示例连接供预览。'); return; }
  profiles.splice(profiles.indexOf(menuTarget), 1);
  if (relay === menuTarget.id) relay = null;
  if (selected === menuTarget) selected = profiles[0];
  renderProfile();
  showToast('已删除示例连接，真实配置不受影响。');
}

el('connections').addEventListener('click', event => {
  const connection = event.target.closest('[data-connection]');
  const menu = event.target.closest('[data-menu]');
  if (connection) { selected = profiles.find(profile => profile.id === Number(connection.dataset.connection)); renderProfile(); closeMobileSidebar(); }
  if (menu) openMenu(menu);
});
document.querySelectorAll('[data-tab]').forEach(button => {
  button.addEventListener('click', () => setTab(button.dataset.tab));
  button.addEventListener('keydown', event => {
    if (!['ArrowRight', 'ArrowLeft'].includes(event.key)) return;
    event.preventDefault();
    const tabs = ['connection', 'model', 'purpose', 'advanced'];
    setTab(tabs[(tabs.indexOf(activeTab) + (event.key === 'ArrowRight' ? 1 : 3)) % 4]);
    el(`tab-${activeTab}`).focus();
  });
});
el('reasoning-options').addEventListener('click', event => {
  const button = event.target.closest('[data-effort]');
  if (button) { selected.effort = button.dataset.effort; markDirty(); renderEffort(); }
});
el('reasoning-options').addEventListener('keydown', event => {
  if (!['ArrowRight', 'ArrowLeft'].includes(event.key)) return;
  event.preventDefault();
  const index = levels.findIndex(level => level[0] === selected.effort);
  selected.effort = levels[(index + (event.key === 'ArrowRight' ? 1 : levels.length - 1)) % levels.length][0];
  markDirty(); renderEffort(); el('reasoning-options').querySelector('[aria-checked="true"]').focus();
});
el('mode-switch').addEventListener('click', () => {
  relay = relay === null ? selected.id : null;
  renderRoute(); renderList();
  showToast(relay === null ? '设计稿演示：已切回 Codex 自带模型。' : '设计稿演示：已开启挟持模式。实际使用需重启 Codex。');
});
document.querySelectorAll('input, select, textarea').forEach(field => field.addEventListener('input', markDirty));
el('save-button').addEventListener('click', saveConfiguration);
el('test-connection').addEventListener('click', () => showToast('设计稿演示：连接成功。未发送 API 请求，不产生费用。'));
el('show-key').addEventListener('click', () => {
  const hidden = el('api-key').type === 'password';
  el('api-key').type = hidden ? 'text' : 'password';
  el('show-key').setAttribute('aria-label', hidden ? '隐藏示例 Key' : '显示示例 Key');
});
el('env-link').addEventListener('click', () => { setTab('advanced'); el('key-env').focus(); });
el('refresh-models').addEventListener('click', () => showToast('设计稿使用示例模型列表，未访问模型服务。'));
el('manual-model').addEventListener('click', () => {
  const manual = el('model-manual').hidden;
  el('model-manual').hidden = !manual;
  el('model-id').parentElement.hidden = manual;
  el('manual-model').textContent = manual ? '列表选择' : '手动输入';
  if (manual) el('model-manual').focus();
});
el('add-connection').addEventListener('click', () => {
  let index = 1;
  while (profiles.some(profile => profile.name === `new-connection-${index}`)) index++;
  selected = {...profiles[0], id: Date.now(), name: `new-connection-${index}`, model: '', effort: '', subtitle: '新连接', vendor: '未配置', avatar: '+'};
  profiles.push(selected); dirty.add(selected.id); setTab('connection'); renderProfile(); closeMobileSidebar(); el('connection-name').focus(); el('connection-name').select();
});
el('copy-connection').addEventListener('click', copyConnection);
el('delete-connection').addEventListener('click', deleteConnection);
el('help-button').addEventListener('click', () => el('help-dialog').showModal());
el('design-info-button').addEventListener('click', () => el('help-dialog').showModal());
el('plugin-button').addEventListener('click', () => showToast('设计稿演示：插件已安装，可在这里进行安装与更新。'));
el('update-button').addEventListener('click', () => showToast('设计稿演示：当前为示例版本 v0.3.1，未检查真实更新。'));
el('sidebar-toggle').addEventListener('click', toggleSidebar);
el('sidebar-backdrop').addEventListener('click', closeMobileSidebar);
narrowSidebar.addEventListener('change', () => { el('workbench').classList.remove('sidebar-open'); syncSidebarState(); });
document.addEventListener('click', event => {
  if (el('model-menu').matches(':popover-open') && !event.target.closest('#model-menu, [data-menu]')) el('model-menu').hidePopover();
});
document.addEventListener('keydown', event => {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') { event.preventDefault(); saveConfiguration(); }
  if (event.key === 'Escape' && el('model-menu').matches(':popover-open')) el('model-menu').hidePopover();
  if (event.key === 'Escape') closeMobileSidebar();
});
syncSidebarState();
renderProfile();
