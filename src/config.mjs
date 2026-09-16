import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import crypto from 'node:crypto';

export const defaults = {
  responses: 'https://api.openai.com/v1',
  compatible: 'https://api.openai.com/v1',
  anthropic: 'https://api.anthropic.com/v1',
  gemini: 'https://generativelanguage.googleapis.com/v1beta',
};
/** 返回本机配置与任务记录目录；环境变量用于隔离测试或自定义存储位置。 */
export const dataDir = () =>
  process.env.API_SUBAGENTS_HOME ||
  path.join(process.env.LOCALAPPDATA || path.join(os.homedir(), '.local', 'share'), 'CodexApiSubagents');
/** 模型配置文件的默认绝对路径。 */
export const configPath = () => path.join(dataDir(), 'models.json');
/** 首次运行时的空配置，不写入文件，也不预置真实模型或密钥。 */
export const emptyConfig = () => ({version: 1, maxConcurrent: 3, models: {}});
/** 校验并标准化配置，同时为旧配置补默认值；拉取模型列表时可暂不要求模型 ID。 */
export function validateConfig(input, {requireModel = true} = {}) {
  if (
    !input ||
    input.version !== 1 ||
    !input.models ||
    typeof input.models !== 'object' ||
    Array.isArray(input.models)
  )
    throw new Error('配置需要 version: 1 和 models 对象。');
  const maxConcurrent = input.maxConcurrent ?? 3;
  if (!Number.isInteger(maxConcurrent) || maxConcurrent < 1 || maxConcurrent > 8)
    throw new Error('并发数必须为 1–8。');
  if (Object.keys(input.models).length > 50) throw new Error('最多配置 50 个模型。');
  const models = Object.create(null);
  for (const [name, raw] of Object.entries(input.models)) {
    if (
      !/^[a-z][a-z0-9_-]{0,47}$/.test(name) ||
      ['constructor', 'prototype', '__proto__'].includes(name)
    )
      throw new Error('调用名称需为小写字母、数字、下划线或横线，并以字母开头。');
    if (!raw || !Object.hasOwn(defaults, raw.protocol)) throw new Error(`${name}: 不支持的接口类型。`);
    const model = raw.model ?? '';
    if (typeof model !== 'string' || (requireModel && !model.trim()) || model.length > 200)
      throw new Error(`${name}: 请填写模型 ID。`);
    let url;
    try {
      url = new URL(raw.baseUrl || defaults[raw.protocol]);
    } catch {
      throw new Error(`${name}: API 地址无效，请填写完整 HTTP(S) 地址。`);
    }
    if (
      !['https:', 'http:'].includes(url.protocol) ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    )
      throw new Error(`${name}: API 地址必须是无密码、查询参数的 HTTP(S) 地址。`);
    if (raw.apiKeyEnv && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(raw.apiKeyEnv))
      throw new Error(`${name}: 环境变量名称不合法。`);
    const maxTokens = raw.maxTokens ?? 4096;
    if (!Number.isInteger(maxTokens) || maxTokens < 128 || maxTokens > 32000)
      throw new Error(`${name}: maxTokens 必须为 128–32000。`);
    const stream = raw.stream ?? true;
    if (typeof stream !== 'boolean') throw new Error(`${name}: stream 必须为布尔值。`);
    const firstResponseTimeoutSeconds = raw.firstResponseTimeoutSeconds ?? 180;
    const streamIdleTimeoutSeconds = raw.streamIdleTimeoutSeconds ?? 120;
    const taskTimeoutMinutes = raw.taskTimeoutMinutes ?? 15;
    for (const [field, value, min, max] of [
      ['firstResponseTimeoutSeconds', firstResponseTimeoutSeconds, 10, 600],
      ['streamIdleTimeoutSeconds', streamIdleTimeoutSeconds, 10, 600],
      ['taskTimeoutMinutes', taskTimeoutMinutes, 1, 60],
    ]) {
      if (!Number.isInteger(value) || value < min || value > max)
        throw new Error(`${name}: ${field} 必须为 ${min}–${max}。`);
    }
    if (raw.apiKey !== undefined && typeof raw.apiKey !== 'string')
      throw new Error(`${name}: API Key 必须是字符串。`);
    models[name] = {
      protocol: raw.protocol,
      model: model.trim(),
      baseUrl: url.href.replace(/\/$/, ''),
      apiKey: raw.apiKey || '',
      apiKeyEnv: raw.apiKeyEnv || '',
      description: String(raw.description || '').slice(0, 300),
      maxTokens,
      stream,
      firstResponseTimeoutSeconds,
      streamIdleTimeoutSeconds,
      taskTimeoutMinutes,
    };
  }
  return {version: 1, maxConcurrent, models};
}
/** 读取并校验配置；文件不存在视为首次使用，解析失败不回显可能含密钥的原文。 */
export async function readConfig(file = configPath()) {
  let content;
  try {
    content = await fs.readFile(file, 'utf8');
  } catch (error) {
    if (error.code === 'ENOENT') return emptyConfig();
    throw new Error('无法读取模型配置文件，请检查文件权限。');
  }
  let parsed;
  try {
    parsed = JSON.parse(content.replace(/^\uFEFF/, ''));
  } catch {
    throw new Error('模型配置 JSON 无效，请检查 models.json 的格式。');
  }
  return validateConfig(parsed);
}
/** 先校验，再通过同目录临时文件原子替换配置，避免中断后留下半份 JSON。 */
export async function saveConfig(input, file = configPath()) {
  const config = validateConfig(input);
  await fs.mkdir(path.dirname(file), {recursive: true, mode: 0o700});
  const temp = `${file}.${crypto.randomUUID()}.tmp`;
  try {
    await fs.writeFile(temp, JSON.stringify(config, null, 2) + '\n', {mode: 0o600});
    await fs.rename(temp, file);
  } finally {
    await fs.rm(temp, {force: true});
  }
  return config;
}
/** 生成供配置页展示的副本：隐藏已保存的 Key，仅提供 hasKey 提示。 */
export function publicConfig(config) {
  return {
    ...config,
    models: Object.fromEntries(
      Object.entries(config.models).map(([name, m]) => [
        name,
        {...m, apiKey: '', hasKey: Boolean(m.apiKey || (m.apiKeyEnv && process.env[m.apiKeyEnv]))},
      ]),
    ),
  };
}
/** 解析指定模型的实际凭据；环境变量优先，变量缺失时明确报错而不回退到旧 Key。 */
export function resolveProfile(config, name) {
  if (!Object.hasOwn(config.models, name))
    throw new Error(`未配置模型 ${name}。请打开配置页面添加模型。`);
  const model = config.models[name];
  const apiKey = model.apiKeyEnv ? process.env[model.apiKeyEnv] : model.apiKey;
  if (model.apiKeyEnv && !apiKey) throw new Error(`未找到环境变量 ${model.apiKeyEnv}。`);
  return {...model, apiKey: apiKey || ''};
}
/** 对错误和结果文本中的已知 API Key、Bearer 凭据脱敏，不把密钥写入任务记录。 */
export function redact(text, profiles = []) {
  let value = String(text);
  for (const model of profiles) if (model.apiKey) value = value.split(model.apiKey).join('[REDACTED]');
  return value.replace(/Bearer\s+[A-Za-z0-9._~+\/-]+/gi, 'Bearer [REDACTED]');
}

/** 递归脱敏值而保留字段名与 JSON 结构；用于文件建议，调用方需标记被修改的内容。 */
export function redactValue(value, profiles) {
  if (typeof value === 'string') return redact(value, profiles);
  if (Array.isArray(value)) return value.map((item) => redactValue(item, profiles));
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value).map(([key, item]) => [key, redactValue(item, profiles)]),
    );
  }
  return value;
}
