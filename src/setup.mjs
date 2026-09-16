import http from 'node:http';
import fs from 'node:fs/promises';
import crypto from 'node:crypto';
import path from 'node:path';
import {
  readConfig,
  saveConfig,
  publicConfig,
  validateConfig,
  resolveProfile,
  configPath,
} from './config.mjs';
import {probe} from './providers.mjs';
import {listRemoteModels} from './catalog.mjs';

/** 以字节为单位限制请求体并统一解码 UTF-8；解析错误不回显含 Key 的输入。 */
async function readBody(req) {
  const chunks = [];
  let size = 0;
  // 先拼字节再解码，中文 UTF-8 字符可能横跨两个网络数据块。
  for await (const chunk of req.iterator({destroyOnReturn: false})) {
    size += chunk.length;
    if (size > 300000) {
      req.resume();
      throw Object.assign(new Error('配置过大。'), {status: 413});
    }
    chunks.push(chunk);
  }
  try {
    const body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    if (!body || typeof body !== 'object' || Array.isArray(body)) throw new Error();
    return body;
  } catch {
    // JSON.parse 的原始错误可能包含请求片段，其中可能有 API Key。
    throw new Error('请求 JSON 无效，请检查配置格式。');
  }
}

/** 给配置页附上原保存名称，供改名和复制追溯凭据；此标记不会写入配置文件。 */
function editableConfig(config) {
  const result = publicConfig(config);
  for (const [name, profile] of Object.entries(result.models)) profile.savedName = name;
  return result;
}

/** 合并原连接的凭据并校验改名来源；即使改名或复制，也不能跨地址静默复用 Key。 */
function mergeSavedKeys(input, previous, options) {
  const config = validateConfig(input, options);
  const sources = new Set();
  for (const [name, next] of Object.entries(config.models)) {
    const raw = input.models[name];
    const source = raw.savedName ?? name;
    if (raw.savedName !== undefined) {
      if (typeof source !== 'string' || !Object.hasOwn(previous.models, source))
        throw new Error('原模型配置已不存在，请刷新后重试。');
      if (source !== name && Object.hasOwn(previous.models, name))
        throw new Error('调用名称已被另一连接使用，请换一个名称。');
      if (sources.has(source)) throw new Error('同一连接不能重复改名，请通过“复制”创建副本。');
      sources.add(source);
    }
    const old = Object.hasOwn(previous.models, source) ? previous.models[source] : undefined;
    if (!old) continue;
    const keepKey = !next.apiKey && raw.hasKey && old.apiKey;
    const keepEnv = next.apiKeyEnv && next.apiKeyEnv === old.apiKeyEnv;
    const changedEndpoint = next.protocol !== old.protocol || next.baseUrl !== old.baseUrl;
    // 已保存的凭据（包括环境变量）不能随地址修改而无提示地发给另一个服务。
    if (changedEndpoint && (keepKey || keepEnv)) {
      throw new Error('API 地址或接口类型已改变，请重新填写 Key，并清除原环境变量设置后保存或测试。');
    }
    if (keepKey) next.apiKey = old.apiKey;
  }
  return config;
}

/** 生成合法且不覆盖保存项或页面草稿的副本名称，后缀计数也计入 48 字符限制。 */
function copyName(name, taken) {
  for (let index = 1; ; index++) {
    const suffix = index === 1 ? '-copy' : `-copy-${index}`;
    const candidate = name.slice(0, 48 - suffix.length) + suffix;
    if (!taken.has(candidate)) return candidate;
  }
}

