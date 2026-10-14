import {clone, contextSettings, defaultContextSettings, isDirty, makeSnapshot} from './domain.mjs';

/** 创建首次使用的真实空工作区，不预置连接、模型或任何密钥。 */
export function emptyStore() {return {connections: {}, savedConnections: {}, plans: [], savedPlans: {}, selected: '', active: null, enabled: false};}

/** 读取模型映射的自有项，允许 __proto__、constructor 等合法模型 ID，不读取对象原型上的成员。 */
function modelValue(map, id) {return map && Object.hasOwn(map, id) ? map[id] : undefined;}

/** 将脱敏 Go 配置转换成原型组件的数据形状；稳定 ID 与显示名称分离，未选目录保持原顺序。 */
export function decodeConfig(config) {
  const connections = Object.create(null);
  for (const [name, p] of Object.entries(config.models || {})) {
    const selected = new Set([p.model, ...(p.relayModels || [])].filter(Boolean));
    const catalog = p.modelCatalog || [], order = [...new Set([...catalog.map(m => m.id), ...(p.modelOrder || []), ...selected])];
    const models = order.map(id => {
      const known = catalog.find(m => m.id === id) || {}, context = modelValue(p.modelOfficialContexts, id) ?? known.context ?? null;
      const policy = typeof modelValue(p.modelContextOverrides, id) === 'boolean' ? {contextEnabled: modelValue(p.modelContextOverrides, id), contextOverride: modelValue(p.modelContextWindows, id) ?? (Math.min(context || 0, 256000) || null)} : modelValue(p.modelContextWindows, id) ? {contextEnabled: true, contextOverride: modelValue(p.modelContextWindows, id)} : defaultContextSettings(context);
      return {id, context, ...policy, selected: selected.has(id), name: modelValue(p.modelNames, id) ?? known.name ?? '', fast: modelValue(p.modelFastModes, id) === true, image: typeof modelValue(p.modelImageInputs, id) === 'boolean' ? modelValue(p.modelImageInputs, id) ? 'yes' : 'no' : 'auto', output: modelValue(p.modelOutputs, id)?.mode === 'custom' ? String(modelValue(p.modelOutputs, id).tokens) : '', outputMode: modelValue(p.modelOutputs, id)?.mode || 'auto', compatibility: modelValue(p.modelCompatibility, id) || 'auto'};
    });
    const id = p.id || name;
    connections[id] = {id, name, savedName: name, protocol: p.protocol, url: p.baseUrl, apiKey: '', apiKeyEnv: p.apiKeyEnv || '', hasKey: Boolean(p.hasKey), description: p.description || '', models, defaultID: p.model, effort: p.reasoningEffort === 'ultra' ? 'max' : p.reasoningEffort || 'default', override: p.advancedOverride ?? true, maxTokens: p.maxTokens ?? 4096, firstTimeout: p.firstResponseTimeoutSeconds ?? 180, idleTimeout: p.streamIdleTimeoutSeconds ?? 120, taskTimeout: p.taskTimeoutMinutes ?? 15, response: p.stream === false ? 'complete' : 'stream'};
  }
  const plans = clone(config.accessConfigs || []);
  return {...emptyStore(), connections: clone(connections), savedConnections: connections, plans, savedPlans: Object.fromEntries(plans.map(p => [p.id, clone(p)])), selected: plans[0]?.id || ''};
}

/** 保存已选模型的显式 Fast 设置；空 Key 沿用脱敏凭据，不把未选目录的设置带入连接。 */
export function encodeConnection(c) {
  const ids = c.models.filter(m => m.selected).map(m => m.id);
  const [context, native, overrides, names, images, outputs, compatibility, fastModes] = Array.from({length: 8}, () => Object.create(null));
  for (const m of c.models) {
    if (!m.selected) continue;
    if (m.context) native[m.id] = m.context;
    const policy = contextSettings(m);
    overrides[m.id] = policy.contextEnabled;
    if (policy.contextEnabled && policy.contextOverride != null) context[m.id] = Number(policy.contextOverride);
    if (m.name) names[m.id] = m.name;
    if (m.fast === true) fastModes[m.id] = true;
    if (m.image !== 'auto') images[m.id] = m.image === 'yes';
    if (m.output !== '') outputs[m.id] = {mode: 'custom', tokens: Number(m.output)}; else if (['upstream', 'volcengine'].includes(m.outputMode)) outputs[m.id] = {mode: m.outputMode};
    if (m.compatibility && m.compatibility !== 'auto') compatibility[m.id] = m.compatibility;
  }
  return {id: c.id, savedName: c.savedName, copySource: c.copySource, protocol: c.protocol, baseUrl: c.url, apiKey: c.apiKey || '', apiKeyEnv: c.apiKey ? '' : c.apiKeyEnv || '', hasKey: c.hasKey, model: c.defaultID, relayModels: ids.filter(id => id !== c.defaultID), modelOrder: ids, modelNames: names, modelFastModes: fastModes, modelOfficialContexts: native, modelContextWindows: context, modelContextOverrides: overrides, modelCompatibility: compatibility, modelImageInputs: images, modelOutputs: outputs, modelCatalog: c.models.map(m => ({id: m.id, name: m.name || '', context: m.context || 0})), description: c.description, reasoningEffort: c.effort === 'default' ? '' : c.effort, advancedOverride: c.override, maxTokens: Number(c.maxTokens), stream: c.response === 'stream', firstResponseTimeoutSeconds: Number(c.firstTimeout), streamIdleTimeoutSeconds: Number(c.idleTimeout), taskTimeoutMinutes: Number(c.taskTimeout)};
}

