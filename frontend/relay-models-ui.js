// 挟持模型的编辑仅修改连接草稿；保存与密钥处理继续经过统一配置入口。
(() => {
  const modes = new WeakMap();

  /** 返回默认模型和额外模型的去重列表，默认项自动加入并且不能在此单独删除。 */
  function entries(profile) {
    return [...new Set([profile.model?.trim(), ...(profile.relayModels || [])].filter(Boolean))];
  }

  /** 新建带文本的界面元素，模型 ID 和服务显示名称始终作为纯文本处理。 */
  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  /** 校验并添加一个额外模型；不修改默认模型、不发起测试请求，重复输入只显示提示。 */
  function addModel(value, root, context) {
    const model = value.trim();
    const profile = context.profile;
    if (!model || [...model].length > 200 || /[\u0000-\u001f\u007f-\u009f]/u.test(model)) {
      window.notices.show('relay-models', '请输入有效的挟持模型 ID，最多 200 字符。', 'error');
      return;
    }
    if (entries(profile).includes(model)) {
      window.notices.show('relay-models', '此模型已在挟持列表中。', 'info');
      return;
    }
    if ((profile.relayModels || []).length >= 32) {
      window.notices.show('relay-models', '每个连接最多额外添加 32 个挟持模型。', 'error');
      return;
    }
    profile.relayModels = [...(profile.relayModels || []), model];
    window.notices.clear('relay-models');
    context.onChange();
    render(root, context);
    (root.querySelector('#relay-model-input') || root.querySelector('#relay-model-select-trigger'))?.focus();
  }

  /** 绘制共享连接的模型列表与添加入口，复用已拉取目录和统一白底下拉，列表本身不触发网络请求。 */
  function render(root, context) {
    const {profile, catalog} = context;
    const models = entries(profile);
    const isManual = modes.get(profile) ?? !catalog?.models.length;
    root.innerHTML = '<div class="field-label"><h3 id="relay-models-label">挟持模型列表</h3><span id="relay-model-count" class="quiet-meta"></span></div><p class="hint relay-model-intro">在 Codex 中直接切换，共用此连接的地址、Key 和思考等级。</p><ul id="relay-model-list" class="relay-model-list" aria-labelledby="relay-models-label"></ul><div class="field-label relay-add-label"><label for="relay-model-input">添加模型</label><button id="relay-model-mode" class="text-button" type="button"></button></div><div class="model-row"><div id="relay-model-control" class="model-input"></div><button id="add-relay-model" class="secondary" type="button">添加到列表</button></div><p class="hint">默认模型自动加入；其他模型可从服务列表选择，也可手动填写 ID。</p>';
    root.querySelector('#relay-model-count').textContent = `${models.length} 个模型`;
    const list = root.querySelector('#relay-model-list');
    for (const model of models) {
      const item = element('li', 'relay-model-item');
      item.append(element('code', '', model));
      if (model === profile.model.trim()) item.append(element('span', 'relay-default-tag', '默认模型'));
      else {
        const remove = element('button', 'icon-button relay-remove', '×');
        remove.type = 'button';
        remove.setAttribute('aria-label', `移除挟持模型 ${model}`);
        remove.title = '从挟持列表移除';
        remove.onclick = () => {
          if (remove.disabled) return;
          profile.relayModels = (profile.relayModels || []).filter(value => value !== model);
          context.onChange();
          render(root, context);
          (root.querySelector('.relay-remove') || root.querySelector('#relay-model-mode')).focus();
        };
        item.append(remove);
      }
      list.append(item);
    }
    if (!models.length) list.append(element('li', 'relay-model-empty', '先选择默认模型，或在下面添加挟持模型。'));
    const toggle = root.querySelector('#relay-model-mode');
    toggle.textContent = isManual ? '从服务列表选择' : '手动输入';
    toggle.setAttribute('aria-label', isManual ? '从服务列表添加挟持模型' : '手动输入挟持模型');
    toggle.onclick = () => { modes.set(profile, !isManual); render(root, context); };
    const holder = root.querySelector('#relay-model-control');
    const add = root.querySelector('#add-relay-model');
    let input;
    if (isManual) {
      input = element('input');
      input.id = 'relay-model-input';
      input.autocomplete = 'off';
      input.placeholder = '输入此服务支持的模型 ID';
      input.onkeydown = event => {
        if (event.key === 'Enter' && !event.isComposing) { event.preventDefault(); if (!add.disabled) addModel(input.value, root, context); }
      };
      holder.append(input);
    } else {
      input = element('select');
      input.id = 'relay-model-select';
      root.querySelector('.relay-add-label label').htmlFor = input.id;
      holder.append(input);
      const available = (catalog?.models || []).filter(item => !models.includes(item.id));
      input.append(new Option(available.length ? '选择要添加的模型' : '请先刷新上方模型列表，或手动输入', ''));
      for (const model of available) input.append(new Option(model.name && model.name !== model.id ? `${model.name} · ${model.id}` : model.id, model.id));
      input.disabled = !available.length;
      if (available.length > 8) {
        const search = element('input', 'search');
        search.placeholder = '搜索要添加的模型';
        search.setAttribute('aria-label', '筛选挟持模型候选');
        search.oninput = () => {
          const current = input.value;
          const query = search.value.toLowerCase();
          input.replaceChildren(new Option('选择要添加的模型', ''));
          for (const model of available.filter(item => item.id === current || `${item.id} ${item.name || ''}`.toLowerCase().includes(query))) input.append(new Option(model.name && model.name !== model.id ? `${model.name} · ${model.id}` : model.id, model.id));
          input.value = current;
          window.selectUI.refresh();
        };
        holder.parentElement.after(search);
      }
    }
    add.disabled = true;
    /** 仅在有候选值时启用添加按钮，刷新和保存期间仍由页面统一锁定控件。 */
    const updateAdd = () => { add.disabled = !input.value.trim(); };
    input.addEventListener('input', updateAdd);
    input.addEventListener('change', updateAdd);
    add.onclick = () => { if (!add.disabled) addModel(input.value, root, context); };
    window.selectUI.enhance(root);
  }

  window.relayModelsUI = {render};
})();
