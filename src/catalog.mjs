import {fetchJSON} from './providers.mjs';
import {redact} from './config.mjs';

/** 只读拉取模型目录并统一名称、去重及分页；最多 10 页/1000 项，游标不改变服务地址。 */
export async function listRemoteModels(
  profile,
  {fetchImpl = fetch, signal = AbortSignal.timeout(20000)} = {},
) {
  const {protocol, apiKey} = profile;
  const suffix = {compatible: '/chat/completions', responses: '/responses', anthropic: '/messages'}[
    protocol
  ];
  let base = profile.baseUrl.replace(/\/$/, '');
  if (suffix && base.endsWith(suffix)) base = base.slice(0, -suffix.length);
  const endpoint = base.endsWith('/models') ? base : base + '/models';
  const headers = {accept: 'application/json'};
  if (protocol === 'anthropic') {
    headers['x-api-key'] = apiKey;
    headers['anthropic-version'] = '2023-06-01';
  } else if (protocol === 'gemini') headers['x-goog-api-key'] = apiKey;
  else if (apiKey) headers.authorization = `Bearer ${apiKey}`;

  const models = new Map(),
    cursors = new Set();
  let cursor = '',
    truncated = false;
  try {
    for (let page = 0; page < 10; page++) {
      signal.throwIfAborted();
      const url = new URL(endpoint);
      if (protocol === 'anthropic') {
        url.searchParams.set('limit', '100');
        if (cursor) url.searchParams.set('after_id', cursor);
      } else if (protocol === 'gemini') {
        url.searchParams.set('pageSize', '100');
        if (cursor) url.searchParams.set('pageToken', cursor);
      }
      const data = await fetchJSON({url: url.href, method: 'GET', headers}, signal, fetchImpl);
      const entries = protocol === 'gemini' ? data?.models : data?.data;
      if (!Array.isArray(entries)) throw new Error('接口未返回标准模型列表，可切换为手动输入。');
      for (const entry of entries) {
        if (!entry || typeof entry !== 'object') continue;
        if (
          protocol === 'gemini' &&
          Array.isArray(entry.supportedGenerationMethods) &&
          !entry.supportedGenerationMethods.includes('generateContent')
        )
          continue;
        const rawId = protocol === 'gemini' ? entry.name : entry.id;
        if (typeof rawId !== 'string') continue;
        const id = protocol === 'gemini' ? rawId.replace(/^models\//, '') : rawId;
        if (!id.trim() || id.length > 200) continue;
        const name = entry.display_name || entry.displayName || id;
        models.set(id, {id, name: String(name).slice(0, 160)});
        if (models.size >= 1000) {
          truncated = true;
          break;
        }
      }
      cursor =
        protocol === 'anthropic' && data.has_more
          ? data.last_id
          : protocol === 'gemini'
            ? data.nextPageToken
            : '';
      if (!cursor || truncated) {
        if (protocol === 'anthropic' && data.has_more && !cursor)
          throw new Error('模型列表分页信息不完整，可切换为手动输入。');
        break;
      }
      if (typeof cursor !== 'string' || cursor.length > 4096 || cursors.has(cursor))
        throw new Error('模型列表分页异常，可切换为手动输入。');
      cursors.add(cursor);
      if (page === 9) truncated = true;
    }
    return {models: [...models.values()], truncated};
  } catch (error) {
    if (signal.aborted) throw new Error('拉取模型列表超时或已取消，请重试或手动输入。');
    if ([404, 405, 501].includes(error.status))
      throw new Error('此接口不支持拉取模型列表，请切换为手动输入。');
    throw new Error(redact(error.message || '拉取模型列表失败，请检查连接信息。', [profile]));
  }
}
