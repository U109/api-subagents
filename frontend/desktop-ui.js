// 桌面状态只通过固定 Go 绑定读取，弹窗和状态入口共享一份后台快照
(() => {
  const bridge = window.desktopApp;
  if (!bridge) return;
  document.querySelectorAll('.desktop-only').forEach(element => { element.hidden = false; });
  let state, actionPending = false;
  /** 读取固定界面节点，桌面状态只写入 textContent，不解释为 HTML */
  const node = id => document.getElementById(id);

  /** 按稳定版本的三个数字比较新旧，只用于按钮呈现，真正安装仍由 Go 再次检查 */
  function newerVersion(next, current) {
    const a = String(next || '').split('.').map(Number), b = String(current || '0.0.0').split('.').map(Number);
    if (a.length !== 3 || b.length !== 3 || [...a, ...b].some(value => !Number.isFinite(value))) return false;
    for (let i = 0; i < 3; i++) { if (a[i] !== b[i]) return a[i] > b[i]; }
    return false;
  }

  /** 独立显示插件已安装、随 App 提供和在线可更新版本，不再把 App 版本当作插件版本 */
  function renderPlugin(next) {
    const plugin = next.plugin, update = next.pluginUpdate || {phase: 'idle'};
    const installed = plugin.installedVersion, bundled = plugin.bundledVersion;
    const available = ['available', 'downloaded'].includes(update.phase);
    const downloading = update.phase === 'downloading';
    const installing = plugin.phase === 'installing';
    const wouldDowngrade = newerVersion(installed, bundled);
    node('plugin-version').textContent = installed ? 'v' + installed : '未安装';
    node('plugin-bundled-version').textContent = bundled ? 'v' + bundled : '不可用';
    node('plugin-available-row').hidden = !update.availableVersion;
    node('plugin-available-version').textContent = update.availableVersion ? 'v' + update.availableVersion : '—';
    node('plugin-label').textContent = installing ? '安装中' : installed ? '已安装' : '待安装';
    node('plugin-dot').hidden = !installed;
    node('plugin-state').textContent = window.notices.formatMessage(downloading ? '正在下载插件更新… ' + Math.round(update.progress) + '%' : installing || update.phase === 'idle' ? plugin.message : update.message);
    node('plugin-progress').hidden = !downloading;
    node('plugin-progress').value = update.progress || 0;
    node('check-plugin-update').disabled = actionPending || installing || update.phase === 'checking';
    node('check-plugin-update').textContent = update.phase === 'checking' ? '正在检查…' : '检查插件更新';
    const install = node('install-plugin');
    install.textContent = downloading ? '正在下载…' : installing ? '正在安装…' : available ? '更新至 v' + update.availableVersion : !installed ? '安装到 Codex' : newerVersion(bundled, installed) ? '更新至 v' + bundled : wouldDowngrade ? '已安装更高版本' : '重新安装插件';
    install.disabled = actionPending || installing || (!available && (!bundled || wouldDowngrade));
  }

  /** 后台阶段改变时显示三秒结果提示；模式切换保留重启提醒，实际接入仍以本地收到请求为准 */
  function notifyChanges(previous, next) {
    if (!previous) return;
    if (previous.update.phase !== next.update.phase) {
      if (next.update.phase === 'available') window.notices.show('app-update', '有新版本可用：v' + next.update.availableVersion, 'warning');
      if (next.update.phase === 'latest') window.notices.show('app-update', 'App 已是最新版本');
      if (next.update.phase === 'downloaded') window.notices.show('app-update', '更新已下载，可以重启并更新');
    }
    if (previous.pluginUpdate?.phase !== next.pluginUpdate?.phase) {
      if (next.pluginUpdate?.phase === 'available') window.notices.show('plugin-update', '有插件新版本可用：v' + next.pluginUpdate.availableVersion, 'warning');
      if (next.pluginUpdate?.phase === 'latest') window.notices.show('plugin-update', '插件已是最新版本');
      if (next.pluginUpdate?.phase === 'incompatible') window.notices.show('plugin-update', next.pluginUpdate.message, 'warning');
    }
    if (previous.plugin.phase === 'installing' && next.plugin.phase === 'installed') window.notices.show('plugin-update', '插件已安装，请在 Codex 新建对话');
    if (previous.relay?.enabled !== next.relay?.enabled && !['closing', 'ready'].includes(next.close?.phase)) window.notices.show('relay', next.relay?.enabled ? '挟持已开启，请重启 Codex' : '挟持已关闭，请重启 Codex', 'warning');
    if (next.relay?.enabled && !previous.relay?.requests && next.relay.requests > 0) window.notices.show('relay', 'Codex 请求已接入本地网关');
  }

  /** 将安装、下载及模型接入状态同步到侧栏、工具栏和管理弹窗 */
  function renderDesktop(next) {
    const previous = state;
    state = next;
    node('desktop-version').textContent = 'v' + next.version;
    renderPlugin(next);
    const phase = next.update.phase;
    node('update-state').textContent = window.notices.formatMessage(next.update.message);
    node('update-available-row').hidden = !next.update.availableVersion;
    node('update-available-version').textContent = next.update.availableVersion ? 'v' + next.update.availableVersion : '—';
    node('check-update').textContent = phase === 'available' ? '下载 ' + next.update.availableVersion : phase === 'downloaded' ? '重启并更新' : phase === 'downloading' ? '正在下载…' : phase === 'installing' ? '正在安装…' : '下载更新';
    node('check-update').hidden = !['available', 'downloaded', 'downloading', 'installing'].includes(phase);
    node('check-update').disabled = actionPending || ['checking', 'downloading', 'installing'].includes(phase) || next.plugin.phase === 'installing';
    node('recheck-update').textContent = phase === 'checking' ? '正在检查最新版本…' : '检查最新版本';
    node('recheck-update').disabled = actionPending || ['checking', 'downloading', 'installing'].includes(phase) || next.plugin.phase === 'installing';
    node('update-label').textContent = phase === 'available' ? '有新版本' : phase === 'downloaded' ? '更新已就绪' : phase === 'downloading' ? '正在下载' : phase === 'checking' ? '正在检查' : '检查更新';
    node('update-dot').hidden = !['available', 'downloaded'].includes(phase);
    node('open-releases').hidden = !['manual', 'error'].includes(phase);
    node('open-releases').disabled = actionPending;
    node('update-progress').hidden = !['downloading', 'downloaded'].includes(phase);
    node('update-progress').value = next.update.progress;
    renderRelay(next.relay);
    renderClose(previous?.close, next.close);
    notifyChanges(previous, next);
  }

  /** 正常退出只显示恢复进度；仅有草稿时打开与页面一致的确认框，失败后恢复界面操作 */
  function renderClose(previous = {}, next = {}) {
    const dialog = node('exit-dialog');
    const stopping = ['closing', 'ready'].includes(next.phase);
    node('workbench').inert = stopping;
    node('confirm-exit').disabled = stopping;
    if (next.phase === 'confirm') window.uiShell.openDialog('exit-dialog');
    else if (dialog.open) dialog.close();
    if (previous.phase === next.phase && previous.message === next.message) return;
    if (next.phase === 'error') window.notices.show('exit', next.message, 'error');
    else if (['closing', 'blocked'].includes(next.phase)) window.notices.show('exit', next.message, 'info');
    else window.notices.clear('exit');
  }

  /** 开关表示整个挟持模式；另一个连接被选中时提供明确的切换动作，绿色圆点标记实际请求连接 */
  function renderRelay(relay) {
    if (!relay) return;
    node('relay-panel').hidden = false;
    node('relay-panel').classList.toggle('enabled', relay.enabled);
    node('relay-label').textContent = relay.enabled ? '已开启' : '未开启';
    node('relay-state').textContent = relay.message;
    node('relay-state').title = relay.message;
    const selected = window.modelEditor?.selectedName();
    const ready = window.modelEditor?.ready();
    const active = relay.enabled ? relay.activeModel || relay.model : null;
    node('relay-target').textContent = active ? relay.activeModelId || active : '自带模型';
    node('relay-target').title = relay.enabled ? '默认连接：' + relay.model + '；最近使用：' + active + (relay.activeModelId ? ' · ' + relay.activeModelId : '') : 'Codex 自带模型';
    node('relay-switch').setAttribute('aria-checked', String(Boolean(relay.enabled)));
    node('relay-switch').disabled = actionPending || (!relay.enabled && !ready);
    node('relay-switch').title = !relay.enabled && !ready ? '请先保存连接配置' : relay.enabled ? '关闭并恢复 Codex 原有配置' : '用当前连接开启';
    node('enable-relay').hidden = !relay.enabled || relay.model === selected || !selected;
    node('enable-relay').disabled = actionPending || !ready;
    node('enable-relay').title = ready ? '设为跟随 App 时使用的默认连接' : '请先保存连接配置';
    node('sidebar-mode').textContent = active ? '挟持中 · ' + active : 'Codex 使用自带模型';
    node('sidebar-relay').title = node('sidebar-mode').textContent;
    node('sidebar-dot').classList.toggle('on', Boolean(active));
    document.querySelectorAll('.model-select').forEach(button => {
      const enabled = button.dataset.name === active;
      button.closest('.model').classList.toggle('relay-enabled', enabled);
      button.setAttribute('aria-label', button.dataset.name + (enabled ? '，挟持中' : ''));
      const badge = button.querySelector('.relay-badge');
      if (badge) badge.hidden = !enabled;
    });
  }

  /** 将失败原因显示为三秒轻提示，服务返回的诊断内容仍完整保留 */
  function desktopError(message) {
    window.notices.show('desktop', message, 'error');
  }

  /** 串行执行桌面操作；过程状态仍接收后台事件，失败不改变用户的表单草稿 */
  async function desktopAction(action) {
    if (actionPending || !state || ['closing', 'ready'].includes(state.close?.phase)) return;
    actionPending = true;
    renderDesktop(state);
    desktopError('');
    try { renderDesktop(await action()); }
    catch (error) { desktopError(typeof error === 'string' ? error : error.message || '操作未完成，请稍后重试'); }
    finally { actionPending = false; renderDesktop(state); }
  }

  /** 依照已验证的更新阶段选择动作；安装前沿用配置草稿和正在请求的保护事件 */
  function updateAction() {
    return desktopAction(async () => {
      if (state.update.phase === 'available') return bridge.downloadUpdate();
      if (state.update.phase === 'downloaded') {
        if (!window.dispatchEvent(new Event('desktop:before-update', {cancelable: true}))) {
          desktopError('请先保存配置并等待当前操作完成，再重启更新');
          return state;
        }
        return bridge.installUpdate();
      }
      return bridge.checkUpdate();
    });
  }

  node('check-plugin-update').onclick = () => desktopAction(() => bridge.checkPluginUpdate());
  node('install-plugin').onclick = () => desktopAction(() => ['available', 'downloaded'].includes(state.pluginUpdate?.phase) ? bridge.updatePlugin() : bridge.installPlugin());
  node('enable-relay').onclick = () => desktopAction(() => bridge.enableRelay(window.modelEditor.selectedName()));
  node('relay-switch').onclick = () => desktopAction(() => state.relay.enabled ? bridge.disableRelay() : bridge.enableRelay(window.modelEditor.selectedName()));
  node('check-update').onclick = updateAction;
  node('recheck-update').onclick = () => desktopAction(() => bridge.checkUpdate());
  node('update-entry').onclick = () => {
    window.uiShell.openDialog('update-dialog');
    // 每次打开都刷新发布信息，已有下载仅在后台确认仍为最新版时复用。
    if (state && !actionPending && !['downloading', 'checking', 'installing'].includes(state.update.phase)) void desktopAction(() => bridge.checkUpdate());
  };
  node('open-releases').onclick = () => desktopAction(() => bridge.openReleases());
  node('confirm-exit').onclick = () => {
    node('confirm-exit').disabled = true;
    bridge.confirmClose().catch(() => { node('confirm-exit').disabled = false; desktopError('无法退出，请重试'); });
  };
  node('cancel-exit').onclick = () => { void bridge.cancelClose().catch(() => desktopError('无法取消退出，请重试')); };
  node('exit-dialog').addEventListener('cancel', event => {
    event.preventDefault();
    void bridge.cancelClose().catch(() => desktopError('无法取消退出，请重试'));
  });
  window.addEventListener('config:rendered', () => { if (state) renderRelay(state.relay); });
  window.addEventListener('config:draft', () => { if (state) renderRelay(state.relay); });
  bridge.onState(renderDesktop);
  bridge.getState().then(renderDesktop).catch(() => desktopError('桌面服务未就绪，请重新打开应用'));
})();
