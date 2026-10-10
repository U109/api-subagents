// 全局轻提示使用白底浮层，不移动表单或抢占焦点；只通过 textContent 展示服务返回的文字。
(() => {
  const stack = document.createElement('div');
  stack.className = 'notice-stack';
  stack.setAttribute('popover', 'manual');
  stack.setAttribute('aria-label', '操作提示');
  const entries = new Map();

  /** 统一提示文案的结尾样式，仅移除末尾中文句号，保留句间标点、英文句点和换行 */
  function formatMessage(message) {
    return String(message ?? '').replace(/。+(\s*)$/u, '$1');
  }

  /** 保持提示位于最上层弹窗内，防止模态窗口把关闭按钮设为不可操作。 */
  function mount() {
    const owner = [...document.querySelectorAll('dialog[open]')].at(-1) || document.body;
    if (stack.parentElement !== owner) owner.append(stack);
    if (entries.size && !stack.matches(':popover-open')) stack.showPopover();
  }

  /** 清理指定通道和计时器；提示关闭时恢复其占用的键盘焦点，不影响其他提示 */
  function clear(key) {
    const entry = entries.get(key);
    if (!entry) return;
    const focused = entry.card.contains(document.activeElement);
    clearTimeout(entry.timer);
    entry.card.remove();
    entries.delete(key);
    if (!entries.size && stack.matches(':popover-open')) stack.hidePopover();
    if (focused && entry.origin?.isConnected) entry.origin.focus();
  }

  /** 普通提示三秒后关闭；撤销可指定十秒及一次性操作，同通道替换清理旧计时且不保存历史。 */
  function show(key, message, kind = 'success', options = {}) {
    clear(key);
    message = formatMessage(message);
    if (!message) return;
    if (!['success', 'info', 'warning', 'error'].includes(kind)) kind = 'info';
    if (entries.size >= 3) clear(entries.keys().next().value);
    const card = document.createElement('div');
    card.className = 'notice ' + kind;
    card.dataset.noticeKey = key;
    const icon = document.createElement('span');
    icon.className = 'notice-icon';
    icon.setAttribute('aria-hidden', 'true');
    icon.textContent = {success: '✓', info: 'i', warning: '!', error: '!'}[kind];
    const text = document.createElement('div');
    text.className = 'notice-message';
    text.setAttribute('role', kind === 'error' ? 'alert' : 'status');
    const close = document.createElement('button');
    close.type = 'button';
    close.className = 'notice-close';
    close.dataset.shell = '';
    close.setAttribute('aria-label', '关闭提示');
    close.textContent = '×';
    const origin = document.activeElement;
    close.addEventListener('click', () => clear(key));
    card.append(icon, text, close);
    if (typeof options.action === 'function') {
      const action = document.createElement('button'); action.type = 'button'; action.className = 'notice-action'; action.textContent = options.label || '撤销';
      action.addEventListener('click', () => { clear(key); options.action(); }); card.insertBefore(action,close);
    }
    const entry = {card, origin, timer: setTimeout(() => clear(key), options.action ? 10000 : 3000)};
    entries.set(key, entry);
    stack.append(card);
    mount();
    text.textContent = message;
  }

  new MutationObserver(mount).observe(document.body, {subtree: true, attributes: true, attributeFilter: ['open']});
  window.notices = {show, clear, formatMessage};
})();