/** 合并保存快照；有提交副本时保留等待期间的新草稿，只更新凭据引用并清除已提交而未再次修改的 Key。 */
export function mergeSaved(store, config, {connectionID, planID, submittedConnection, submittedPlan, removeConnectionID, removePlanID} = {}) {
  const canonical = decodeConfig(config), next = {...store, savedConnections: canonical.savedConnections, savedPlans: canonical.savedPlans, connections: {...store.connections}, plans: [...store.plans]};
  if (connectionID && canonical.connections[connectionID]) {
    const saved = canonical.connections[connectionID], draft = store.connections[connectionID];
    if (!submittedConnection || (draft && !isDirty(draft, submittedConnection))) next.connections[connectionID] = clone(saved);
    else if (draft) {
      const retained = {...clone(draft), savedName: saved.savedName, hasKey: saved.hasKey};
      if (draft.apiKey === submittedConnection.apiKey) retained.apiKey = '';
      if (draft.apiKeyEnv === submittedConnection.apiKeyEnv) retained.apiKeyEnv = saved.apiKeyEnv;
      delete retained.copySource;
      next.connections[connectionID] = retained;
    }
  }
  if (planID) next.plans = next.plans.map(p => p.id === planID && (!submittedPlan || !isDirty(p, submittedPlan)) ? clone(canonical.savedPlans[p.id] || p) : p);
  if (removeConnectionID) {
    delete next.connections[removeConnectionID];
    next.plans = next.plans.map(p => {
      const copy = clone(p); copy.connectionIDs = copy.connectionIDs.filter(id => id !== removeConnectionID); delete copy.modelIDs[removeConnectionID];
      if (copy.defaultKey && JSON.parse(copy.defaultKey)[0] === removeConnectionID) copy.defaultKey = '';
      return copy;
    });
    if (next.active?.connectionIDs.includes(removeConnectionID)) {next.active = null; next.enabled = false;}
  }
  if (removePlanID) {next.plans = next.plans.filter(p => p.id !== removePlanID); if (next.active?.id === removePlanID) {next.active = null; next.enabled = false;} if (next.selected === removePlanID) next.selected = next.plans[0]?.id || '';}
  return next;
}

/** 构造接入快照仅供展示；过期引用保留摘要但不会被判为已应用，正式开启仍由后端严格校验。 */
export function displaySnapshot(plan, connections) {
  if (!plan) return null;
  try {return makeSnapshot(plan, connections);} catch {return {id: plan.id, name: plan.name, connectionIDs: [...plan.connectionIDs], defaultKey: plan.defaultKey, models: plan.connectionIDs.flatMap(id => (plan.modelIDs[id] || []).map(mid => ({connectionID: id, connectionName: connections[id]?.name || id, modelID: mid})))};}
}

/** 合并上游目录数据，不默认选中新模型，也不改变连接默认或任何接入配置范围。 */
export function mergeCatalog(c, result) {
  const next = clone(c);
  for (const item of result.models || []) {
    const id = item.id; if (!id || typeof id !== 'string') continue;
    const existing = next.models.find(m => m.id === id);
    if (existing) {if (item.contextWindow > 0) {existing.context = item.contextWindow; if (existing.contextEnabled && existing.contextOverride == null) Object.assign(existing, defaultContextSettings(item.contextWindow));} continue;}
    next.models.push({id, name: '', context: item.contextWindow || null, ...defaultContextSettings(item.contextWindow), selected: false, image: 'auto', output: '', outputMode: 'auto', compatibility: 'auto', fast: false});
  }
  return next;
}

/** 目录只属于发起查询时的地址、协议和凭据来源；本地模型编辑不使结果失效，改 Key 则必须重新查询。 */
export function sameCatalogSource(current, submitted) {
  return Boolean(current && submitted) && ['id', 'url', 'protocol', 'apiKey', 'apiKeyEnv', 'hasKey', 'savedName', 'copySource'].every(field => current[field] === submitted[field]);
}
