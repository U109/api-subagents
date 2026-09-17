// 桌面状态只通过固定 Go 绑定读取，弹窗和状态入口共享一份后台快照。
(() => {
  const bridge = window.desktopApp;
  if (!bridge) return;
  document.querySelectorAll('.desktop-only').forEach(element => { element.hidden = false; });
  let state, actionPending = false;
  /** 读取固定界面节点，桌面状态只写入 textContent，不解释为 HTML。 */
  const node = id => document.getElementById(id);

  /** 将安装、下载及模型接入状态同步到侧栏、工具栏和管理弹窗。 */
  function renderDesktop(next) {
    state = next;
    node('desktop-version').textContent = next.version;
    node('plugin-state').textContent = next.plugin.message;
    node('plugin-label').textContent = next.plugin.phase === 'installed' ? '已安装' : next.plugin.phase === 'installing' ? '安装中' : '待安装';
    node('plugin-dot').hidden = next.plugin.phase !== 'installed';
    const install = node('install-plugin');
    install.textContent = next.plugin.phase === 'installing' ? '正在安装…' : next.plugin.phase === 'installed' ? next.plugin.installedVersion === next.version ? '重新安装插件' : '更新插件' : '安装到 Codex';
    install.disabled = actionPending || next.plugin.phase === 'installing';
    const phase = next.update.phase;
    node('update-state').textContent = next.update.message;
    node('check-update').textContent = phase === 'available' ? '下载 ' + next.update.availableVersion : phase === 'downloaded' ? '重启并更新' : phase === 'checking' ? '正在检查…' : phase === 'downloading' ? '正在下载…' : '检查更新';
    node('check-update').disabled = actionPending || ['checking', 'downloading', 'installing'].includes(phase) || next.plugin.phase === 'installing';
    node('update-label').textContent = phase === 'available' ? '有新版本' : phase === 'downloaded' ? '更新已就绪' : phase === 'downloading' ? '正在下载' : phase === 'checking' ? '正在检查' : '检查更新';
    node('update-dot').hidden = !['available', 'downloaded'].includes(phase);
    node('open-releases').hidden = !['manual', 'error'].includes(phase);
    node('open-releases').disabled = actionPending;
    node('update-progress').hidden = !['downloading', 'downloaded'].includes(phase);
    node('update-progress').value = next.update.progress;
    renderRelay(next.relay);
  }

  /** 开关表示整个挟持模式；另一个连接被选中时提供明确的切换动作，绿色圆点标记实际请求连接。 */
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
    node('relay-target').textContent = active || '自带模型';
    node('relay-target').title = relay.enabled ? '默认连接：' + relay.model + '；最近使用：' + active : 'Codex 自带模型';
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

  /** 同时更新页面和弹窗错误，确保下载失败或配置恢复失败不会藏在已关闭的面板里。 */
  function desktopError(message) {
    node('desktop-error').textContent = message;
    document.querySelectorAll('[data-desktop-error]').forEach(element => { element.textContent = message; });
  }

  /** 串行执行桌面操作；过程状态仍接收后台事件，失败不改变用户的表单草稿。 */
  async function desktopAction(action) {
    if (actionPending || !state) return;
    actionPending = true;
    renderDesktop(state);
    desktopError('');
    try { renderDesktop(await action()); }
    catch (error) { desktopError(typeof error === 'string' ? error : error.message || '操作未完成，请稍后重试。'); }
    finally { actionPending = false; renderDesktop(state); }
  }

  /** 依照已验证的更新阶段选择动作；安装前沿用配置草稿和正在请求的保护事件。 */
  function updateAction() {
    return desktopAction(async () => {
      if (state.update.phase === 'available') return bridge.downloadUpdate();
      if (state.update.phase === 'downloaded') {
        if (!window.dispatchEvent(new Event('desktop:before-update', {cancelable: true}))) {
          desktopError('请先保存配置并等待当前操作完成，再重启更新。');
          return state;
        }
        return bridge.installUpdate();
      }
      return bridge.checkUpdate();
    });
  }

  node('install-plugin').onclick = () => desktopAction(() => bridge.installPlugin());
  node('enable-relay').onclick = () => desktopAction(() => bridge.enableRelay(window.modelEditor.selectedName()));
  node('relay-switch').onclick = () => desktopAction(() => state.relay.enabled ? bridge.disableRelay() : bridge.enableRelay(window.modelEditor.selectedName()));
  node('check-update').onclick = updateAction;
  node('update-entry').onclick = () => {
    window.uiShell.openDialog('update-dialog');
    if (state && !['available', 'downloaded', 'downloading', 'checking', 'installing'].includes(state.update.phase)) void updateAction();
  };
  node('open-releases').onclick = () => desktopAction(() => bridge.openReleases());
  window.addEventListener('config:rendered', () => { if (state) renderRelay(state.relay); });
  window.addEventListener('config:draft', () => { if (state) renderRelay(state.relay); });
  bridge.onState(renderDesktop);
  bridge.getState().then(renderDesktop).catch(() => desktopError('桌面服务未就绪，请重新打开应用。'));
})();
