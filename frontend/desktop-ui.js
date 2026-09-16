(() => {
  const bridge = window.desktopApp;
  if (!bridge) return;
  const panel = document.getElementById('desktop-panel');
  panel.hidden = false;
  document.body.classList.add('desktop-app');
  let state,
    actionPending = false;

  /** 将固定的桌面状态写入节点；所有服务端文字都使用 textContent，避免版本内容成为标记。 */
  function renderDesktop(next) {
    state = next;
    document.getElementById('desktop-version').textContent = `桌面版 ${next.version}`;
    document.getElementById('plugin-state').textContent = next.plugin.message;
    document.getElementById('update-state').textContent = next.update.message;
    const install = document.getElementById('install-plugin');
    install.textContent =
      next.plugin.phase === 'installing'
        ? '正在安装…'
        : next.plugin.phase === 'installed'
          ? next.plugin.installedVersion === next.version
            ? '重新安装插件'
            : '更新插件'
          : '安装到 Codex';
    install.disabled = actionPending || next.plugin.phase === 'installing';
    const update = document.getElementById('check-update');
    const phase = next.update.phase;
    update.textContent =
      phase === 'available'
        ? `下载 ${next.update.availableVersion}`
        : phase === 'downloaded'
          ? '重启并更新'
          : phase === 'checking'
            ? '正在检查…'
            : phase === 'downloading'
              ? '正在下载…'
              : '检查更新';
    update.disabled =
      actionPending ||
      ['checking', 'downloading', 'installing'].includes(phase) ||
      next.plugin.phase === 'installing';
    document.getElementById('open-releases').hidden = !['manual', 'error'].includes(phase);
    const progress = document.getElementById('update-progress');
    progress.hidden = !['downloading', 'downloaded'].includes(phase);
    progress.value = next.update.progress;
    renderRelay(next.relay);
  }

  /** 显示模式状态并标记实际使用的连接；不把模型地址和 Key 放入桌面状态。 */
  function renderRelay(relay) {
    if (!relay) return;
    document.getElementById('relay-panel').hidden = false;
    const label = document.getElementById('relay-label');
    label.textContent = relay.enabled ? '已开启' : '未开启';
    label.classList.toggle('enabled', relay.enabled);
    document.getElementById('relay-state').textContent = relay.message;
    const selected = window.modelEditor?.selectedName();
    const enable = document.getElementById('enable-relay');
    enable.textContent = relay.enabled ? relay.model === selected ? '当前默认连接' : '切换到此连接' : '用此连接开启';
    enable.disabled = actionPending || !window.modelEditor?.ready() || (relay.enabled && relay.model === selected);
    enable.title = !window.modelEditor?.ready() ? '请先保存连接配置' : '';
    const disable = document.getElementById('disable-relay');
    disable.hidden = !relay.enabled;
    disable.disabled = actionPending;
    const active = relay.enabled ? relay.activeModel || relay.model : null;
    document.querySelectorAll('.model-select').forEach((button) => {
      const enabled = button.dataset.name === active;
      button.closest('.model').classList.toggle('relay-enabled', enabled);
      const badge = button.querySelector('.relay-badge');
      if (badge) badge.hidden = !enabled;
    });
  }

  /** 锁定桌面按钮并显示可恢复错误，不影响配置表单中的未保存内容。 */
  async function desktopAction(action) {
    if (actionPending || !state) return;
    actionPending = true;
    renderDesktop(state);
    document.getElementById('desktop-error').textContent = '';
    try {
      renderDesktop(await action());
    } catch (error) {
      document.getElementById('desktop-error').textContent = typeof error === 'string' ? error : error.message || '操作未完成，请稍后重试。';
    } finally {
      actionPending = false;
      renderDesktop(state);
    }
  }

  document.getElementById('install-plugin').onclick = () => desktopAction(() => bridge.installPlugin());
  document.getElementById('enable-relay').onclick = () => desktopAction(() => bridge.enableRelay(window.modelEditor.selectedName()));
  document.getElementById('disable-relay').onclick = () => desktopAction(() => bridge.disableRelay());
  window.addEventListener('config:rendered', () => { if (state) renderRelay(state.relay); });
  window.addEventListener('config:draft', () => { if (state) renderRelay(state.relay); });
  document.getElementById('check-update').onclick = () =>
    desktopAction(async () => {
      if (state.update.phase === 'available') return bridge.downloadUpdate();
      if (state.update.phase === 'downloaded') {
        if (!window.dispatchEvent(new Event('desktop:before-update', {cancelable: true}))) return state;
        return bridge.installUpdate();
      }
      return bridge.checkUpdate();
    });
  document.getElementById('open-releases').onclick = () => desktopAction(() => bridge.openReleases());
  bridge.onState(renderDesktop);
  bridge
    .getState()
    .then(renderDesktop)
    .catch(() => {
      document.getElementById('desktop-error').textContent = '桌面服务未就绪，请重新打开应用。';
    });
})();
