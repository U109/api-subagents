/** 深复制配置数据，隔离连接、配置草稿、保存状态与实际接入快照。 */
export function clone(value) { return structuredClone(value); }

/** 获取字典自有成员，失效引用不落到 Object.prototype 上；JSON 自由名称仍可正常使用。 */
export function own(object, key) {return object && Object.hasOwn(object, key) ? object[key] : undefined;}

/** 以连接和模型联合生成身份；JSON 编码避免同名或分隔符造成碰撞。 */
export function modelKey(connectionID, modelID) { return JSON.stringify([connectionID, modelID]); }

/** 已知原生容量默认开启上下文设置并取 min(原生值, 256K)，未知容量不填假值。 */
export function defaultContextSettings(context) {
  const known = Number.isInteger(context) && context > 0;
  return {contextEnabled: known, contextOverride: known ? Math.min(context, 256000) : null};
}

/** 兼容未设置策略的模型；显式关闭后不产生自定义上下文参数。 */
export function contextSettings(model) {
  if (typeof model?.contextEnabled !== 'boolean') return defaultContextSettings(model?.context);
  return {contextEnabled: model.contextEnabled, contextOverride: model.contextEnabled ? model.contextOverride : null};
}

/** 返回预览和接入快照使用的容量；关闭覆盖时保留原生值，未知保持 null。 */
export function effectiveContext(model) {
  const policy = contextSettings(model);
  return (policy.contextEnabled ? policy.contextOverride : model?.context) ?? null;
}

/** 仅重新排列可见项所在的槽位，隐藏项、勾选及模型元数据保持不变；非法落点不修改。 */
export function reorderModels(models, activeID, overID, visibleIDs = models.map(m => m.id)) {
  const ids = new Set(visibleIDs), from = visibleIDs.indexOf(activeID), to = visibleIDs.indexOf(overID);
  if (from < 0 || to < 0 || from === to || ids.size !== visibleIDs.length || visibleIDs.some(id => !models.some(m => m.id === id))) return models;
  const ordered = visibleIDs.map(id => models.find(m => m.id === id));
  ordered.splice(to, 0, ordered.splice(from, 1)[0]);
  let index = 0;
  return models.map(m => ids.has(m.id) ? ordered[index++] : m);
}

/** 创建持有独立模型子集的配置，不修改或共享连接中的 selected 属性。 */
export function makePlan(id, name, connectionIDs, saved, subset) {
  const modelIDs = Object.fromEntries(connectionIDs.map(cid => [cid, [...(subset?.[cid] ?? own(saved, cid)?.models.filter(m => m.selected).map(m => m.id) ?? [])]]));
  const cid = connectionIDs.find(cid => modelIDs[cid]?.length);
  return {id, name, connectionIDs: [...connectionIDs], modelIDs, defaultKey: cid ? modelKey(cid, modelIDs[cid][0]) : ''};
}

/** 获取配置中的独立已选范围；过期引用仍保留，禁止隐式删除或扩大范围。 */
export function planModels(plan, saved) {
  if (!plan) return [];
  return plan.connectionIDs.flatMap(cid => (plan.modelIDs[cid] ?? []).map(mid => {
    const c = own(saved, cid), m = c?.models.find(m => m.id === mid && m.selected);
    return {connectionID: cid, connectionName: c?.name ?? cid, modelID: mid, key: modelKey(cid, mid), context: effectiveContext(m), nativeContext: m?.context ?? null, contextOverride: contextSettings(m).contextOverride, name: m?.name || mid, available: Boolean(m)};
  }));
}

/** 判断草稿与最后保存状态的差异；新建对象自然被视为未保存。 */
export function isDirty(draft, saved) { return JSON.stringify(draft) !== JSON.stringify(saved); }

/** 校验配置自身及引用边界，返回可显示在操作附近的错误。 */
export function validatePlan(plan, saved, drafts = saved) {
  if (!plan?.name.trim()) return '请填写配置名称。';
  if (!plan.connectionIDs.length) return '请至少添加一个连接。';
  if (plan.connectionIDs.some(cid => !own(saved, cid))) return '包含未保存或已删除的连接，请先保存连接。';
  if (plan.connectionIDs.some(cid => isDirty(own(drafts, cid), own(saved, cid)))) return '包含未保存的连接修改，请先保存或还原连接。';
  const models = planModels(plan, saved);
  if (!models.length) return '请在 Codex 模型预览中至少选择一个模型。';
  if (models.some(m => !m.available)) return '有模型已不在连接的可用范围内，请取消这些模型或恢复连接。';
  if (!models.some(m => m.key === plan.defaultKey)) return '请星标一个已选模型作为此配置的默认模型。';
  return '';
}

/** 勾选只写入配置级 ID 集合；移除默认后留空，要求用户明确指定新默认。 */
export function choosePlanModel(plan, cid, mid, selected) {
  const next = clone(plan), ids = new Set(next.modelIDs[cid] ?? []);
  if (selected) ids.add(mid); else ids.delete(mid);
  next.modelIDs[cid] = [...ids];
  if (!selected && next.defaultKey === modelKey(cid, mid)) next.defaultKey = '';
  return next;
}

/** 显式添加连接时复制候选范围；移除连接后清理本配置的模型，不触碰其他配置。 */
export function chooseConnection(plan, cid, selected, saved) {
  const next = clone(plan);
  if (selected && !next.connectionIDs.includes(cid)) {
    next.connectionIDs.push(cid);
    next.modelIDs[cid] = own(saved, cid)?.models.filter(m => m.selected).map(m => m.id) ?? [];
  } else if (!selected) {
    next.connectionIDs = next.connectionIDs.filter(id => id !== cid);
    delete next.modelIDs[cid];
    if (next.defaultKey && JSON.parse(next.defaultKey)[0] === cid) next.defaultKey = '';
  }
  return next;
}

/** 生成实际接入快照；必须先校验，空集合不被解释成所有模型。 */
export function makeSnapshot(plan, saved, drafts = saved) {
  const error = validatePlan(plan, saved, drafts);
  if (error) throw new Error(error);
  return {id: plan.id, name: plan.name, connectionIDs: [...plan.connectionIDs], defaultKey: plan.defaultKey, models: planModels(plan, saved).map(m => ({...m, connection: clone(saved[m.connectionID])}))};
}

/** 仅保存当前配置，运行快照与其他配置保持不变。 */
export function savePlan(store, id) {
  const next = clone(store), plan = next.plans.find(p => p.id === id);
  const error = validatePlan(plan, next.savedConnections, next.connections);
  if (error) throw new Error(error);
  next.savedPlans[id] = clone(plan);
  return next;
}

/** 用户显式保存并启用后才切换接入范围，记录完全隔离的保存快照。 */
export function activatePlan(store, id) {
  const next = savePlan(store, id);
  next.active = makeSnapshot(next.savedPlans[id], next.savedConnections, next.connections);
  next.enabled = true;
  return next;
}

/** 删除共享连接并清理所有配置引用；影响当前运行范围时先关闭接入。 */
export function deleteConnection(store, cid) {
  const next = clone(store);
  delete next.connections[cid]; delete next.savedConnections[cid];
  next.plans = next.plans.map(p => chooseConnection(p, cid, false, next.savedConnections));
  next.savedPlans = Object.fromEntries(Object.entries(next.savedPlans).map(([id, p]) => [id, chooseConnection(p, cid, false, next.savedConnections)]));
  if (next.active?.connectionIDs.includes(cid)) { next.enabled = false; next.active = null; }
  return next;
}
