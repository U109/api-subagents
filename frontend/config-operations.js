// 配置操作的可测试状态逻辑：不读取文件，不自行访问接口，所有服务调用由正式桥接层注入。
(() => {
  /** 克隆已脱敏的可编辑配置；数据只来自现有配置 API，不自行读取或保留真实 Key。 */
  function clone(value) { return JSON.parse(JSON.stringify(value)); }

  /** 保存服务端确认过的连接快照，恢复单个草稿时不影响其他连接。 */
  function createSnapshots() {
    const savedProfiles = new Map();
    return {
      /** 完整保存或首次加载后替换所有已保存快照。 */
      remember(config) { savedProfiles.clear(); for (const [name, profile] of Object.entries(config.models)) savedProfiles.set(name, clone(profile)); },
      /** 定点复制只添加新连接的服务端结果，不覆盖其他未保存草稿。 */
      rememberOne(name, profile) { savedProfiles.set(name, clone(profile)); },
      /** 成功删除后清理对应快照，防止恢复已经持久删除的配置。 */
      forget(name) { savedProfiles.delete(name); },
      /** 恢复选中连接或移除未保存的新连接；名称冲突时抛错且不修改输入配置。 */
      restore(config, selected) {
        const profile = config.models[selected];
        if (!profile) throw Error('当前连接不存在，请重新选择');
        const original = profile.savedName;
        const models = Object.assign(Object.create(null), config.models);
        if (original) {
          if (!savedProfiles.has(original)) throw Error('没有可恢复的保存记录，请重新加载配置');
          if (original !== selected && Object.hasOwn(models, original)) throw Error('原名称已被其他草稿占用，请先调整连接名称');
          delete models[selected];
          models[original] = clone(savedProfiles.get(original));
          return {config:{...config, models}, selected:original};
        }
        delete models[selected];
        return {config:{...config, models}, selected:Object.keys(models)[0] || null};
      }
    };
  }

  /** 创建可取消的连接测试；桌面请求通过随机 ID 取消，HTTP 请求通过 AbortSignal 取消。 */
  function createProbe({api, cancelDesktop, onState, idFactory = () => crypto.randomUUID()}) {
    let active = null;
    return {
      /** 执行唯一活动测试，取消后即使迟到成功也不能覆盖取消状态。 */
      async run(config, name) {
        if (active) return false;
        const request = {id:idFactory(), controller:new AbortController(), cancelled:false};
        active = request;
        onState('running', '正在测试连接，最多等待 30 秒…');
        try {
          await api('/api/probe', {config, name, requestId:request.id}, {signal:request.controller.signal});
          onState(request.cancelled ? 'cancelled' : 'success', request.cancelled ? '连接测试已取消' : '连接测试成功');
          return !request.cancelled;
        } catch (error) {
          if (request.cancelled) { onState('cancelled', '连接测试已取消'); return false; }
          onState('error', typeof error === 'string' ? error : error.message || '连接测试失败，请检查地址和 Key 后重试');
          return false;
        } finally { if (active === request) active = null; }
      },
      /** 发出取消意图并等待原测试结束；不提前解除配置请求锁。 */
      async cancel() {
        const request = active;
        if (!request || request.cancelled) return false;
        request.cancelled = true;
        onState('cancelling', '正在取消连接测试…');
        request.controller.abort();
        if (cancelDesktop) {
          try { await cancelDesktop(request.id); }
          catch { if (active === request) onState('cancelling', '取消请求未确认，正在等待测试结束或超时…'); }
        }
        return true;
      },
      /** 提供只读忙碌状态供关闭和按钮保护使用，不暴露请求体或凭据。 */
      isRunning() { return active !== null; }
    };
  }
  /** 计算已知容量的实际值；缺失、非法和小数数据均保持待确认。 */
  function effectiveContext(value) { return Number.isSafeInteger(value) && value > 0 && value <= 100000000 ? Math.min(value,256000) : null; }

  /** 同步已选模型的明确目录元数据；清理已移除项，不推测未知模型容量。 */
  function applyCatalogueContexts(profile, catalog) {
    const selected = new Set([profile.model,...(profile.relayModels || [])].filter(Boolean));
    const contexts = Object.assign(Object.create(null), profile.modelOfficialContexts);
    for (const id of Object.keys(contexts)) if (!selected.has(id)) delete contexts[id];
    for (const model of catalog?.models || []) {
      if (!selected.has(model.id)) continue;
      if (effectiveContext(model.contextWindow) !== null) contexts[model.id] = model.contextWindow;
      else delete contexts[model.id];
    }
    const changed = JSON.stringify(profile.modelOfficialContexts || {}) !== JSON.stringify(contexts);
    if (changed) profile.modelOfficialContexts = contexts;
    return changed;
  }
  const exports = {createSnapshots, createProbe, effectiveContext, applyCatalogueContexts};
  if (typeof module !== 'undefined' && module.exports) module.exports = exports;
  else window.configOperations = exports;
})();
