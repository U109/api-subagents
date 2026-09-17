// 为原生 select 提供一致的白底菜单；原控件保留值、校验和 change 事件，不改变配置协议。
(() => {
  const bindings = new WeakMap();
  const menu = document.createElement('div');
  menu.id = 'select-menu';
  menu.className = 'select-menu';
  menu.setAttribute('popover', 'manual');
  menu.setAttribute('role', 'listbox');
  document.body.append(menu);
  let active = null, search = '', searchAt = 0;

  /** 关闭唯一的下拉菜单并清理辅助技术状态；键盘提交时可恢复触发按钮焦点。 */
  function close(focus = false) {
    const previous = active;
    active = null;
    if (menu.matches(':popover-open')) menu.hidePopover();
    if (!previous) return;
    previous.binding.button.setAttribute('aria-expanded', 'false');
    previous.binding.button.removeAttribute('aria-activedescendant');
    if (focus && previous.binding.button.isConnected) previous.binding.button.focus();
  }

  /** 将原生控件当前值、禁用和校验状态映射到触发按钮，文本始终以 textContent 写入。 */
  function sync(binding) {
    const {select, button} = binding;
    button.querySelector('.select-value').textContent = select.selectedOptions[0]?.textContent || '请选择';
    button.disabled = select.disabled;
    button.setAttribute('aria-invalid', select.getAttribute('aria-invalid') || 'false');
    if (active?.binding === binding && (select.disabled || !select.isConnected)) close();
  }

  /** 判断选项及其分组是否禁用，键盘导航和鼠标提交共用同一边界。 */
  function enabled(option) { return !option.disabled && !option.parentElement?.disabled; }

  /** 更新候选焦点并保持可见；不在浏览或滚动候选项时改变配置值。 */
  function highlight(index) {
    if (!active || !active.options[index] || !enabled(active.options[index])) return;
    active.index = index;
    menu.querySelectorAll('[data-choice]').forEach(item => item.classList.toggle('highlighted', Number(item.dataset.choice) === index));
    const item = menu.querySelector(`[data-choice="${index}"]`);
    active.binding.button.setAttribute('aria-activedescendant', item.id);
    item.scrollIntoView({block: 'nearest'});
  }

  /** 跳过禁用项并循环移动候选焦点，边界模式用于 Home 和 End。 */
  function move(direction, edge = false) {
    if (!active) return;
    const indexes = active.options.map((option, index) => enabled(option) ? index : -1).filter(index => index >= 0);
    if (!indexes.length) return;
    const current = indexes.indexOf(active.index);
    const next = edge ? (direction > 0 ? 0 : indexes.length - 1) : (current + direction + indexes.length) % indexes.length;
    highlight(indexes[next]);
  }

  /** 仅在明确提交候选后修改原生值并发送一次 change，取消菜单不产生草稿。 */
  function choose(index) {
    if (!active || !active.options[index] || !enabled(active.options[index])) return;
    const {binding} = active;
    const changed = binding.select.selectedIndex !== index;
    binding.select.selectedIndex = index;
    close(true);
    sync(binding);
    if (changed) {
      binding.select.dispatchEvent(new Event('change', {bubbles: true}));
      // 接口切换会重绘表单；只在旧触发器被替换时恢复焦点，保留校验主动定位的错误字段。
      if (!binding.button.isConnected) document.getElementById(binding.button.id)?.focus();
    }
  }

  /** 按输入框宽度和屏幕剩余空间定位菜单，必要时向上展开，避免原生蓝色选中态。 */
  function open(binding) {
    if (binding.select.disabled) return;
    close();
    const options = [...binding.select.options];
    active = {binding, options, index: binding.select.selectedIndex};
    search = '';
    searchAt = 0;
    menu.replaceChildren();
    menu.setAttribute('aria-label', binding.label);
    options.forEach((option, index) => {
      const item = document.createElement('div');
      item.id = `select-choice-${index}`;
      item.dataset.choice = String(index);
      item.className = 'select-option';
      item.setAttribute('role', 'option');
      item.setAttribute('aria-selected', String(option.selected));
      item.setAttribute('aria-disabled', String(!enabled(option)));
      const text = document.createElement('span');
      text.textContent = option.textContent;
      const check = document.createElement('span');
      check.className = 'select-check';
      check.setAttribute('aria-hidden', 'true');
      check.textContent = option.selected ? '✓' : '';
      item.append(text, check);
      menu.append(item);
    });
    // 弹窗中的下拉必须留在该弹窗内，避免被模态窗口的 inert 规则屏蔽。
    (binding.button.closest('dialog') || document.body).append(menu);
    const rect = binding.button.getBoundingClientRect();
    const below = innerHeight - rect.bottom - 12;
    const above = rect.top - 12;
    const up = below < Math.min(260, options.length * 38 + 10) && above > below;
    menu.style.width = `${Math.min(rect.width, innerWidth - 24)}px`;
    menu.style.maxHeight = `${Math.max(60, Math.min(280, (up ? above : below) - 6))}px`;
    menu.style.left = `${Math.max(12, Math.min(rect.left, innerWidth - Math.min(rect.width, innerWidth - 24) - 12))}px`;
    menu.style.top = `${rect.bottom + 6}px`;
    menu.showPopover();
    if (up) menu.style.top = `${Math.max(6, rect.top - menu.offsetHeight - 6)}px`;
    binding.button.setAttribute('aria-expanded', 'true');
    if (active.index >= 0 && enabled(options[active.index])) highlight(active.index);
    else move(1, true);
  }

  /** 支持方向键、首尾、提交、取消及前缀检索；Tab 使用浏览器正常的焦点顺序。 */
  function onKey(event, binding) {
    const opened = active?.binding === binding;
    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      event.preventDefault();
      if (!opened) open(binding);
      if (event.key === 'Home' || event.key === 'End') move(event.key === 'Home' ? 1 : -1, true);
      else if (opened) move(event.key === 'ArrowDown' ? 1 : -1);
    } else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      if (opened) choose(active.index); else open(binding);
    } else if (event.key === 'Escape' && opened) {
      event.preventDefault();
      event.stopPropagation();
      close(true);
    } else if (event.key === 'Tab') close();
    else if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
      event.preventDefault();
      if (!opened) open(binding);
      const now = Date.now();
      search = (now - searchAt > 700 ? '' : search) + event.key.toLowerCase();
      searchAt = now;
      const index = active.options.findIndex(option => enabled(option) && option.textContent.trim().toLowerCase().startsWith(search));
      if (index >= 0) highlight(index);
    }
  }

  /** 为新渲染的下拉框建立适配器；调用方继续使用原 ID 读取和修改配置值。 */
  function enhance(root = document) {
    root.querySelectorAll('select').forEach(select => {
      if (bindings.has(select)) return;
      const control = document.createElement('div');
      control.className = 'select-control';
      const button = document.createElement('button');
      button.type = 'button';
      button.id = select.id + '-trigger';
      button.className = 'select-trigger';
      button.setAttribute('role', 'combobox');
      button.setAttribute('aria-haspopup', 'listbox');
      button.setAttribute('aria-expanded', 'false');
      button.setAttribute('aria-controls', menu.id);
      const label = document.querySelector(`label[for="${CSS.escape(select.id)}"]`);
      if (label) {
        label.id ||= select.id + '-label';
        label.htmlFor = button.id;
        button.setAttribute('aria-labelledby', label.id);
      } else button.setAttribute('aria-label', select.getAttribute('aria-label') || '选择选项');
      button.innerHTML = '<span class="select-value"></span><svg aria-hidden="true"><use href="#i-down"/></svg>';
      const binding = {select, button, label: label?.textContent || select.getAttribute('aria-label') || '选择选项'};
      bindings.set(select, binding);
      select.before(control);
      control.append(select, button);
      select.hidden = true;
      button.addEventListener('click', () => active?.binding === binding ? close() : open(binding));
      button.addEventListener('keydown', event => onKey(event, binding));
      select.addEventListener('change', () => sync(binding));
      new MutationObserver(() => sync(binding)).observe(select, {attributes: true, childList: true, subtree: true});
      sync(binding);
    });
    refresh();
  }

  /** 同步程序设置的 value 与禁用状态，并清理已被重新渲染替换的菜单。 */
  function refresh() {
    if (active && !active.binding.select.isConnected) close();
    document.querySelectorAll('select').forEach(select => { if (bindings.has(select)) sync(bindings.get(select)); });
  }

  window.selectUI = {enhance, refresh, close};
  menu.addEventListener('pointerdown', event => event.preventDefault());
  menu.addEventListener('click', event => { const item = event.target.closest('[data-choice]'); if (item) choose(Number(item.dataset.choice)); });
  document.addEventListener('pointerdown', event => { if (active && !menu.contains(event.target) && !active.binding.button.contains(event.target)) close(); });
  document.addEventListener('scroll', event => { if (active && !menu.contains(event.target)) close(); }, true);
  window.addEventListener('resize', () => close());
})();
