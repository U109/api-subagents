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

  /** 清理指定通道的提示和计时器，不影响其他操作的错误或警告。 */
  function clear(key) {
    const entry = entries.get(key);
    if (!entry) return;
    clearTimeout(entry.timer);
    entry.card.remove();
    entries.delete(key);
    if (!entries.size && stack.matches(':popover-open')) stack.hidePopover();
  }

  /** 鼠标悬停或键盘进入提示后暂停消失计时，留下足够时间阅读长文本。 */
  function pause(entry) {
    if (!entry.timer) return;
    clearTimeout(entry.timer);
    entry.timer = null;
    entry.remaining = Math.max(0, entry.remaining - (Date.now() - entry.started));
  }

  /** 成功与普通提示自动收起，错误和警告保留到用户主动关闭。 */
  function resume(entry) {
    if (entries.get(entry.key) !== entry || entry.timer || entry.sticky || entry.card.matches(':hover') || entry.card.contains(document.activeElement)) return;
    entry.started = Date.now();
    entry.timer = setTimeout(() => clear(entry.key), entry.remaining);
  }

  /** 同一通道替换旧提示并统一文末标点，最多显示三条；所有关闭操作均可通过键盘完成 */
  function show(key, message, kind = 'success') {
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
    close.addEventListener('click', () => {
      const focused = card.contains(document.activeElement);
      clear(key);
      if (focused && origin?.isConnected) origin.focus();
    });
    card.append(icon, text, close);
    const entry = {key, card, sticky: ['error', 'warning'].includes(kind), remaining: kind === 'success' ? 5500 : 8000};
    entries.set(key, entry);
    stack.append(card);
    mount();
    text.textContent = message;
    card.addEventListener('pointerenter', () => pause(entry));
    card.addEventListener('pointerleave', () => resume(entry));
    card.addEventListener('focusin', () => pause(entry));
    card.addEventListener('focusout', () => queueMicrotask(() => resume(entry)));
    resume(entry);
  }

  new MutationObserver(mount).observe(document.body, {subtree: true, attributes: true, attributeFilter: ['open']});
  window.notices = {show, clear, formatMessage};
})();
