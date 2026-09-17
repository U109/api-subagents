// 页面外壳只处理导航和说明弹窗，不读取模型配置或调用桌面服务。
(() => {
  const app = document.getElementById('workbench');
  const sidebar = document.getElementById('connection-sidebar');
  const toggle = document.getElementById('sidebar-toggle');
  const backdrop = document.getElementById('sidebar-backdrop');
  const compact = matchMedia('(max-width: 700px)');

  /** 同步折叠按钮与可聚焦区域；窄屏抽屉开启时，背后的配置表单不可操作。 */
  function syncSidebar() {
    const expanded = compact.matches ? app.classList.contains('sidebar-open') : !app.classList.contains('sidebar-collapsed');
    const label = expanded ? (compact.matches ? '关闭侧栏' : '收起侧栏') : '展开侧栏';
    toggle.setAttribute('aria-expanded', String(expanded));
    toggle.setAttribute('aria-label', label);
    toggle.title = label;
    sidebar.inert = compact.matches && !expanded;
    document.querySelector('main').inert = compact.matches && expanded;
    backdrop.hidden = !compact.matches || !expanded;
  }

  /** 关闭窄屏抽屉并恢复入口焦点，保持连接草稿和桌面折叠偏好不变。 */
  function closeSidebar() {
    if (!compact.matches || !app.classList.contains('sidebar-open')) return;
    app.classList.remove('sidebar-open');
    syncSidebar();
    toggle.focus();
  }

  /** 只打开页面已有的说明或管理弹窗，关闭抽屉以保证弹窗的焦点恢复正确。 */
  function openDialog(id) {
    const dialog = document.getElementById(id);
    if (!(dialog instanceof HTMLDialogElement) || dialog.open) return;
    closeSidebar();
    dialog.showModal();
  }

  window.uiShell = {closeSidebar, openDialog};
  toggle.addEventListener('click', () => {
    app.classList.toggle(compact.matches ? 'sidebar-open' : 'sidebar-collapsed');
    syncSidebar();
  });
  backdrop.addEventListener('click', closeSidebar);
  compact.addEventListener('change', () => { app.classList.remove('sidebar-open'); syncSidebar(); });
  document.querySelectorAll('[data-dialog]').forEach(button => button.addEventListener('click', () => openDialog(button.dataset.dialog)));
  document.querySelectorAll('[data-close]').forEach(button => button.addEventListener('click', () => button.closest('dialog').close()));
  document.addEventListener('keydown', event => { if (event.key === 'Escape') closeSidebar(); });
  syncSidebar();
})();
