// 模型目录、挟持勾选和默认模型共用一个编辑器；所有修改先写入连接草稿。
(() => {
  const states = new WeakMap();
  const maxExtraModels = 32;

  /** 返回包含默认模型的去重集合，兼容尚未配置默认项的旧草稿。 */
  function selectedModels(profile) {
    return [...new Set([profile.model?.trim(), ...(profile.relayModels || [])].filter(Boolean))];
  }

  /** 按连接对象保留搜索、筛选和手动候选；连接重命名不会丢失当前操作位置。 */
  function editorState(profile) {
    if (!states.has(profile)) states.set(profile, {query: '', selectedOnly: false, custom: new Set()});
    return states.get(profile);
  }

  /** 创建纯文本节点，来自模型服务的名称和 ID 不能作为 HTML 执行。 */
  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  /** 优先显示用户命名，缺省时沿用服务名称或 ID；仅读取自有属性，兼容特殊模型 ID。 */
  function nameFor(profile, id, fallback = id) {
    return Object.hasOwn(profile.modelNames || {}, id) && profile.modelNames[id] ? profile.modelNames[id] : fallback;
  }

  /** 合并已配置、手动添加与远端候选；目录未返回的配置仍显示，不自动移除。 */
  function modelEntries(context) {
    const remote = new Map((context.catalog?.models || []).map(item => [item.id, item]));
    const ids = new Set([...selectedModels(context.profile), ...editorState(context.profile).custom, ...remote.keys()]);
    return [...ids].map(id => ({id, name: nameFor(context.profile, id, remote.get(id)?.name || id), remote: remote.has(id)}));
  }

  /** 更新列表外的数量与默认值，让搜索隐藏默认行时也能确认当前配置。 */
  function updateSummary(root, context) {
    const count = selectedModels(context.profile).length;
    root.dataset.selectedCount = count;
    const current = root.querySelector('#current-default-model');
    current.textContent = nameFor(context.profile, context.profile.model) || '尚未设置，请点击「设为默认」';
    current.title = context.profile.model || '';
    root.querySelector('#model-all-count').textContent = modelEntries(context).length;
    root.querySelector('#model-selected-count').textContent = count;
    root.querySelector('#model-selection-count').textContent = `已选 ${count} / ${context.profile.model ? 33 : 32}`;
    const state = editorState(context.profile);
    root.querySelector('#model-filter-all').setAttribute('aria-pressed', String(!state.selectedOnly));
    root.querySelector('#model-filter-selected').setAttribute('aria-pressed', String(state.selectedOnly));
    root.querySelector('#clear-model-search').hidden = !state.query;
  }

  /** 就地更新勾选、默认及移除入口；未选中的远端候选保留在服务目录中 */
  function updateRow(row, profile, chosen) {
    const isDefault = row.dataset.modelId === profile.model;
    row.classList.toggle('is-selected', chosen.has(row.dataset.modelId));
    const check = row.querySelector('input');
    check.checked = chosen.has(row.dataset.modelId);
    check.disabled = isDefault;
    check.title = isDefault ? '默认模型始终保留在挟持列表中' : '在 Codex 中显示此模型';
    row.querySelector('[data-rename-model]').hidden = !chosen.has(row.dataset.modelId);
    row.querySelector('[data-remove-model]').hidden = !chosen.has(row.dataset.modelId) && row.dataset.remoteModel === 'true';
    const button = row.querySelector('[data-set-default]');
    button.textContent = isDefault ? '默认模型' : '设为默认';
    button.classList.toggle('is-default', isDefault);
    button.setAttribute('aria-pressed', String(isDefault));
    button.setAttribute('aria-label', isDefault ? `${row.dataset.modelId}，默认模型` : `将 ${row.dataset.modelId} 设为默认模型`);
  }

  /** 生成可勾选、编辑、移除和设默认的模型行，显示名称与真实 ID 不同时分别展示 */
  function modelRow(item, profile, chosen) {
    const row = element('div', 'model-catalog-row');
    row.dataset.modelId = item.id;
    row.dataset.remoteModel = String(item.remote);
    const label = element('label', 'model-catalog-choice');
    const check = element('input');
    check.type = 'checkbox';
    check.setAttribute('aria-label', `在 Codex 中显示 ${item.id}`);
    const copy = element('span', 'model-catalog-copy');
    copy.append(element('span', 'model-catalog-name', item.name));
    if (item.name !== item.id) copy.append(element('code', 'model-catalog-id', item.id));
    copy.title = item.name === item.id ? item.id : `${item.name}\n${item.id}`;
    label.append(check, copy);
    row.append(label);
    const edit = element('button', 'icon-button model-rename-button');
    edit.type = 'button';
    edit.dataset.renameModel = '';
    edit.title = '编辑模型 ID、显示名称与上下文';
    edit.setAttribute('aria-label', `编辑模型 ${item.id}`);
    edit.innerHTML = '<svg aria-hidden="true" viewBox="0 0 24 24"><path d="m15 5 4 4M4 20l4-1 12-12a2.8 2.8 0 0 0-4-4L4 15Z"/></svg>';
    row.append(edit);
    const remove = element('button', 'icon-button model-remove-button');
    remove.type = 'button';
    remove.dataset.removeModel = '';
    remove.title = '从当前连接移除模型';
    remove.setAttribute('aria-label', `移除模型 ${item.id}`);
    remove.innerHTML = '<svg aria-hidden="true" viewBox="0 0 24 24"><path d="M3 6h18M9 6V4h6v2M5 6l1 14h12l1-14M10 10v6M14 10v6"/></svg>';
    row.append(remove);
    const button = element('button', 'model-default-button');
    button.type = 'button';
    button.dataset.setDefault = '';
    row.append(button);
    updateRow(row, profile, chosen);
    return row;
  }

  /** 绘制搜索结果与空状态；仅在筛选或目录变化时重建，模型最多沿用接口的 1000 项上限。 */
  function renderList(root, context) {
    const state = editorState(context.profile);
    const query = state.query.trim().toLocaleLowerCase();
    const chosen = new Set(selectedModels(context.profile));
    const models = modelEntries(context).filter(item => (!state.selectedOnly || chosen.has(item.id)) && `${item.id} ${item.name}`.toLocaleLowerCase().includes(query));
    const list = root.querySelector('#model-catalog-list');
    const fragment = document.createDocumentFragment();
    for (const item of models) fragment.append(modelRow(item, context.profile, chosen));
    if (!models.length) {
      const empty = element('div', 'model-catalog-empty');
      empty.append(element('strong', '', query ? '没有匹配的模型' : state.selectedOnly ? '还没有选中模型' : '先获取此连接的模型列表'));
      empty.append(element('span', '', query ? '试试其他关键词，或手动添加模型 ID。' : state.selectedOnly ? '切换到「全部」，勾选需要的模型。' : '点击「刷新列表」，也可以直接手动添加。'));
      fragment.append(empty);
    }
    list.replaceChildren(fragment);
    list.scrollTop = 0;
    root.querySelector('#model-result-count').textContent = query ? `${models.length} 个匹配` : `${models.length} 个模型`;
    updateSummary(root, context);
  }

  /** 更新草稿后的可见状态；仅看已选时移除被取消的行并将键盘焦点留在列表中。 */
  function selectionChanged(root, context, changedRow) {
    context.onChange();
    const chosen = new Set(selectedModels(context.profile));
    for (const row of root.querySelectorAll('.model-catalog-row')) updateRow(row, context.profile, chosen);
    if (editorState(context.profile).selectedOnly && changedRow && !chosen.has(changedRow.dataset.modelId)) {
      const neighbor = changedRow.nextElementSibling || changedRow.previousElementSibling;
      changedRow.remove();
      if (neighbor) neighbor.querySelector('input:not(:disabled), button').focus({preventScroll: true});
      else {
        renderList(root, context);
        root.querySelector('#model-filter-all').focus({preventScroll: true});
      }
    }
    updateSummary(root, context);
    const size = root.querySelectorAll('.model-catalog-row').length;
    root.querySelector('#model-result-count').textContent = editorState(context.profile).query.trim() ? `${size} 个匹配` : `${size} 个模型`;
  }

  /** 勾选仅影响挟持列表；默认项不可取消，达到额外 32 项时撤销本次勾选并提示。 */
  function toggleModel(root, context, row, checked) {
    const {profile} = context;
    const id = row.dataset.modelId;
    if (context.isBusy() || id === profile.model) return;
    const extra = selectedModels(profile).filter(value => value !== profile.model && value !== id);
    if (checked) extra.push(id);
    if (extra.length > maxExtraModels) {
      row.querySelector('input').checked = false;
      window.notices.show('model-picker', '最多选择 32 个额外模型，请先取消勾选一个模型。', 'error');
      return;
    }
    // 取消手动模型后仍将其保留为候选，用户可立即重新勾选。
    editorState(profile).custom.add(id);
    profile.relayModels = extra;
    window.notices.clear('model-picker');
    selectionChanged(root, context, row);
  }

  /** 设置默认时保留原默认及所有已选模型；如果集合已满则不隐式删除任何配置。 */
  function setDefault(root, context, id) {
    if (context.isBusy() || id === context.profile.model) return;
    const extra = selectedModels(context.profile).filter(value => value !== id);
    if (extra.length > maxExtraModels) {
      window.notices.show('model-picker', '挟持列表已满，请先取消勾选一个模型，再设置新的默认模型。', 'error');
      return;
    }
    context.profile.model = id;
    context.profile.relayModels = extra;
    window.notices.clear('model-picker');
    selectionChanged(root, context);
  }

  /** 从草稿中移除模型、显示名称与上下文设置，默认项由剩余已选项接替；远端候选仍保留 */
  function removeModel(root, context, id) {
    if (context.isBusy() || !modelEntries(context).some(item => item.id === id)) return false;
    const {profile} = context;
    const remaining = selectedModels(profile).filter(model => model !== id);
    if (profile.model === id) profile.model = remaining[0] || '';
    profile.relayModels = remaining.filter(model => model !== profile.model);
    const names = Object.assign(Object.create(null), profile.modelNames);
    delete names[id];
    profile.modelNames = names;
    const windows = Object.assign(Object.create(null), profile.modelContextWindows);
    delete windows[id];
    profile.modelContextWindows = windows;
    const modes = Object.assign(Object.create(null), profile.modelCompatibility);
    delete modes[id];
    profile.modelCompatibility = modes;
    editorState(profile).custom.delete(id);
    const list = root.querySelector('#model-catalog-list');
    const scroll = list.scrollTop;
    context.onChange();
    renderList(root, context);
    list.scrollTop = scroll;
    window.notices.show('model-picker', profile.model ? '模型已从当前连接移除，保存配置后生效' : '模型已移除，请先选择或添加默认模型，再保存配置', profile.model ? 'success' : 'warning');
    return true;
  }

  /** 显示移除影响，明确默认模型的接替项或空草稿限制；确认前不改动连接配置 */
  function openModelRemoval(root, context, id) {
    if (context.isBusy()) return;
    const entry = modelEntries(context).find(item => item.id === id);
    if (!entry || (entry.remote && !selectedModels(context.profile).includes(id))) return;
    const dialog = root.querySelector('#remove-picker-model-dialog');
    const rows = [...root.querySelectorAll('.model-catalog-row')];
    const index = rows.findIndex(row => row.dataset.modelId === id);
    dialog.dataset.modelId = id;
    dialog.dataset.neighborId = (rows[index + 1] || rows[index - 1])?.dataset.modelId || '';
    let description = `从当前连接移除“${entry.name}”${entry.name === id ? '' : `（${id}）`}？`;
    if (context.profile.model === id) {
      const replacement = selectedModels(context.profile).find(model => model !== id);
      description += replacement ? `默认模型将切换为“${nameFor(context.profile, replacement)}”，保存配置后生效` : '移除后需先选择或添加新的默认模型，才能保存配置';
    } else description += '保存配置后生效';
    if (entry.remote) description += '；服务目录中仍会保留此模型，可重新勾选';
    root.querySelector('#remove-picker-model-description').textContent = description;
    dialog.returnValue = '';
    dialog.showModal();
    root.querySelector('#cancel-remove-picker-model').focus();
  }

  /** 绑定移除确认与取消，关闭后将键盘焦点还给原行或相邻行，空列表返回手动添加入口 */
  function bindRemovalDialog(root, context) {
    const dialog = root.querySelector('#remove-picker-model-dialog');
    root.querySelector('#cancel-remove-picker-model').onclick = () => dialog.close();
    root.querySelector('#remove-picker-model-form').onsubmit = event => {
      event.preventDefault();
      if (removeModel(root, context, dialog.dataset.modelId)) dialog.close('removed');
    };
    dialog.onclose = () => {
      const id = dialog.returnValue === 'removed' ? dialog.dataset.neighborId : dialog.dataset.modelId;
      const rows = [...root.querySelectorAll('.model-catalog-row')];
      const row = rows.find(item => item.dataset.modelId === id) || rows[0];
      (row?.querySelector('[data-remove-model]:not([hidden])') || row?.querySelector('input:not(:disabled), [data-set-default]') || root.querySelector('#add-manual-model')).focus({preventScroll: true});
    };
  }

  /** 校验单个手动 ID 并加入草稿，首个模型同时设为默认；错误留在弹窗内便于修正。 */
  function addManual(root, context) {
    if (context.isBusy()) return;
    const input = root.querySelector('#manual-model-id');
    const id = input.value.trim();
    const profile = context.profile;
    const chosen = selectedModels(profile);
    let error = '';
    if (!id || [...id].length > 200 || /[\u0000-\u001f\u007f-\u009f]/u.test(id)) error = '请输入有效的模型 ID，最多 200 字符，不包含控制字符';
    else if (chosen.includes(id)) error = '此模型已选中，无需重复添加';
    else if (profile.model && chosen.length >= maxExtraModels + 1) error = '列表已满，请先取消勾选一个模型';
    root.querySelector('#manual-model-error').textContent = error;
    input.setAttribute('aria-invalid', String(Boolean(error)));
    if (error) { input.focus(); return; }
    if (!profile.model) profile.model = id;
    else profile.relayModels = [...chosen.filter(value => value !== profile.model), id];
    const state = editorState(profile);
    state.custom.add(id);
    state.query = '';
    state.selectedOnly = true;
    root.querySelector('#model-search').value = '';
    root.querySelector('#manual-model-dialog').close('added');
    context.onChange();
    renderList(root, context);
    const row = [...root.querySelectorAll('.model-catalog-row')].find(item => item.dataset.modelId === id);
    row?.querySelector('[data-set-default]').focus({preventScroll: true});
    row?.scrollIntoView({block: 'nearest'});
    window.notices.show('model-picker', '模型已加入已选列表，保存配置后生效。');
  }

  /** 初始化独立的手动输入弹窗，取消不会修改配置，Esc 与关闭按钮均返回添加入口。 */
  function bindManualDialog(root, context) {
    const dialog = root.querySelector('#manual-model-dialog');
    const input = root.querySelector('#manual-model-id');
    root.querySelector('#add-manual-model').onclick = () => {
      if (context.isBusy()) return;
      input.value = '';
      input.removeAttribute('aria-invalid');
      root.querySelector('#manual-model-error').textContent = '';
      root.querySelector('#manual-model-description').textContent = context.profile.model ? '填写此服务支持的模型 ID，添加后自动勾选，可在列表中设为默认' : '填写此服务支持的模型 ID，第一个模型将同时设为默认模型';
      dialog.returnValue = '';
      dialog.showModal();
      input.focus();
    };
    root.querySelector('#cancel-manual-model').onclick = () => dialog.close();
    root.querySelector('#manual-model-form').onsubmit = event => { event.preventDefault(); addManual(root, context); };
    input.oninput = () => { input.removeAttribute('aria-invalid'); root.querySelector('#manual-model-error').textContent = ''; };
    dialog.onclose = () => { if (dialog.returnValue !== 'added') root.querySelector('#add-manual-model').focus({preventScroll: true}); };
  }

  /** 更新上下文说明及自动压缩阈值；未知上游容量保守回退，输入只修改弹窗草稿 */
  function updateContextHint(root) {
    const field = root.querySelector('#model-context-window');
    const size = field.value === '' ? 32768 : Number(field.value);
    const valid = Number.isInteger(size) && size >= 4096 && size <= 2000000;
    root.querySelector('#model-context-help').textContent = valid
      ? `${size.toLocaleString('zh-CN')} tokens 上下文，约 ${(Math.floor(size * 0.9)).toLocaleString('zh-CN')} tokens 自动压缩。请按上游实际容量填写，保存后重启 Codex 生效`
      : '填写 4096–2000000 的整数 tokens；留空使用保守默认值 32768';
  }

  /** 一次校验并提交 ID、名称、容量与兼容策略；改 ID 时迁移独立设置，失败或取消不改变原选择 */
  function bindModelDialog(root, context) {
    const dialog = root.querySelector('#rename-model-dialog');
    const modelID = root.querySelector('#edit-model-id');
    const input = root.querySelector('#model-display-name');
    const form = root.querySelector('#rename-model-form');
    const error = root.querySelector('#model-name-error');
    const contextInput = root.querySelector('#model-context-window');
    root.querySelector('#cancel-model-name').onclick = () => dialog.close();
    root.querySelector('#reset-model-name').onclick = () => { input.value = ''; input.removeAttribute('aria-invalid'); error.textContent = ''; input.focus(); };
    input.oninput = () => { input.removeAttribute('aria-invalid'); error.textContent = ''; };
    modelID.oninput = () => { modelID.removeAttribute('aria-invalid'); error.textContent = ''; input.placeholder = modelID.value.trim(); };
    contextInput.oninput = () => { contextInput.removeAttribute('aria-invalid'); error.textContent = ''; updateContextHint(root); };
    form.onsubmit = event => {
      event.preventDefault();
      const id = dialog.dataset.modelId;
      const {profile} = context;
      const chosen = selectedModels(profile);
      if (context.isBusy() || !chosen.includes(id)) return;
      const nextID = modelID.value.trim();
      let idError = '';
      if (!nextID || [...nextID].length > 200 || /[\u0000-\u001f\u007f-\u009f]/u.test(nextID)) idError = '模型 ID 不能为空、超过 200 字符或包含控制字符';
      else if (nextID !== id && chosen.includes(nextID)) idError = '该模型 ID 已在已选列表中，请直接编辑对应模型';
      if (idError) {
        error.textContent = idError;
        modelID.setAttribute('aria-invalid', 'true');
        modelID.focus();
        return;
      }
      const value = input.value.trim();
      if ([...value].length > 80 || /[\u0000-\u001f\u007f-\u009f]/u.test(value)) {
        error.textContent = '显示名称最多 80 字符，不能包含控制字符';
        input.setAttribute('aria-invalid', 'true');
        input.focus();
        return;
      }
      const size = contextInput.value === '' && !contextInput.validity.badInput ? 0 : Number(contextInput.value);
      if (!Number.isInteger(size) || (size !== 0 && (size < 4096 || size > 2000000)) || (contextInput.value !== '' && size === 0) || contextInput.validity.badInput) {
        error.textContent = '上下文长度必须为 4096–2000000 的整数 tokens，留空使用默认值';
        contextInput.setAttribute('aria-invalid', 'true');
        contextInput.focus();
        return;
      }
      const modes = Object.assign(Object.create(null), profile.modelCompatibility);
      const previousMode = modes[id] || '';
      const mode = root.querySelector('#model-compatibility').value;
      delete modes[id];
      if (mode) modes[nextID] = mode;
      else delete modes[nextID];
      const windows = Object.assign(Object.create(null), profile.modelContextWindows);
      const previousSize = windows[id] || 0;
      delete windows[id];
      if (size) windows[nextID] = size;
      else delete windows[nextID];
      const names = Object.assign(Object.create(null), profile.modelNames);
      const previous = names[id] || '';
      delete names[id];
      if (value && value !== nextID) names[nextID] = value;
      else delete names[nextID];
      if (nextID !== id || (names[nextID] || '') !== previous || size !== previousSize || mode !== previousMode) {
        // 更换 ID 是替换同一个已选位置，默认身份与其他选择均保留，不消耗额外名额。
        if (profile.model === id) profile.model = nextID;
        profile.relayModels = [...new Set((profile.relayModels || []).map(model => model === id ? nextID : model))].filter(model => model !== profile.model);
        profile.modelNames = names;
        profile.modelContextWindows = windows;
        profile.modelCompatibility = modes;
        const state = editorState(profile);
        state.custom.delete(id);
        state.custom.add(nextID);
        // 旧 ID 的搜索条件可能隐藏修正后的模型，此时清空搜索以便立即确认结果。
        if (nextID !== id) { state.query = ''; root.querySelector('#model-search').value = ''; }
        dialog.dataset.modelId = nextID;
        context.onChange();
        const list = root.querySelector('#model-catalog-list');
        const scroll = list.scrollTop;
        renderList(root, context);
        list.scrollTop = scroll;
        window.notices.show('model-picker', nextID !== id ? '模型 ID 已修改。保存后请求将使用新 ID；重启 Codex 后重新选择更新的模型' : size !== previousSize || mode !== previousMode ? '模型设置已更新，保存配置并重启 Codex 后生效' : '显示名称已更新，保存配置后生效');
      }
      dialog.close();
    };
    dialog.onclose = () => {
      window.selectUI.close();
      const row = [...root.querySelectorAll('.model-catalog-row')].find(item => item.dataset.modelId === dialog.dataset.modelId);
      (row?.querySelector('[data-rename-model]') || root.querySelector('#model-search')).focus({preventScroll: true});
    };
  }

  /** 打开已选模型编辑器，载入独立容量、名称与兼容策略；旧配置留空以显示默认容量 */
  function openModelEditor(root, context, id) {
    if (context.isBusy() || !selectedModels(context.profile).includes(id)) return;
    const dialog = root.querySelector('#rename-model-dialog');
    const input = root.querySelector('#model-display-name');
    dialog.dataset.modelId = id;
    const modelID = root.querySelector('#edit-model-id');
    modelID.value = id;
    modelID.removeAttribute('aria-invalid');
    input.value = Object.hasOwn(context.profile.modelNames || {}, id) ? context.profile.modelNames[id] : '';
    input.placeholder = id;
    input.removeAttribute('aria-invalid');
    const contextInput = root.querySelector('#model-context-window');
    contextInput.value = Object.hasOwn(context.profile.modelContextWindows || {}, id) ? context.profile.modelContextWindows[id] : '';
    contextInput.removeAttribute('aria-invalid');
    const compatibility = root.querySelector('#model-compatibility');
    compatibility.value = Object.hasOwn(context.profile.modelCompatibility || {}, id) ? context.profile.modelCompatibility[id] : '';
    compatibility.disabled = context.profile.protocol !== 'compatible';
    root.querySelector('#model-compatibility-fields').hidden = compatibility.disabled;
    window.selectUI.refresh();
    updateContextHint(root);
    root.querySelector('#model-name-error').textContent = '';
    dialog.showModal();
    modelID.focus();
    modelID.select();
  }

  /** 方向键在同类的勾选、编辑、移除或默认按钮之间移动，跳过隐藏与禁用控件 */
  function navigateRows(event, list) {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const row = event.target.closest('.model-catalog-row');
    if (!row) return;
    const selector = ['input:not(:disabled)', '[data-rename-model]:not([hidden])', '[data-remove-model]:not([hidden])', '[data-set-default]'].find(value => event.target.matches(value));
    if (!selector) return;
    const controls = [...list.querySelectorAll(selector)];
    const index = controls.indexOf(event.target);
    if (index < 0) return;
    const next = {ArrowDown: Math.min(index + 1, controls.length - 1), ArrowUp: Math.max(0, index - 1), Home: 0, End: controls.length - 1}[event.key];
    event.preventDefault();
    controls[next].focus({preventScroll: true});
    controls[next].scrollIntoView({block: 'nearest'});
  }

  /** 绘制统一模型编辑器；只读刷新由外层鉴权入口执行，其余操作完全在本地草稿中完成。 */
  function render(root, context) {
    root.innerHTML = `
      <div class="model-current"><span>默认模型</span><code id="current-default-model"></code><span class="model-current-hint">用于插件委派与「跟随 App」</span></div>
      <div class="model-catalog-toolbar">
        <div class="model-search-wrap"><svg aria-hidden="true" viewBox="0 0 24 24"><circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 4.5 4.5"/></svg><input id="model-search" type="search" autocomplete="off" aria-label="搜索模型" placeholder="搜索模型名称或 ID"><button id="clear-model-search" type="button" class="icon-button" aria-label="清空模型搜索" hidden>×</button></div>
        <div class="model-filters" role="group" aria-label="模型筛选"><button id="model-filter-all" type="button">全部 <span id="model-all-count"></span></button><button id="model-filter-selected" type="button">已选 <span id="model-selected-count"></span></button></div>
        <div class="model-catalog-actions"><button id="pull-models" class="secondary" type="button"><svg aria-hidden="true"><use href="#i-refresh"/></svg>刷新列表</button><button id="add-manual-model" class="secondary" type="button"><span aria-hidden="true">＋</span>手动添加</button></div>
      </div>
      <div class="model-catalog" role="group" aria-label="模型列表"><div class="model-catalog-head"><span>在 Codex 中显示</span><span id="model-result-count" role="status" aria-live="polite"></span></div><div id="model-catalog-list" class="model-catalog-list"></div></div>
      <div class="model-catalog-footer"><span id="model-selection-count" role="status" aria-live="polite"></span><span id="model-catalog-hint"></span></div>
      <dialog id="remove-picker-model-dialog" aria-labelledby="remove-picker-model-title" aria-describedby="remove-picker-model-description"><form id="remove-picker-model-form"><h2 id="remove-picker-model-title">移除模型？</h2><p id="remove-picker-model-description"></p><div class="dialog-actions"><button id="cancel-remove-picker-model" class="secondary" type="button" autofocus>取消</button><button class="primary danger" type="submit">确认移除</button></div></form></dialog>
      <dialog id="manual-model-dialog" aria-labelledby="manual-model-title" aria-describedby="manual-model-description"><form id="manual-model-form" novalidate><h2 id="manual-model-title">手动添加模型</h2><p id="manual-model-description"></p><label for="manual-model-id">模型 ID</label><input id="manual-model-id" autocomplete="off" placeholder="输入服务提供的完整模型 ID" aria-describedby="manual-model-error"><div id="manual-model-error" class="manual-model-error" role="status" aria-live="polite"></div><div class="dialog-actions"><button id="cancel-manual-model" class="secondary" type="button">取消</button><button class="primary" type="submit">添加模型</button></div></form></dialog>
      <dialog id="rename-model-dialog" aria-labelledby="rename-model-title" aria-describedby="rename-model-description"><form id="rename-model-form" novalidate><h2 id="rename-model-title">编辑模型</h2><p id="rename-model-description">服务返回的模型不正确时，在这里修正实际调用的模型 ID</p><div class="model-editor-fields"><label for="edit-model-id">上游模型 ID</label><input id="edit-model-id" autocomplete="off" aria-describedby="model-id-help model-name-error"><p id="model-id-help" class="hint model-edit-help">实际发送给上游的 model 值，请填写正确的完整 ID</p><label for="model-display-name">显示名称（可选）</label><input id="model-display-name" autocomplete="off" aria-describedby="model-display-help model-name-error"><p id="model-display-help" class="hint model-edit-help">仅用于 App 与 Codex 列表展示，留空使用模型默认名称</p><div id="model-compatibility-fields"><label for="model-compatibility">兼容策略</label><select id="model-compatibility" aria-describedby="model-compatibility-help"><option value="">自动（官方地址识别）</option><option value="generic">通用 Chat Completions</option><option value="gemini">Gemini</option><option value="deepseek">DeepSeek</option><option value="kimi">Kimi / Moonshot</option><option value="kimi-coding">Kimi Coding</option><option value="doubao">Doubao / 豆包</option><option value="minimax">MiniMax</option><option value="glm">GLM / 智谱</option></select><p id="model-compatibility-help" class="hint model-edit-help">用于 Chat Completions 接口。自定义地址默认通用；直转厂商接口时可指定对应策略。原生接口按接口类型处理</p></div><label for="model-context-window">上下文长度（tokens）</label><input id="model-context-window" type="number" inputmode="numeric" min="4096" max="2000000" step="1" placeholder="默认 32768，例如 128000" aria-describedby="model-context-help model-name-error"><p id="model-context-help" class="hint model-edit-help"></p><div id="model-name-error" class="manual-model-error" role="status" aria-live="polite"></div></div><div class="dialog-actions"><button id="reset-model-name" type="button" class="text-button">恢复默认名称</button><button id="cancel-model-name" type="button" class="secondary">取消</button><button type="submit" class="primary">保存修改</button></div></form></dialog>`;
    const state = editorState(context.profile);
    const search = root.querySelector('#model-search');
    search.value = state.query;
    search.oninput = () => { state.query = search.value; renderList(root, context); };
    root.querySelector('#clear-model-search').onclick = () => { state.query = ''; search.value = ''; renderList(root, context); search.focus(); };
    root.querySelector('#model-filter-all').onclick = () => { state.selectedOnly = false; renderList(root, context); };
    root.querySelector('#model-filter-selected').onclick = () => { state.selectedOnly = true; renderList(root, context); };
    root.querySelector('#pull-models').onclick = context.onRefresh;
    const list = root.querySelector('#model-catalog-list');
    search.onkeydown = event => {
      if (event.key !== 'ArrowDown' || event.isComposing) return;
      event.preventDefault();
      list.querySelector('input:not(:disabled), [data-set-default]')?.focus();
    };
    list.onkeydown = event => navigateRows(event, list);
    list.onchange = event => {
      const row = event.target.closest('.model-catalog-row');
      if (row && event.target.matches('input[type="checkbox"]')) toggleModel(root, context, row, event.target.checked);
    };
    list.onclick = event => {
      const button = event.target.closest('[data-set-default]');
      if (button) setDefault(root, context, button.closest('.model-catalog-row').dataset.modelId);
      const edit = event.target.closest('[data-rename-model]');
      if (edit) openModelEditor(root, context, edit.closest('.model-catalog-row').dataset.modelId);
      const remove = event.target.closest('[data-remove-model]');
      if (remove) openModelRemoval(root, context, remove.closest('.model-catalog-row').dataset.modelId);
    };
    bindManualDialog(root, context);
    window.selectUI.enhance(root);
    bindModelDialog(root, context);
    bindRemovalDialog(root, context);
    renderList(root, context);
    const catalog = context.catalog;
    root.querySelector('#model-catalog-hint').textContent = catalog ? `勾选加入挟持列表 · 默认模型始终保留${catalog.truncated ? ' · 服务目录仅显示前 1000 项' : ''}` : '可刷新服务目录；已配置的模型始终保留。';
  }

  window.modelPickerUI = {render, nameFor};
})();