/** 启动仅监听回环地址的配置服务，通过 Host、Origin、随机令牌和保存锁保护本机配置。 */
export async function startSetup({
  htmlPath,
  configFile = configPath(),
  port = 0,
  probeImpl = probe,
  listModelsImpl = listRemoteModels,
} = {}) {
  const token = crypto.randomBytes(32).toString('hex');
  let origin,
    host,
    saving = false;
  const server = http.createServer(async (req, res) => {
    res.setHeader('Cache-Control', 'no-store');
    res.setHeader('X-Content-Type-Options', 'nosniff');
    res.setHeader('Referrer-Policy', 'no-referrer');
    res.setHeader(
      'Content-Security-Policy',
      "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
    );
    /** 统一输出 UTF-8 JSON，错误响应也使用相同格式供配置页显示。 */
    const json = (status, value) => {
      res.writeHead(status, {'Content-Type': 'application/json; charset=utf-8'});
      res.end(JSON.stringify(value));
    };
    try {
      if (req.headers.host !== host || (req.headers.origin && req.headers.origin !== origin))
        return json(403, {error: '拒绝跨站请求。'});
      if (req.url === '/' && req.method === 'GET') {
        res.setHeader('Content-Type', 'text/html; charset=utf-8');
        return res.end(await fs.readFile(htmlPath));
      }
      if (
        ['/config-ui.js', '/config.css', '/desktop-ui.js', '/desktop.css'].includes(req.url) &&
        req.method === 'GET'
      ) {
        res.setHeader(
          'Content-Type',
          req.url.endsWith('.css') ? 'text/css; charset=utf-8' : 'text/javascript; charset=utf-8',
        );
        return res.end(await fs.readFile(path.join(path.dirname(htmlPath), req.url.slice(1))));
      }
      if (req.headers.authorization !== `Bearer ${token}`)
        return json(401, {error: '请从配置启动器重新打开页面。'});
      if (req.url === '/api/config' && req.method === 'GET')
        return json(200, {config: editableConfig(await readConfig(configFile)), path: configFile});
      if (
        !['/api/config', '/api/config/remove', '/api/config/copy', '/api/probe', '/api/models'].includes(
          req.url,
        ) ||
        req.method !== 'POST'
      )
        return json(404, {error: '未找到操作。'});
      if (!(req.headers['content-type'] || '').startsWith('application/json'))
        return json(415, {error: '需要 JSON 请求。'});
      const input = await readBody(req);

      // 测试只读取当前模型，不锁住保存，也不受其他未填完模型的影响。
      if (req.url === '/api/probe' || req.url === '/api/models') {
        const single = {...input.config, models: {[input.name]: input.config?.models?.[input.name]}};
        const listing = req.url === '/api/models';
        const config = mergeSavedKeys(single, await readConfig(configFile), {requireModel: !listing});
        const profile = resolveProfile(config, input.name);
        return json(200, await (listing ? listModelsImpl(profile) : probeImpl(profile)));
      }
      if (saving) return json(409, {error: '正在保存，请稍后重试。'});
      saving = true;
      try {
        let config = await readConfig(configFile);
        let copiedName;
        if (req.url === '/api/config/remove') {
          if (typeof input.name !== 'string' || !Object.hasOwn(config.models, input.name))
            return json(404, {error: '该模型配置不存在，请刷新后重试。'});
          // 从最新已保存配置中只删除目标，不提交页面上其他尚未保存的编辑。
          delete config.models[input.name];
        } else if (req.url === '/api/config/copy') {
          if (typeof input.name !== 'string' || !Object.hasOwn(input.config?.models || {}, input.name))
            return json(404, {error: '该模型配置不存在，请刷新后重试。'});
          // 只校验和复制目标草稿；其他未填完连接及并发数草稿不影响复制，也不被保存。
          const single = {...config, models: {[input.name]: input.config.models[input.name]}};
          const source = mergeSavedKeys(single, config).models[input.name];
          copiedName = copyName(
            input.name,
            new Set([...Object.keys(config.models), ...Object.keys(input.config.models)]),
          );
          config.models[copiedName] = source;
        } else config = mergeSavedKeys(input.config, config);
        await saveConfig(config, configFile);
        return json(200, {
          config: editableConfig(config),
          path: configFile,
          ...(copiedName ? {name: copiedName} : {}),
        });
      } finally {
        saving = false;
      }
    } catch (error) {
      json(error.status || 400, {error: error.message || '配置操作失败。'});
    }
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', resolve);
  });
  host = `127.0.0.1:${server.address().port}`;
  origin = `http://${host}`;
  return {server, url: `${origin}/#${token}`, token, origin};
}
