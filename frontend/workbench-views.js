// 已确认原型的正式视图模板；只接收配置快照，不读取文件、发请求或预置连接。
(() => {
  /** 转义服务返回和用户填写的文本，连接名、模型 ID 不得成为可执行标记。 */
  function escape(value) { return String(value ?? '').replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[char]); }
  /** 从页面内固定图标集合引用图形，不加载外部图标或动态资源。 */
  function icon(name) { return `<svg viewBox="0 0 24 24" aria-hidden="true"><use href="#i-${name}"/></svg>`; }
  /** 按连接标识生成稳定身份色，不把颜色当作健康状态或协议类型。 */
  function tone(name) { let hash = 0; for (const char of name) hash = (Math.imul(hash,31) + char.codePointAt(0)) >>> 0; return ['teal','slate','blue','amber','violet','rose'][hash % 6]; }
  /** 生成同款卡片内部分组头部，仅在调用方明确提供时显示副说明或右侧操作。 */
  function heading(name, title, description = '', action = '') {
    return `<div class="form-section-heading"><span>${icon(name)}</span><div><strong>${title}</strong>${description ? `<small>${description}</small>` : ''}</div>${action}</div>`;
  }
  /** 返回高级覆盖是否生效；旧配置缺少开关时保留其既有参数，新配置显式关闭。 */
  function advancedEnabled(profile) { return profile.advancedOverride !== false; }
  /** 绘制连接表单、模型容器、用途与高级参数；非活动页隐藏但不丢失输入和草稿。 */
  function settings(name, profile) {
    const p = profile, enabled = advancedEnabled(p);
    return `<section id="panel-connection" class="settings-card" role="tabpanel" aria-labelledby="tab-connection" hidden>
      ${heading('link','服务连接')}
      <label class="field">连接名称<input id="name" value="${escape(name)}" autocomplete="off"></label>
      <div class="endpoint-field"><label class="field">API 地址<input id="baseUrl" class="mono" type="url" value="${escape(p.baseUrl)}" autocomplete="off"></label><div class="protocol-field"><label for="protocol" class="sr-only">API 协议</label><select id="protocol">${[['responses','Responses 透传'],['compatible','Chat Completions'],['anthropic','Claude 原生'],['gemini','Gemini 原生']].map(([value,label]) => `<option value="${value}" ${p.protocol === value ? 'selected' : ''}>${label}</option>`).join('')}</select></div></div>
      <div class="form-divider"></div>${heading('lock','身份验证')}
      <label class="field">API Key<span class="key-saved">${p.hasKey ? '已保存' : p.apiKeyEnv ? '环境变量' : '未保存'}</span><span class="input-wrap key-input"><input id="apiKey" type="password" value="${escape(p.apiKey)}" placeholder="${p.hasKey ? '留空保留已保存的 Key' : p.apiKeyEnv ? '留空沿用环境变量中的 Key' : '输入此服务的 API Key'}" autocomplete="new-password"><button id="toggle-key" class="icon-button" type="button" aria-label="显示输入的 Key" ${p.apiKey ? '' : 'disabled'}>${icon('eye')}</button></span></label>
    </section>
    <section id="panel-model" class="settings-card" role="tabpanel" aria-labelledby="tab-model" hidden>
      ${heading('brain','模型目录','勾选要使用的模型，星标设为默认。')}<div id="picker"></div>
      <div class="form-divider"></div><div class="reasoning-section">${heading('zap','思考等级','','<span id="reasoning-summary" class="settings-header-value"></span>')}<div id="reasoning-options" class="reasoning-options" role="radiogroup" aria-label="思考等级"></div></div>
    </section>
    <section id="panel-purpose" class="settings-card" role="tabpanel" aria-labelledby="tab-purpose" hidden>
      ${heading('branch','擅长与用途','帮助 Codex 在插件委派时选择连接。')}
      <label class="field">任务说明<textarea id="description" maxlength="300" rows="6" placeholder="例如：分析后端逻辑与边界条件，适合排错和代码审查。">${escape(p.description)}</textarea></label>
      <div class="description-meta"><span>作为委派参考，不替代明确指令</span><span id="purpose-count">${(p.description || '').length} / 300</span></div>
      <div class="suggestions"><span>快捷填入</span>${[['代码审查','blue'],['文档整理','teal'],['批量修改','violet']].map(([label,color],index) => `<button type="button" class="purpose-tag tone-${color}" data-purpose="${index}">${icon('plus')}${label}</button>`).join('')}</div>
    </section>
    <section id="panel-advanced" class="settings-card" role="tabpanel" aria-labelledby="tab-advanced" hidden>
      ${heading('sliders','运行与响应','按需覆盖参数，未开启时保持连接默认行为。','<button id="reset-advanced" type="button" class="text-button">恢复默认</button>')}
      <div class="override-row"><strong>自定义参数覆盖<span id="override-label" class="badge ${enabled ? 'enabled' : ''}">${enabled ? '已开启' : '未开启'}</span></strong><button id="advanced-override" type="button" class="switch" role="switch" aria-label="自定义参数覆盖" aria-checked="${enabled}"><span></span></button></div>
      <fieldset id="advanced-fields" ${enabled ? '' : 'disabled'}><div class="form-divider"></div>${heading('zap','输出与响应')}
        <label class="field">插件最大输出长度<input id="maxTokens" type="number" min="128" max="32000" step="1" value="${p.maxTokens ?? 4096}"><small>128–32,000 tokens；透传请求仍由 Codex 与上游决定。</small></label>
        <div class="radio-options"><label><input type="radio" name="response-mode" value="stream" ${p.stream !== false ? 'checked' : ''}>流式响应<span class="badge">默认</span></label><label><input type="radio" name="response-mode" value="complete" ${p.stream === false ? 'checked' : ''}>完整响应</label></div>
        <div class="form-divider"></div>${heading('clock','超时保护')}<div class="timeout-fields">${[['firstResponseTimeoutSeconds','首个数据等待',10,600,'秒',180],['streamIdleTimeoutSeconds','响应中断等待',10,600,'秒',120],['taskTimeoutMinutes','任务总时长',1,60,'分钟',15]].map(([key,label,min,max,unit,value]) => `<label class="field">${label}（${min}–${max} ${unit}）<input id="${key}" type="number" min="${min}" max="${max}" step="1" value="${p[key] ?? value}"></label>`).join('')}</div>
      </fieldset>
    </section>`;
  }
  /** 总览卡片只选择连接，模型标签仅预览；独立管理和保存按钮不嵌入选择按钮。 */
  function overview(models, selected, dirty, query = '') {
    const entries = Object.entries(models);
    if (!entries.length) return '<div class="empty"><h2>还没有连接</h2><p>添加模型服务连接，选择要使用的模型。</p><button type="button" class="primary" data-add-connection>添加连接</button></div>';
    const filter = query.toLowerCase().trim(), matching = entries.filter(([name,p]) => `${name} ${window.modelPickerUI.selectedModels(p).map(id => `${id} ${window.modelPickerUI.nameFor(p,id)}`).join(' ')}`.toLowerCase().includes(filter));
    return `<div class="overview-toolbar"><label class="search-box">${icon('search')}<input id="connection-search" placeholder="搜索连接或已选模型" aria-label="搜索连接" value="${escape(query)}" autocomplete="off"></label><span class="badge">${entries.length} 个模型连接</span></div><div class="provider-grid">${matching.length ? matching.map(([name,p]) => {
      const ids = window.modelPickerUI.selectedModels(p), preview = [p.model,...ids.filter(id => id !== p.model)].filter(Boolean).slice(0,3);
      const changed = dirty.has(name) || !p.savedName;
      return `<article class="provider-card ${name === selected ? 'selected' : ''}"><button type="button" class="provider-select" data-select-connection="${escape(name)}" aria-label="选择连接 ${escape(name)}" aria-pressed="${name === selected}"></button>
        <div class="provider-top"><div class="provider-title"><span class="connection-avatar tone-${tone(name)}" aria-hidden="true">${escape([...name][0]?.toUpperCase())}</span><div class="provider-copy"><div class="provider-name"><h3>${escape(name)}</h3>${name === selected ? `<span class="badge warm provider-selection">${icon('check')}已选连接</span>` : ''}${changed ? '<span class="badge amber">草稿</span>' : ''}</div><p>${escape(p.baseUrl)}</p></div></div><button type="button" class="secondary provider-manage" data-manage-connection="${escape(name)}">管理连接${icon('chevron')}</button></div>
        <div class="provider-bottom"><div class="model-chips">${preview.map(id => `<span class="model-chip model-preview ${id === p.model ? 'is-default' : ''}" title="${escape(id)}">${id === p.model ? icon('star') : ''}<span class="model-preview-label">${escape(window.modelPickerUI.nameFor(p,id))}</span></span>`).join('') || '<span class="muted">尚未选择模型</span>'}<span class="badge">${ids.length} 个已选模型</span></div><span class="provider-note">${p.description ? '已填写任务分工' : '尚未填写任务分工'}</span></div>
        ${changed ? `<div class="provider-save-row"><span>此连接有未保存修改 · 保存后生效</span><button type="button" class="primary provider-save" data-save-connection="${escape(name)}" aria-label="保存连接 ${escape(name)} 的草稿">${icon('check')}保存连接草稿</button></div>` : ''}</article>`;
    }).join('') : '<div class="empty-state"><strong>还没有匹配的连接</strong><p>换一个搜索词，或添加新的连接。</p></div>'}</div>`;
  }
  window.workbenchViews = {escape,icon,tone,settings,overview,advancedEnabled};
})();
